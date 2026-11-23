package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// Раунд 42: переписывание флагов сервисных сообщений о подарке под агрегат.
// Официальные клиенты (tdesktop PremiumGift::subtitle, кнопка
// «показать/скрыть» в заголовке бокса) читают saved/prepaid_upgrade/
// convert_stars прямо из messageActionStarGift / messageActionStarGiftUnique
// в боксе, поэтому каждое изменение состояния агрегата обязано обновлять
// обе копии логического сообщения и доставлять durable edit-событие —
// иначе кнопка в сервисном сообщении «работает», а её состояние и подписи
// не обновляются.

// editStarGiftSavedFlagTx переписывает флаг saved во ВСЕХ живых проекциях
// сервисных сообщений одного агрегата peer_star_gifts: исходное сообщение
// messageActionStarGift, отдельное уведомление о предоплате улучшения
// (upgrade_separate) и messageActionStarGiftUnique для уникальных подарков.
// После payments.saveStarGift обе копии логического сообщения (владелец и
// отправитель) видят актуальное состояние кнопки «показать/скрыть в
// профиле». Каждому видимому боксу выдаётся собственный pts и durable
// edit-event — как при конвертации (editConvertedStarGiftMessagesTx).
func (s *StarGiftLifecycleStore) editStarGiftSavedFlagTx(
	ctx context.Context,
	tx pgx.Tx,
	saved domain.SavedStarGift,
	unsaved bool,
	date int,
	actorUserID int64,
) error {
	if s == nil || s.messages == nil || date <= 0 || saved.ID <= 0 {
		return nil
	}
	if saved.Owner.Type == domain.PeerTypeUser {
		if saved.Owner.ID <= 0 {
			return nil
		}
		return editStarGiftUserMessageFlagsTx(ctx, tx, s.messages, saved.Owner.ID, saved.ID, saved, date,
			func(starGift *domain.MessageStarGiftAction, unique *domain.MessageStarGiftUniqueAction) {
				if starGift != nil {
					starGift.Saved = !unsaved
				}
				if unique != nil {
					unique.Saved = !unsaved
				}
			}, actorUserID)
	}
	if saved.Owner.Type == domain.PeerTypeChannel && saved.Owner.ID > 0 {
		// Канальные подарки: флаг saved живёт в «Telegram»-уведомлениях
		// владельцу/админам, зарегистрированных в star_gift_user_message_refs.
		viewers, err := starGiftRefViewers(ctx, tx, saved.ID)
		if err != nil {
			return err
		}
		for _, viewer := range viewers {
			if err := editStarGiftUserMessageFlagsTx(ctx, tx, s.messages, viewer.userID, saved.ID, saved, date,
				func(starGift *domain.MessageStarGiftAction, unique *domain.MessageStarGiftUniqueAction) {
					if starGift != nil {
						starGift.Saved = !unsaved
					}
					if unique != nil {
						unique.Saved = !unsaved
					}
				}, actorUserID); err != nil {
				return err
			}
		}
	}
	return nil
}

type starGiftRefViewer struct {
	userID int64
	msgID  int
}

// starGiftRefViewers перечисляет (viewer, msg_id) пары всех зарегистрированных
// проекций агрегата — исходное сообщение + отдельные уведомления.
func starGiftRefViewers(ctx context.Context, tx pgx.Tx, savedGiftID int64) ([]starGiftRefViewer, error) {
	rows, err := tx.Query(ctx, `
SELECT owner_user_id, msg_id FROM star_gift_user_message_refs
WHERE saved_gift_id=$1 ORDER BY owner_user_id, msg_id`, savedGiftID)
	if err != nil {
		return nil, fmt.Errorf("list star gift message refs: %w", err)
	}
	defer rows.Close()
	out := make([]starGiftRefViewer, 0, 4)
	for rows.Next() {
		var viewer starGiftRefViewer
		if err := rows.Scan(&viewer.userID, &viewer.msgID); err != nil {
			return nil, fmt.Errorf("scan star gift message ref: %w", err)
		}
		out = append(out, viewer)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate star gift message refs: %w", err)
	}
	return out, nil
}

// starGiftActionEditor точечно правит флаги одного действия сервисного
// сообщения. В функцию попадают уже декодированные действие обычного подарка
// и/или уникального; nil означает, что данная проекция другого вида.
type starGiftActionEditor func(starGift *domain.MessageStarGiftAction, unique *domain.MessageStarGiftUniqueAction)

// editStarGiftUserMessageFlagsTx переписывает ОБЕ копии (box'а) каждого
// логического приватного сообщения, зарегистрированного за агрегатом
// (owner_user_id + msg_id из star_gift_user_message_refs), применяя editor к
// действию. Каждый переписанный бокс получает собственный pts, durable
// edit_message event и строку outbox-диспетча.
func editStarGiftUserMessageFlagsTx(
	ctx context.Context,
	tx pgx.Tx,
	messages *MessageStore,
	viewerUserID int64,
	savedGiftID int64,
	saved domain.SavedStarGift,
	date int,
	edit starGiftActionEditor,
	actorUserID int64,
) error {
	if messages == nil || viewerUserID <= 0 || savedGiftID <= 0 || date <= 0 {
		return nil
	}
	refs, err := starGiftRefViewersForOwner(ctx, tx, viewerUserID, savedGiftID)
	if err != nil {
		return err
	}
	if saved.MsgID > 0 && !containsStarGiftRefMsg(refs, saved.MsgID) {
		refs = append(refs, starGiftRefViewer{userID: viewerUserID, msgID: saved.MsgID})
	}
	q := sqlcgen.New(tx)
	seenPrivate := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		var peerType string
		var peerID int64
		err := tx.QueryRow(ctx, `
SELECT peer_type,peer_id FROM message_boxes
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted
FOR UPDATE`, viewerUserID, ref.msgID).Scan(&peerType, &peerID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("lock star gift message box %d: %w", ref.msgID, err)
		}
		if peerType != string(domain.PeerTypeUser) || peerID <= 0 {
			continue
		}
		target, err := q.GetMessageBoxForEdit(ctx, sqlcgen.GetMessageBoxForEditParams{
			OwnerUserID: viewerUserID, BoxID: int32(ref.msgID), PeerType: peerType, PeerID: peerID,
		})
		if err != nil {
			return fmt.Errorf("load star gift message box %d: %w", ref.msgID, err)
		}
		logicalKey := fmt.Sprintf("%d:%d", target.MessageSenderID, target.PrivateMessageID)
		if _, done := seenPrivate[logicalKey]; done {
			continue
		}
		seenPrivate[logicalKey] = struct{}{}
		boxes, err := q.ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
			OwnerUserIds:    privateMessageOwnerIDs(viewerUserID, peerID),
			MessageSenderID: target.MessageSenderID, PrivateMessageID: target.PrivateMessageID,
		})
		if err != nil {
			return fmt.Errorf("list star gift message %d boxes: %w", ref.msgID, err)
		}
		var privateMediaJSON []byte
		for _, box := range boxes {
			media, err := decodeMessageMedia(box.MediaJson)
			if err != nil {
				return fmt.Errorf("decode star gift message media: %w", err)
			}
			starGift := privateStarGiftAction(media)
			unique := privateStarGiftUniqueAction(media)
			if starGift == nil && unique == nil {
				// Чужое действие в том же логическом сообщении — не наша проекция.
				continue
			}
			if starGift != nil && saved.ID > 0 && starGift.GiftID != saved.GiftID {
				continue
			}
			edit(starGift, unique)
			mediaJSON, err := encodeMessageMedia(media)
			if err != nil {
				return fmt.Errorf("encode star gift message media: %w", err)
			}
			pts, err := messages.reservePts(ctx, tx, box.OwnerUserID)
			if err != nil {
				return fmt.Errorf("allocate star gift edit pts: %w", err)
			}
			tag, err := tx.Exec(ctx, `
UPDATE message_boxes SET media=$3,pts=$4
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted`, box.OwnerUserID, box.BoxID, mediaJSON, int32(pts))
			if err != nil {
				return fmt.Errorf("update star gift message box: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("update star gift message box lost row")
			}
			msg, err := messageFromVisibleBoxRow(box)
			if err != nil {
				return err
			}
			msg.Media = media
			msg.Pts = pts
			if err := replaceMessageBoxMediaIndexTx(ctx, tx, msg.OwnerUserID, msg.Peer.ID, msg.ID, msg.Date, msg.Media, msg.Entities); err != nil {
				return err
			}
			event := domain.UpdateEvent{UserID: msg.OwnerUserID, Type: domain.UpdateEventEditMessage,
				Pts: pts, PtsCount: 1, Date: date, Message: msg}
			if err := appendUserUpdateEvent(ctx, tx, q, msg.OwnerUserID, event); err != nil {
				return fmt.Errorf("append star gift edit event: %w", err)
			}
			if err := enqueueDispatch(ctx, q, sqlcgen.EnqueueDispatchParams{
				TargetUserID: msg.OwnerUserID, Pts: int32(pts), EventType: string(domain.UpdateEventEditMessage),
			}); err != nil {
				return fmt.Errorf("enqueue star gift edit: %w", err)
			}
			if box.OwnerUserID == box.MessageSenderID || len(privateMediaJSON) == 0 {
				privateMediaJSON, err = encodeSharedPrivateStarGiftMedia(media)
				if err != nil {
					return err
				}
			}
		}
		if len(privateMediaJSON) == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
UPDATE private_messages SET media=$3
WHERE sender_user_id=$1 AND id=$2`, target.MessageSenderID, target.PrivateMessageID, privateMediaJSON); err != nil {
			return fmt.Errorf("update star gift private media: %w", err)
		}
	}
	return nil
}

func starGiftRefViewersForOwner(ctx context.Context, tx pgx.Tx, ownerUserID, savedGiftID int64) ([]starGiftRefViewer, error) {
	rows, err := tx.Query(ctx, `
SELECT msg_id FROM star_gift_user_message_refs
WHERE owner_user_id=$1 AND saved_gift_id=$2 ORDER BY msg_id`, ownerUserID, savedGiftID)
	if err != nil {
		return nil, fmt.Errorf("list owner star gift message refs: %w", err)
	}
	defer rows.Close()
	out := make([]starGiftRefViewer, 0, 2)
	for rows.Next() {
		var msgID int
		if err := rows.Scan(&msgID); err != nil {
			return nil, fmt.Errorf("scan owner star gift message ref: %w", err)
		}
		out = append(out, starGiftRefViewer{userID: ownerUserID, msgID: msgID})
	}
	return out, rows.Err()
}

func containsStarGiftRefMsg(refs []starGiftRefViewer, msgID int) bool {
	for _, ref := range refs {
		if ref.msgID == msgID {
			return true
		}
	}
	return false
}

// ToggleStarGiftSaved — агрегат payments.saveStarGift: состояние unsaved
// peer_star_gifts (с переносом pinned_order при скрытии) и флаг saved во всех
// сервисных сообщениях агрегата обновляются одной транзакцией. Официальные
// клиенты перерисовывают кнопки «показать/скрыть в профиле» из edit-события.
func (s *StarGiftLifecycleStore) ToggleStarGiftSaved(ctx context.Context, req domain.StarGiftSavedToggleRequest) (domain.SavedStarGift, bool, error) {
	if s == nil || s.db == nil || s.messages == nil || req.ActorUserID <= 0 || !req.Ref.Valid() || req.Date <= 0 {
		return domain.SavedStarGift{}, false, domain.ErrStarGiftNotFound
	}
	if req.Ref.Owner.Type == domain.PeerTypeUser && req.Ref.Owner.ID != req.ActorUserID {
		return domain.SavedStarGift{}, false, domain.ErrStarGiftOwnerInvalid
	}
	if !validLifecyclePeer(req.Ref.Owner) {
		return domain.SavedStarGift{}, false, domain.ErrStarGiftOwnerInvalid
	}
	var saved domain.SavedStarGift
	changed := false
	err := withTx(ctx, s.db, "toggle star gift saved", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(req.Ref.Owner)); err != nil {
			return err
		}
		where, args := savedStarGiftRefWhere(req.Ref)
		var savedID int64
		var pinnedOrder int
		err := tx.QueryRow(ctx, `SELECT id,pinned_order FROM peer_star_gifts WHERE `+where+` AND lifecycle_status='active' FOR UPDATE`, args...).Scan(&savedID, &pinnedOrder)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET unsaved=$2,pinned_order=CASE WHEN $2 THEN 0 ELSE pinned_order END WHERE id=$1`, savedID, req.Unsaved); err != nil {
			return err
		}
		if req.Unsaved && pinnedOrder > 0 {
			// The positive-order unique index is immediate. Move the bounded
			// vector one vacant slot at a time so no transient duplicate order
			// can be observed by PostgreSQL.
			for order := pinnedOrder + 1; order <= domain.MaxPinnedStarGifts; order++ {
				if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET pinned_order=$4
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND pinned_order=$3`, string(req.Ref.Owner.Type), req.Ref.Owner.ID, order, order-1); err != nil {
					return err
				}
			}
		}
		locked, err := lockSavedStarGiftByID(ctx, tx, savedID)
		if err != nil {
			return err
		}
		if err := s.editStarGiftSavedFlagTx(ctx, tx, locked, req.Unsaved, req.Date, req.ActorUserID); err != nil {
			return err
		}
		locked.Unsaved = req.Unsaved
		if req.Unsaved {
			locked.PinnedOrder = 0
		} else {
			locked.PinnedOrder = pinnedOrder
		}
		saved = locked
		changed = true
		return nil
	})
	if err != nil {
		return domain.SavedStarGift{}, false, err
	}
	return saved, changed, nil
}

// markStarGiftSourcePrepaidUpgradeTx переписывает ИСХОДНОЕ сервисное сообщение
// о подарке после отдельной предоплаты улучшения другим пользователем
// (payments.sendStarsForm с InputInvoiceStarGiftPrepaidUpgrade):
//
//   - convert_stars убирается — конвертация предоплаченного подарка запрещена
//     (ConvertStarGift → STARGIFT_INVALID), клиент не должен показывать кнопку
//     «Обменять на N звёзд» (официальный дамп 183490);
//   - prepaid_upgrade=true + upgrade_stars=сумма предоплаты — подпись
//     «{user} может сделать этот подарок уникальным коллекционным»
//     (tdesktop: lng_action_gift_sent_upgradable по upgrade_stars) и кнопка
//     бесплатного улучшения (Gift2UpgradeButtonFree) у владельца.
//
// can_upgrade сохраняется на обеих копиях: официальные серверы отдают его
// отправителю тоже, а действие апгрейда остаётся доступным только владельцу.
// Правятся ВСЕ копии (владелец + отправитель), каждой выдаётся свой pts и
// durable edit-событие.
func (s *StarGiftLifecycleStore) markStarGiftSourcePrepaidUpgradeTx(
	ctx context.Context,
	tx pgx.Tx,
	req domain.StarGiftPrepaidUpgradeRequest,
	saved domain.SavedStarGift,
	price int64,
) error {
	if s == nil || s.messages == nil || req.Date <= 0 || saved.ID <= 0 ||
		saved.Owner.Type != domain.PeerTypeUser || saved.Owner.ID <= 0 || saved.MsgID <= 0 {
		return nil
	}
	return editStarGiftUserMessageFlagsTx(ctx, tx, s.messages, saved.Owner.ID, saved.ID, saved, req.Date,
		func(starGift *domain.MessageStarGiftAction, _ *domain.MessageStarGiftUniqueAction) {
			if starGift == nil {
				return
			}
			starGift.PrepaidUpgrade = true
			starGift.UpgradeStars = price
			starGift.ConvertStars = 0
			starGift.PrepaidUpgradeHash = ""
		}, req.PayerUserID)
}
