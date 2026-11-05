package postgres

import (
        "context"
        "crypto/sha256"
        "encoding/json"
        "fmt"
        "strings"

        "github.com/jackc/pgx/v5"

        "telesrv/internal/domain"
        "telesrv/internal/store/postgres/sqlcgen"
)

const (
        maxChannelStarGiftNotificationRecipients = 256
        channelStarGiftNotificationLeaseSeconds  = 60
)

// Виды канальных уведомлений (раунд 23): один и тот же агрегат peer_star_gifts
// порождает несколько независимых событий — покупку/выигрыш аукциона
// ('star_gift'), передачу уникального подарка на канал ('star_gift_unique') и
// улучшение канального подарка ('star_gift_unique_upgrade'). Виды входят в
// первичный ключ, поэтому события не затирают друг друга.
const (
        channelStarGiftNotificationKindGift         = "star_gift"
        channelStarGiftNotificationKindUnique       = "star_gift_unique"
        channelStarGiftNotificationKindUniqueUpgrad = "star_gift_unique_upgrade"
)

type channelStarGiftNotificationJob struct {
        SavedGiftID  int64
        TargetUserID int64
        GiftDate     int
        ActionKind   string
        Action       domain.MessageStarGiftAction
        UniqueAction domain.MessageStarGiftUniqueAction
        Attempts     int
}

func normalizeChannelStarGiftNotificationKind(kind string) (string, error) {
        switch kind {
        case "", channelStarGiftNotificationKindGift:
                return channelStarGiftNotificationKindGift, nil
        case channelStarGiftNotificationKindUnique, channelStarGiftNotificationKindUniqueUpgrad:
                return kind, nil
        default:
                return "", fmt.Errorf("unknown channel star gift notification kind %q", kind)
        }
}

func enqueueChannelStarGiftNotifications(
        ctx context.Context,
        tx pgx.Tx,
        savedGiftID int64,
        channelID int64,
        giftDate int,
        action *domain.MessageStarGiftAction,
        excludeUserID int64,
) error {
        if action == nil {
                return fmt.Errorf("enqueue channel star gift notifications: invalid intent")
        }
        return enqueueChannelStarGiftNotificationIntent(ctx, tx, savedGiftID, channelID, giftDate,
                channelStarGiftNotificationKindGift, action, nil, excludeUserID)
}

// enqueueChannelStarGiftUniqueNotifications ставит уведомление о действии с
// уникальным подарком (передача на канал или улучшение) в очередь рассылки
// владельцу/админам канала (раунд 23, официальные дампы 198247/198254).
func enqueueChannelStarGiftUniqueNotifications(
        ctx context.Context,
        tx pgx.Tx,
        savedGiftID int64,
        channelID int64,
        giftDate int,
        kind string,
        action *domain.MessageStarGiftUniqueAction,
        excludeUserID int64,
) error {
        if action == nil {
                return fmt.Errorf("enqueue channel star gift unique notifications: invalid intent")
        }
        kind, err := normalizeChannelStarGiftNotificationKind(kind)
        if err != nil || kind == channelStarGiftNotificationKindGift {
                return fmt.Errorf("enqueue channel star gift unique notifications: invalid kind")
        }
        return enqueueChannelStarGiftNotificationIntent(ctx, tx, savedGiftID, channelID, giftDate,
                kind, nil, action, excludeUserID)
}

func enqueueChannelStarGiftNotificationIntent(
        ctx context.Context,
        tx pgx.Tx,
        savedGiftID int64,
        channelID int64,
        giftDate int,
        kind string,
        action *domain.MessageStarGiftAction,
        uniqueAction *domain.MessageStarGiftUniqueAction,
        excludeUserID int64,
) error {
        if savedGiftID <= 0 || channelID <= 0 || giftDate <= 0 || (action == nil) == (uniqueAction == nil) {
                return fmt.Errorf("enqueue channel star gift notifications: invalid intent")
        }
        if action != nil && (action.PeerChannelID != channelID || action.SavedID <= 0) {
                return fmt.Errorf("enqueue channel star gift notifications: invalid intent")
        }
        if uniqueAction != nil && (uniqueAction.Peer.Type != domain.PeerTypeChannel ||
                uniqueAction.Peer.ID != channelID || uniqueAction.SavedID <= 0) {
                return fmt.Errorf("enqueue channel star gift notifications: invalid intent")
        }
        var payload any
        if action != nil {
                payload = action
        } else {
                payload = uniqueAction
        }
        actionJSON, err := json.Marshal(payload)
        if err != nil {
                return fmt.Errorf("encode channel star gift notification: %w", err)
        }
        _, err = tx.Exec(ctx, `
WITH candidates AS (
    SELECT creator_user_id AS user_id
    FROM channels
    WHERE id=$2 AND NOT deleted
    UNION
    SELECT user_id
    FROM channel_members
    WHERE channel_id=$2 AND status='active'
      AND (role='creator' OR (
          role='admin'
          AND COALESCE((admin_rights->>'PostMessages')::boolean,false)
      ))
), bounded AS (
    SELECT user_id FROM candidates
    WHERE user_id>0 AND ($7::bigint=0 OR user_id<>$7::bigint)
    ORDER BY user_id
    LIMIT $5
)
INSERT INTO star_gift_channel_notification_jobs
    (saved_gift_id,target_user_id,gift_date,action_kind,action,next_attempt_at)
SELECT $1,bounded.user_id,$3,$6::text,$4::jsonb,$3
FROM bounded
LEFT JOIN star_gift_notification_settings settings
  ON settings.user_id=bounded.user_id AND settings.channel_id=$2
WHERE COALESCE(settings.enabled,TRUE)
ON CONFLICT(saved_gift_id,target_user_id,action_kind) DO NOTHING`,
                savedGiftID, channelID, giftDate, string(actionJSON), maxChannelStarGiftNotificationRecipients, kind, excludeUserID)
        if err != nil {
                return fmt.Errorf("enqueue channel star gift notifications: %w", err)
        }
        return nil
}

func (s *StarGiftLifecycleStore) dispatchChannelStarGiftNotifications(
        ctx context.Context,
        now int,
        limit int,
        savedGiftID int64,
) (int, error) {
        if s == nil {
                return 0, domain.ErrStarGiftUnavailable
        }
        return dispatchChannelStarGiftNotificationsStore(ctx, s.db, s.messages, now, limit, savedGiftID)
}

// dispatchChannelStarGiftNotificationsStore — общая реализация рассылки: ею
// пользуется и lifecycle store (покупки/аукционы/передачи), и upgrade store
// (улучшения подарков канала).
func dispatchChannelStarGiftNotificationsStore(
        ctx context.Context,
        db sqlcgen.DBTX,
        messages *MessageStore,
        now int,
        limit int,
        savedGiftID int64,
) (int, error) {
        if db == nil || messages == nil || now <= 0 || limit <= 0 {
                return 0, domain.ErrStarGiftUnavailable
        }
        if limit > maxChannelStarGiftNotificationRecipients {
                limit = maxChannelStarGiftNotificationRecipients
        }
        jobs, err := claimChannelStarGiftNotificationJobs(ctx, db, now, limit, savedGiftID)
        if err != nil {
                return 0, err
        }
        var firstErr error
        delivered := 0
        for _, job := range jobs {
                messageID, sendErr := deliverChannelStarGiftNotification(ctx, messages, db, job)
                if sendErr == nil {
                        tag, markErr := db.Exec(ctx, `UPDATE star_gift_channel_notification_jobs
SET delivered_at=$3,message_id=$4,lease_until=0,last_error='',updated_at=now()
WHERE saved_gift_id=$1 AND target_user_id=$2 AND action_kind=$5 AND delivered_at=0`,
                                job.SavedGiftID, job.TargetUserID, now, messageID, job.ActionKind)
                        if markErr == nil && tag.RowsAffected() == 1 {
                                delivered++
                                continue
                        }
                        if markErr == nil {
                                markErr = fmt.Errorf("channel star gift notification job disappeared")
                        }
                        sendErr = markErr
                }
                if firstErr == nil {
                        firstErr = sendErr
                }
                retryAt := now + channelStarGiftNotificationRetrySeconds(job.Attempts)
                _, _ = db.Exec(ctx, `UPDATE star_gift_channel_notification_jobs
SET next_attempt_at=$3,lease_until=0,last_error=$4,updated_at=now()
WHERE saved_gift_id=$1 AND target_user_id=$2 AND action_kind=$5 AND delivered_at=0`,
                        job.SavedGiftID, job.TargetUserID, retryAt, truncateStarGiftNotificationError(sendErr), job.ActionKind)
        }
        return delivered, firstErr
}

func claimChannelStarGiftNotificationJobs(
        ctx context.Context,
        db sqlcgen.DBTX,
        now int,
        limit int,
        savedGiftID int64,
) ([]channelStarGiftNotificationJob, error) {
        jobs := make([]channelStarGiftNotificationJob, 0, limit)
        err := withTx(ctx, db, "claim channel star gift notifications", func(tx pgx.Tx) error {
                rows, err := tx.Query(ctx, `
WITH picked AS (
    SELECT saved_gift_id,target_user_id,action_kind
    FROM star_gift_channel_notification_jobs
    WHERE delivered_at=0 AND next_attempt_at<=$1 AND lease_until<$1
      AND ($3::bigint=0 OR saved_gift_id=$3)
    ORDER BY next_attempt_at,saved_gift_id,target_user_id
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)
UPDATE star_gift_channel_notification_jobs job
SET attempts=job.attempts+1,
    lease_until=$1+$4,
    updated_at=now()
FROM picked
WHERE job.saved_gift_id=picked.saved_gift_id
  AND job.target_user_id=picked.target_user_id
  AND job.action_kind=picked.action_kind
RETURNING job.saved_gift_id,job.target_user_id,job.gift_date,job.action_kind,job.action,job.attempts`,
                        now, limit, savedGiftID, channelStarGiftNotificationLeaseSeconds)
                if err != nil {
                        return err
                }
                defer rows.Close()
                for rows.Next() {
                        var job channelStarGiftNotificationJob
                        var kind string
                        var actionJSON []byte
                        if err := rows.Scan(&job.SavedGiftID, &job.TargetUserID, &job.GiftDate, &kind, &actionJSON, &job.Attempts); err != nil {
                                return err
                        }
                        kind, err := normalizeChannelStarGiftNotificationKind(kind)
                        if err != nil {
                                return err
                        }
                        job.ActionKind = kind
                        switch kind {
                        case channelStarGiftNotificationKindGift:
                                if err := json.Unmarshal(actionJSON, &job.Action); err != nil {
                                        return fmt.Errorf("decode channel star gift notification: %w", err)
                                }
                                if job.Action.PeerChannelID <= 0 || job.Action.SavedID <= 0 {
                                        return fmt.Errorf("decode channel star gift notification: invalid intent")
                                }
                        default:
                                if err := json.Unmarshal(actionJSON, &job.UniqueAction); err != nil {
                                        return fmt.Errorf("decode channel star gift notification: %w", err)
                                }
                                if job.UniqueAction.Peer.Type != domain.PeerTypeChannel || job.UniqueAction.Peer.ID <= 0 ||
                                        job.UniqueAction.SavedID <= 0 {
                                        return fmt.Errorf("decode channel star gift notification: invalid intent")
                                }
                        }
                        if job.SavedGiftID <= 0 || job.TargetUserID <= 0 || job.GiftDate <= 0 {
                                return fmt.Errorf("decode channel star gift notification: invalid intent")
                        }
                        jobs = append(jobs, job)
                }
                return rows.Err()
        })
        return jobs, err
}

func deliverChannelStarGiftNotification(
        ctx context.Context,
        messages *MessageStore,
        db sqlcgen.DBTX,
        job channelStarGiftNotificationJob,
) (int, error) {
        fingerprint := sha256.Sum256([]byte(fmt.Sprintf(
                "telesrv:channel-star-gift-notification:v2:%s:%d:%d",
                job.ActionKind, job.SavedGiftID, job.TargetUserID,
        )))
        media := &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{}}
        switch job.ActionKind {
        case channelStarGiftNotificationKindUnique:
                unique := job.UniqueAction
                media.ServiceAction.Kind = domain.MessageServiceActionStarGiftUnique
                media.ServiceAction.StarGiftUnique = &unique
        case channelStarGiftNotificationKindUniqueUpgrad:
                unique := job.UniqueAction
                media.ServiceAction.Kind = domain.MessageServiceActionStarGiftUnique
                media.ServiceAction.StarGiftUnique = &unique
        default:
                action := job.Action
                media.ServiceAction.Kind = domain.MessageServiceActionStarGift
                media.ServiceAction.StarGift = &action
        }
        request := domain.SendPrivateTextRequest{
                SenderUserID:           domain.OfficialSystemUserID,
                RecipientUserID:        job.TargetUserID,
                RandomID:               lifecycleCommandRandomID("channel-star-gift-notification", job.ActionKind, job.SavedGiftID, job.TargetUserID),
                Date:                   job.GiftDate,
                IdempotencyFingerprint: fingerprint[:],
                Media:                  media,
        }
        sent, err := messages.sendPrivateTextWithHooks(ctx, request, privateSendTxHooks{
                after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
                        if sent.RecipientMessage.ID <= 0 {
                                return fmt.Errorf("channel star gift notification missing recipient box")
                        }
                        return registerChannelNotificationMessageRef(ctx, tx, job.TargetUserID,
                                sent.RecipientMessage.ID, job.SavedGiftID)
                },
        })
        if err != nil {
                return 0, err
        }
        if sent.RecipientMessage.ID <= 0 {
                return 0, fmt.Errorf("channel star gift notification replay missing recipient box")
        }
        return sent.RecipientMessage.ID, nil
}

func channelStarGiftNotificationRetrySeconds(attempt int) int {
        if attempt < 1 {
                attempt = 1
        }
        delay := attempt * attempt * 5
        if delay > 3600 {
                return 3600
        }
        return delay
}

func truncateStarGiftNotificationError(err error) string {
        if err == nil {
                return ""
        }
        value := strings.TrimSpace(err.Error())
        runes := []rune(value)
        if len(runes) > 1000 {
                value = string(runes[:1000])
        }
        return value
}
