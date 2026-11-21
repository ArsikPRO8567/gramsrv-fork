package memory

import (
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
	// starsBalances 是内存私聊付费消息（paid messages）的最小账本：发送方扣款、
	// 收件人等额入账。生产路径的完整 Stars 流水在 postgres StarsStore；内存
	// 实现只维护余额供 rpc 测试断言。nil map 视作 DefaultStarsStartingGrant。
	starsBalances map[int64]int64
}

// SetStarsBalance 预置内存 Stars 余额（付费消息测试用）。
func (s *MessageStore) SetStarsBalance(userID, balance int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starsBalances == nil {
		s.starsBalances = make(map[int64]int64)
	}
	s.starsBalances[userID] = balance
}

// StarsBalance 读取内存 Stars 余额（未预置时返回起始授予额）。
func (s *MessageStore) StarsBalance(userID int64) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if balance, ok := s.starsBalances[userID]; ok {
		return balance
	}
	return domain.DefaultStarsStartingGrant
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
	s.starsBalances = make(map[int64]int64)
	return s
}
