package postgres

// Раунд 48 (показ/скрытие из сервисного сообщения): payments.saveStarGift
// (ToggleStarGiftSaved) снова переписывает флаг saved в сервисных сообщениях о
// подарке (обе копии логического сообщения + канальные уведомления) и
// доставляет durable edit-события. Диагностика раунда 48: Android-клиент
// (StarGiftSheet) для регулярных подарков читает saved прямо из
// messageActionStarGift и не перепрашивает состояние — при замороженном флаге
// (раунд 47) переключение из сервисного сообщения отправляло unsave=false
// всегда, и скрыть видимый в профиле подарок из чата было невозможно.
// Подпись блока при saved=true задаётся langpack-строкой Gift2ActionSavedInfo,
// приведённой к тексту Gift2ActionBotSavedInfo («Сейчас подарок виден в Вашем
// профиле»), поэтому раунд 46 (корректная строка состояния) не регрессирует.

import (
        "context"
        "testing"
        "time"

        "github.com/jackc/pgx/v5/pgxpool"

        "telesrv/internal/domain"
)

func toggleRound48RecipientGiftAction(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64, msgID int) *domain.MessageStarGiftAction {
        t.Helper()
        var mediaJSON string
        if err := pool.QueryRow(ctx, `SELECT p.media::text FROM private_messages p
JOIN message_boxes b ON b.message_sender_id=p.sender_user_id AND b.private_message_id=p.id
WHERE b.owner_user_id=$1 AND b.box_id=$2`, userID, msgID).Scan(&mediaJSON); err != nil {
                t.Fatalf("load recipient gift media (user %d msg %d): %v", userID, msgID, err)
        }
        media, err := decodeMessageMedia(mediaJSON)
        if err != nil || media == nil || media.ServiceAction == nil || media.ServiceAction.Kind != domain.MessageServiceActionStarGift ||
                media.ServiceAction.StarGift == nil {
                t.Fatalf("recipient message %d is not a star gift service message: media=%+v err=%v", msgID, media, err)
        }
        return media.ServiceAction.StarGift
}

func TestStarGiftToggleSavedRewritesMessageSavedFlagPostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        now := int(time.Now().Unix())

        users := NewUserStore(pool)
        sender := createTestUser(t, ctx, users, "+1891"+suffix+"01", "ToggleSender", "")
        recipient := createTestUser(t, ctx, users, "+1891"+suffix+"02", "ToggleRecipient", "")
        if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, sender.ID, 10000, now); err != nil {
                t.Fatalf("grant sender stars: %v", err)
        }

        gifts := NewStarGiftStore(pool)
        baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 800
        entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                Title: "Toggle Gift " + suffix, Stars: 100, ConvertStars: 50, Enabled: true, Limited: true,
                AvailabilityTotal: 10, AvailabilityRemains: 10,
                Document: collectibleTestDocument(baseDocumentID, "toggle-gift.tgs"), Blob: collectibleTestBlob(baseDocumentID, "toggle-gift"),
                Animation: collectibleTestAnimation("toggle-gift.tgs"), Actor: "integration", CommandID: "toggle-gift-" + suffix,
        })
        if err != nil {
                t.Fatalf("create catalog: %v", err)
        }

        messages := NewMessageStore(pool)
        lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)

        // Покупка: человек -> человек, обычный (не канальный) подарок.
        req := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
                BuyerUserID: sender.ID,
                To:          domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
                GiftID:      entry.Gift.ID,
                CommandKey:  "toggle-round48-" + suffix,
                Date:        now + 1,
        })
        purchased, err := lifecycle.PurchaseStarGift(ctx, req)
        if err != nil {
                t.Fatalf("purchase: %v", err)
        }
        if purchased.Saved.ID <= 0 || purchased.Saved.MsgID <= 0 || purchased.Send.RecipientMessage.ID == 0 {
                t.Fatalf("purchase aggregate malformed: saved=%+v send=%+v", purchased.Saved, purchased.Send)
        }
        msgID := purchased.Saved.MsgID
        if purchased.Send.RecipientMessage.ID != msgID {
                msgID = purchased.Send.RecipientMessage.ID
        }

        // Свежий подарок: saved=false (раунд 5) — клиент рисует подсказку
        // «добавить в профиль или конвертировать».
        fresh := toggleRound48RecipientGiftAction(t, ctx, pool, recipient.ID, msgID)
        if fresh.Saved {
                t.Fatalf("fresh gift action saved = true, want false")
        }

        // 1) Ручное добавление в профиль (payments.saveStarGift unsave=false):
        //    агрегат переходит в unsaved=false, saved в сообщении получателя
        //    становится true (кнопка «скрыть» в шторке из сервисного сообщения).
        shown, changed, err := lifecycle.ToggleStarGiftSaved(ctx, domain.StarGiftSavedToggleRequest{
                ActorUserID: recipient.ID,
                Ref:         domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}, MsgID: msgID},
                Unsaved:     false,
                Date:        now + 5,
        })
        if err != nil || !changed {
                t.Fatalf("toggle show: changed=%v err=%v", changed, err)
        }
        if shown.Unsaved {
                t.Fatalf("after show aggregate unsaved = true, want false")
        }
        afterShow := toggleRound48RecipientGiftAction(t, ctx, pool, recipient.ID, msgID)
        if !afterShow.Saved {
                t.Fatalf("after SHOW message saved = false, want true (раунд 48: шторка из сервисного сообщения)")
        }

        // 2) Скрытие (unsave=true): saved в сообщении снова false.
        hidden, changed, err := lifecycle.ToggleStarGiftSaved(ctx, domain.StarGiftSavedToggleRequest{
                ActorUserID: recipient.ID,
                Ref:         domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}, MsgID: msgID},
                Unsaved:     true,
                Date:        now + 6,
        })
        if err != nil || !changed {
                t.Fatalf("toggle hide: changed=%v err=%v", changed, err)
        }
        if !hidden.Unsaved || hidden.PinnedOrder != 0 {
                t.Fatalf("after hide aggregate = %+v, want unsaved=true pinned=0", hidden)
        }
        afterHide := toggleRound48RecipientGiftAction(t, ctx, pool, recipient.ID, msgID)
        if afterHide.Saved {
                t.Fatalf("after HIDE message saved = true, want false")
        }

        // 3) Повторное добавление и проверка копии ОТПРАВИТЕЛЯ: обе копии
        // логического сообщения получают одинаковый флаг.
        if _, _, err := lifecycle.ToggleStarGiftSaved(ctx, domain.StarGiftSavedToggleRequest{
                ActorUserID: recipient.ID,
                Ref:         domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}, MsgID: msgID},
                Unsaved:     false,
                Date:        now + 7,
        }); err != nil {
                t.Fatalf("toggle show again: %v", err)
        }
        senderAction := toggleRound48RecipientGiftAction(t, ctx, pool, sender.ID, msgID)
        if senderAction == nil {
                t.Fatalf("sender copy missing")
        }
        if !senderAction.Saved {
                t.Fatalf("sender copy saved = false, want true (обе копии переписываются)")
        }
}
