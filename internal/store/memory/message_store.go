package memory

import (
        "context"
        "sync"
        "telesrv/internal/domain"
)

// MessageStore 是 store.MessageStore 的内存实现。
type MessageStore struct {
        mu                  sync.RWMutex
        noForwardsMu        sync.Mutex
        m                   map[int64][]domain.Message
        nextUID             int64
        nextBox             map[int64]int
        nextPts             map[int64]int
        readOutboxDates     map[readOutboxDateKey]int
        privateReactions    map[int64]map[int64][]domain.ChannelMessagePeerReaction
        savedMessageTags    map[int64]map[int][]domain.MessageReaction
        savedTagTitles      map[int64]map[string]string
        privateSendDedup    map[privateSendDedupKey]privateSendDedupRecord
        loginCodeDeliveries map[[32]byte]loginCodeDeliveryRecord
        albumGroups         map[albumGroupKey]albumGroupRecord
        dialogs             *DialogStore
        // polls 是共享 poll 权威（投票校验与读路径 enrichment）；nil 时 poll 链路按未接入处理。
        polls *PollStore
        // savedPins 是收藏夹子会话置顶顺序（下标即 pinned_order，越小越前）。
        savedPins map[int64][]domain.Peer
        // privateNoForwards is keyed by the sorted user pair. Requests are keyed by
        // the shared logical private-message id so both local box ids resolve to one
        // one-shot response fact.
        privateNoForwards         map[privateNoForwardsPair]domain.PrivateNoForwardsState
        privateNoForwardsRequests map[int64]memoryNoForwardsRequest
        // stars — привязанный звёздный ledger для списания платных сообщений
        // (AttachStarsStore); nil → плата не списывается (тесты без звёзд).
        stars *StarsStore
}

// AttachPollStore 注入共享 poll 权威（与 ChannelStore 共用同一实例）。
func (s *MessageStore) AttachPollStore(polls *PollStore) {
        s.polls = polls
}

type readOutboxDateKey struct {
        ownerUserID int64
        peerID      int64
        msgID       int
}

// NewMessageStore 创建内存 MessageStore。
func NewMessageStore(dialogs ...*DialogStore) *MessageStore {
        s := &MessageStore{
                m:                         make(map[int64][]domain.Message),
                nextUID:                   1,
                nextBox:                   make(map[int64]int),
                nextPts:                   make(map[int64]int),
                readOutboxDates:           make(map[readOutboxDateKey]int),
                privateReactions:          make(map[int64]map[int64][]domain.ChannelMessagePeerReaction),
                savedMessageTags:          make(map[int64]map[int][]domain.MessageReaction),
                savedTagTitles:            make(map[int64]map[string]string),
                privateSendDedup:          make(map[privateSendDedupKey]privateSendDedupRecord),
                loginCodeDeliveries:       make(map[[32]byte]loginCodeDeliveryRecord),
                albumGroups:               make(map[albumGroupKey]albumGroupRecord),
                savedPins:                 make(map[int64][]domain.Peer),
                privateNoForwards:         make(map[privateNoForwardsPair]domain.PrivateNoForwardsState),
                privateNoForwardsRequests: make(map[int64]memoryNoForwardsRequest),
        }
        if len(dialogs) > 0 {
                s.dialogs = dialogs[0]
        }
        return s
}

// AttachStarsStore связывает звёздный ledger для списания платных сообщений.
// Необязательная связка: без неё memory-реализация не списывает плату
// (тестовые сценарии без звёзд). Порядок блокировок фиксирован:
// MessageStore.mu → StarsStore.mu (обратный порядок не возникает).
func (s *MessageStore) AttachStarsStore(stars *StarsStore) {
        if s == nil {
                return
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        s.stars = stars
}

// PrivateDialogInitiatedBy (раунд 41) — проба инициатора приватного диалога
// пары (a,b): true, если самое раннее сообщение пары отправил a, либо диалога
// ещё нет. Согласована с postgres-реализацией (private_dialog_initiation.go):
// протокол paid-messages берёт плату только с участника, начавшего диалог
// первым, а ответ получателя платы платность не меняет.
func (s *MessageStore) PrivateDialogInitiatedBy(_ context.Context, aUserID, bUserID int64) (bool, error) {
        if s == nil || aUserID <= 0 || bUserID <= 0 || aUserID == bUserID {
                return true, nil
        }
        s.mu.RLock()
        defer s.mu.RUnlock()
        // Обе стороны диалога лежат в боксе a (исходящие и входящие): первое
        // совпадение по Peer.ID в порядке вставки — самое раннее сообщение пары.
        if first, found := firstPrivatePairMessage(s.m[aUserID], bUserID); found {
                return first.Out, nil
        }
        // Бокс a мог быть очищен — пробуем бокс b и инвертируем направление.
        if first, found := firstPrivatePairMessage(s.m[bUserID], aUserID); found {
                return !first.Out, nil
        }
        return true, nil
}

// firstPrivatePairMessage возвращает первое (по порядку вставки) сообщение
// бокса в диалоге с peerUserID.
func firstPrivatePairMessage(box []domain.Message, peerUserID int64) (domain.Message, bool) {
        for _, msg := range box {
                if msg.Peer.ID == peerUserID {
                        return msg, true
                }
        }
        return domain.Message{}, false
}
