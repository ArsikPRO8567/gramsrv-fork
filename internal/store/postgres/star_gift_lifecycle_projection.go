package postgres

import (
        "context"
        "errors"
        "fmt"
        "sort"

        "github.com/jackc/pgx/v5"

        "telesrv/internal/domain"
        "telesrv/internal/store/postgres/sqlcgen"
)

// insertStarGiftSellerSaleCardTx создаёт в ТЕКУЩЕЙ транзакции само-сообщение
// продавца «Вы продали этот подарок за X» (messageActionStarGiftUnique с
// resale_amount в out-боксе продавца; официальная подпись
// ActionUniqueGiftResaleSoldOutbound). Вставка обязана происходить в той же
// транзакции, что и покупка на маркете: после обновления владельца триггер уже
// выдал событие user_emoji_status, а последующий retire старых карточек даст
// edit_message — итоговый порядок событий продавца
// [user_emoji_status, new_message, edit_message] зафиксирован интеграционным
// тестом. Отдельный sendPrivateText здесь невозможен: он открыл бы вторую
// транзакцию и заблокировался на watermark продавца, уже удержанном внешней
// транзакцией (deadlock).
func insertStarGiftSellerSaleCardTx(
        ctx context.Context,
        tx pgx.Tx,
        messages *MessageStore,
        sellerUserID int64,
        action *domain.MessageStarGiftUniqueAction,
        date int,
) (domain.Message, error) {
        if messages == nil || sellerUserID <= 0 || action == nil || date <= 0 {
                return domain.Message{}, domain.ErrStarGiftResaleUnavailable
        }
        q := sqlcgen.New(tx)
        media := &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
                Kind:           domain.MessageServiceActionStarGiftUnique,
                StarGiftUnique: action,
        }}
        mediaJSON, err := encodeMessageMedia(media)
        if err != nil {
                return domain.Message{}, fmt.Errorf("encode seller sale card media: %w", err)
        }
        pm, err := q.CreatePrivateMessage(ctx, sqlcgen.CreatePrivateMessageParams{
                SenderUserID:       sellerUserID,
                RecipientUserID:    sellerUserID,
                RandomID:           0,
                RequestFingerprint: []byte{},
                RecipientDelivered: false,
                MessageDate:        int32(date),
                EntitiesJson:       []byte("[]"),
                QuoteEntitiesJson:  []byte("[]"),
                MediaJson:          mediaJSON,
                ReplyMarkupJson:    []byte("{}"),
                RichMessageJson:    []byte("{}"),
        })
        if err != nil {
                return domain.Message{}, fmt.Errorf("create seller sale card private message: %w", err)
        }
        boxID, err := messages.boxIDs.NextBoxID(ctx, sellerUserID)
        if err != nil {
                return domain.Message{}, fmt.Errorf("allocate seller sale card box id: %w", err)
        }
        if boxID <= 0 || boxID > domain.MaxMessageBoxID {
                return domain.Message{}, fmt.Errorf("allocate seller sale card box id: %d", boxID)
        }
        pts, err := messages.reservePts(ctx, tx, sellerUserID)
        if err != nil {
                return domain.Message{}, fmt.Errorf("allocate seller sale card pts: %w", err)
        }
        savedPeer := domain.SavedPeerForSelfChat(sellerUserID, nil)
        boxRow, err := q.CreateMessageBox(ctx, sqlcgen.CreateMessageBoxParams{
                OwnerUserID:      sellerUserID,
                BoxID:            int32(boxID),
                PrivateMessageID: pm.ID,
                MessageSenderID:  sellerUserID,
                PeerType:         string(domain.PeerTypeUser),
                PeerID:           sellerUserID,
                FromUserID:       sellerUserID,
                MessageDate:      int32(date),
                Outgoing:         true,
                EntitiesJson:     []byte("[]"),
                QuoteEntitiesJson: []byte("[]"),
                Pts:              int32(pts),
                MediaJson:        mediaJSON,
                ReplyMarkupJson:  []byte("{}"),
                RichMessageJson:  []byte("{}"),
                SavedPeerType:    string(savedPeer.Type),
                SavedPeerID:      savedPeer.ID,
        })
        if err != nil {
                return domain.Message{}, fmt.Errorf("create seller sale card box: %w", err)
        }
        msg := messageFromBoxRow(boxRow)
        if err := insertMessageBoxMediaIndexTx(ctx, tx, sellerUserID, sellerUserID, boxID, date, media, nil); err != nil {
                return domain.Message{}, err
        }
        if err := q.UpsertOutboxDialog(ctx, sqlcgen.UpsertOutboxDialogParams{
                UserID:         sellerUserID,
                PeerType:       string(domain.PeerTypeUser),
                PeerID:         sellerUserID,
                TopMessageID:   int32(msg.ID),
                TopMessageDate: int32(msg.Date),
        }); err != nil {
                return domain.Message{}, fmt.Errorf("upsert seller sale card dialog: %w", err)
        }
        if err := appendNewMessageEvent(ctx, q, msg); err != nil {
                return domain.Message{}, fmt.Errorf("append seller sale card event: %w", err)
        }
        if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
                TargetUserID: sellerUserID, Pts: int32(pts), EventType: string(domain.UpdateEventNewMessage),
                ExcludeAuthKeyID: 0, ExcludeSessionID: 0,
        }); err != nil {
                return domain.Message{}, fmt.Errorf("enqueue seller sale card dispatch: %w", err)
        }
        return msg, nil
}

// retireUserStarGiftMessagesTx closes every user-scoped unique-gift action
// emitted for the source ownership epoch. Ownership moves and terminal export
// must not leave an older chat card with Craft/transfer/resale capabilities.
// The aggregate mutation and all message edits share one transaction and each
// visible box receives its own durable pts/event/outbox entry.
func (s *StarGiftLifecycleStore) retireUserStarGiftMessagesTx(
        ctx context.Context,
        tx pgx.Tx,
        source domain.SavedStarGift,
        current domain.UniqueStarGift,
        date int,
) ([]domain.EditedMessageForUser, error) {
        if s == nil || s.messages == nil || source.Owner.Type != domain.PeerTypeUser || source.Owner.ID <= 0 ||
                source.ID <= 0 || source.UniqueGiftID <= 0 || current.ID != source.UniqueGiftID || date <= 0 {
                return nil, domain.ErrStarGiftTransferUnavailable
        }

        messageIDs := map[int]struct{}{}
        if source.MsgID > 0 {
                messageIDs[source.MsgID] = struct{}{}
        }
        if source.UpgradeMsgID > 0 {
                messageIDs[source.UpgradeMsgID] = struct{}{}
        }
        rows, err := tx.Query(ctx, `
SELECT msg_id FROM star_gift_user_message_refs
WHERE owner_user_id=$1 AND saved_gift_id=$2
ORDER BY msg_id`, source.Owner.ID, source.ID)
        if err != nil {
                return nil, fmt.Errorf("list star gift message projections: %w", err)
        }
        for rows.Next() {
                var msgID int
                if err := rows.Scan(&msgID); err != nil {
                        rows.Close()
                        return nil, fmt.Errorf("scan star gift message projection: %w", err)
                }
                if msgID > 0 {
                        messageIDs[msgID] = struct{}{}
                }
        }
        if err := rows.Err(); err != nil {
                rows.Close()
                return nil, fmt.Errorf("iterate star gift message projections: %w", err)
        }
        rows.Close()

        ids := make([]int, 0, len(messageIDs))
        for msgID := range messageIDs {
                ids = append(ids, msgID)
        }
        sort.Ints(ids)

        q := sqlcgen.New(tx)
        edits := make([]domain.EditedMessageForUser, 0, len(ids)*2)
        seenPrivateMessages := make(map[string]struct{}, len(ids))
        for _, msgID := range ids {
                var peerType string
                var peerID int64
                err := tx.QueryRow(ctx, `
SELECT peer_type,peer_id FROM message_boxes
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted
FOR UPDATE`, source.Owner.ID, msgID).Scan(&peerType, &peerID)
                if errors.Is(err, pgx.ErrNoRows) {
                        continue
                }
                if err != nil {
                        return nil, fmt.Errorf("lock star gift message projection: %w", err)
                }
                if peerType != string(domain.PeerTypeUser) || peerID <= 0 {
                        return nil, fmt.Errorf("star gift message projection %d is not private", msgID)
                }
                target, err := q.GetMessageBoxForEdit(ctx, sqlcgen.GetMessageBoxForEditParams{
                        OwnerUserID: source.Owner.ID, BoxID: int32(msgID), PeerType: peerType, PeerID: peerID,
                })
                if err != nil {
                        return nil, fmt.Errorf("load star gift message projection: %w", err)
                }
                logicalKey := fmt.Sprintf("%d:%d", target.MessageSenderID, target.PrivateMessageID)
                if _, duplicate := seenPrivateMessages[logicalKey]; duplicate {
                        continue
                }
                seenPrivateMessages[logicalKey] = struct{}{}

                boxes, err := q.ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
                        OwnerUserIds:    privateMessageOwnerIDs(source.Owner.ID, peerID),
                        MessageSenderID: target.MessageSenderID, PrivateMessageID: target.PrivateMessageID,
                })
                if err != nil {
                        return nil, fmt.Errorf("list visible star gift message projections: %w", err)
                }
                var privateMediaJSON []byte
                matched := false
                for _, box := range boxes {
                        media, err := decodeMessageMedia(box.MediaJson)
                        if err != nil {
                                return nil, fmt.Errorf("decode star gift message projection: %w", err)
                        }
                        if media == nil || media.Kind != domain.MessageMediaKindService || media.ServiceAction == nil ||
                                media.ServiceAction.Kind != domain.MessageServiceActionStarGiftUnique || media.ServiceAction.StarGiftUnique == nil ||
                                media.ServiceAction.StarGiftUnique.Gift.ID != current.ID {
                                continue
                        }
                        matched = true
                        action := media.ServiceAction.StarGiftUnique
                        retiredGift := current
                        retiredGift.CraftChancePermille = 0
                        retiredGift.ResellAmount = nil
                        action.Gift = retiredGift
                        action.Peer = domain.Peer{}
                        action.SavedID = 0
                        action.Saved = false
                        if validLifecyclePeer(current.Owner) && current.Owner != source.Owner {
                                action.Transferred = true
                        }
                        action.CanExportAt = 0
                        action.TransferStars = 0
                        action.ResaleAmount = nil
                        action.CanTransferAt = 0
                        action.CanResellAt = 0
                        action.DropOriginalDetailsStars = 0
                        action.CanCraftAt = 0

                        mediaJSON, err := encodeMessageMedia(media)
                        if err != nil {
                                return nil, fmt.Errorf("encode retired star gift projection: %w", err)
                        }
                        pts, err := s.messages.reservePts(ctx, tx, box.OwnerUserID)
                        if err != nil {
                                return nil, fmt.Errorf("allocate retired star gift pts: %w", err)
                        }
                        tag, err := tx.Exec(ctx, `
UPDATE message_boxes SET media=$3,pts=$4
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted`, box.OwnerUserID, box.BoxID, mediaJSON, int32(pts))
                        if err != nil {
                                return nil, fmt.Errorf("update retired star gift projection: %w", err)
                        }
                        if tag.RowsAffected() != 1 {
                                return nil, fmt.Errorf("update retired star gift projection lost row")
                        }
                        msg, err := messageFromVisibleBoxRow(box)
                        if err != nil {
                                return nil, err
                        }
                        msg.Media = media
                        msg.Pts = pts
                        if err := replaceMessageBoxMediaIndexTx(ctx, tx, msg.OwnerUserID, msg.Peer.ID, msg.ID, msg.Date, msg.Media, msg.Entities); err != nil {
                                return nil, err
                        }
                        event := domain.UpdateEvent{UserID: msg.OwnerUserID, Type: domain.UpdateEventEditMessage,
                                Pts: pts, PtsCount: 1, Date: date, Message: msg}
                        if err := appendUserUpdateEvent(ctx, tx, q, msg.OwnerUserID, event); err != nil {
                                return nil, fmt.Errorf("append retired star gift edit event: %w", err)
                        }
                        if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
                                TargetUserID: msg.OwnerUserID, Pts: int32(pts), EventType: string(domain.UpdateEventEditMessage),
                                ExcludeAuthKeyID: 0, ExcludeSessionID: 0,
                        }); err != nil {
                                return nil, fmt.Errorf("enqueue retired star gift edit: %w", err)
                        }
                        if box.OwnerUserID == box.MessageSenderID || len(privateMediaJSON) == 0 {
                                privateMediaJSON, err = encodeSharedPrivateStarGiftMedia(media)
                                if err != nil {
                                        return nil, err
                                }
                        }
                        edits = append(edits, domain.EditedMessageForUser{UserID: msg.OwnerUserID, Message: msg, Event: event})
                }
                if !matched {
                        continue
                }
                if len(privateMediaJSON) == 0 {
                        return nil, fmt.Errorf("retired star gift projection missing shared media")
                }
                if _, err := tx.Exec(ctx, `
UPDATE private_messages SET media=$3
WHERE sender_user_id=$1 AND id=$2`, target.MessageSenderID, target.PrivateMessageID, privateMediaJSON); err != nil {
                        return nil, fmt.Errorf("update retired star gift private media: %w", err)
                }
        }
        return edits, nil
}
