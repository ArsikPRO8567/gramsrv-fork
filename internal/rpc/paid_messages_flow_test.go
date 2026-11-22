package rpc

import (
        "context"
        "strings"
        "testing"

        "github.com/iamxvbaba/td/clock"
        "github.com/iamxvbaba/td/tg"
        "go.uber.org/zap/zaptest"

        appaccount "telesrv/internal/app/account"
        appmessages "telesrv/internal/app/messages"
        appstars "telesrv/internal/app/stars"
        appprivacy "telesrv/internal/app/privacy"
        appusers "telesrv/internal/app/users"
        appcontacts "telesrv/internal/app/contacts"
        appdialogs "telesrv/internal/app/dialogs"
        appupdates "telesrv/internal/app/updates"
        "telesrv/internal/domain"
        "telesrv/internal/store/memory"
)

// newPaidMessagesFlowRouter собирает роутер на реальных memory-store (сообщения +
// звёздный ledger связаны AttachStarsStore) для сквозного сценария платных сообщений.
func newPaidMessagesFlowRouter(t *testing.T) (*Router, *memory.StarsStore, domain.User, domain.User) {
        t.Helper()
        ctx := context.Background()
        usersStore := memory.NewUserStore()
        alice, err := usersStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550009001", FirstName: "Alice"})
        if err != nil {
                t.Fatalf("create Alice: %v", err)
        }
        bob, err := usersStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550009002", FirstName: "Bob"})
        if err != nil {
                t.Fatalf("create Bob: %v", err)
        }
        contactStore := memory.NewContactStore()
        users := appusers.NewService(usersStore)
        contacts := appcontacts.NewService(contactStore)
        privacy := appprivacy.NewService(memory.NewPrivacyStore(), contactStore).ConfigureReadModels(users, nil)
        settingsStore := memory.NewPasswordStore()
        account := appaccount.NewService(settingsStore, appaccount.WithAccountSettings(settingsStore))

        dialogStore := memory.NewDialogStore()
        messageStore := memory.NewMessageStore(dialogStore)
        starsStore := memory.NewStarsStore()
        messageStore.AttachStarsStore(starsStore)

        router := New(Config{}, Deps{
                Account:  account,
                Privacy:  privacy,
                Users:    users,
                Contacts: contacts,
                Dialogs:  appdialogs.NewService(dialogStore),
                Messages: appmessages.NewService(messageStore, dialogStore),
                Stars:    appstars.NewService(starsStore),
                Updates:  appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
        }, zaptest.NewLogger(t), clock.System)
        return router, starsStore, alice, bob
}

func paidFlowBalance(t *testing.T, st *memory.StarsStore, userID int64) int64 {
        t.Helper()
        bal, err := st.GetBalance(context.Background(), userID)
        if err != nil {
                t.Fatalf("balance %d: %v", userID, err)
        }
        return bal.Balance
}

func paidFlowSend(t *testing.T, r *Router, ctx context.Context, sender domain.User, recipient domain.User, randomID, allow int64) error {
        t.Helper()
        req := &tg.MessagesSendMessageRequest{
                Peer:     &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
                Message:  "hi",
                RandomID: randomID,
        }
        if allow != 0 {
                req.SetAllowPaidStars(allow)
        }
        _, err := r.onMessagesSendMessage(WithUserID(ctx, sender.ID), req)
        return err
}

// TestPaidMessagesChargeFlow — полный протокол: плата за сообщение, недостаток
// средств, «получатель платы пишет первым», возврат через toggleNoPaidMessagesException.
func TestPaidMessagesChargeFlow(t *testing.T) {
        ctx := context.Background()
        r, stars, alice, bob := newPaidMessagesFlowRouter(t)

        // Bob включил плату 10 звёзд за сообщение от не-контактов.
        account := r.deps.Account.(interface {
                SetGlobalPrivacy(ctx context.Context, userID int64, privacy domain.GlobalPrivacy) (domain.AccountSettings, error)
        })
        if _, err := account.SetGlobalPrivacy(ctx, bob.ID, domain.GlobalPrivacy{NoncontactPeersPaidStars: 10}); err != nil {
                t.Fatalf("set fee: %v", err)
        }

        // Стартовый баланс по умолчанию выдаётся лениво при списании — но баннер и
        // гейт требуют только настройки. Баннер платного вью Bob→Alice:
        settings, err := r.onMessagesGetPeerSettings(WithUserID(ctx, bob.ID), &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash})
        if err != nil {
                t.Fatalf("get peer settings: %v", err)
        }
        if stars2, ok := settings.Settings.GetChargePaidMessageStars(); !ok || stars2 != 10 {
                t.Fatalf("charge hint = %d, %v; want 10, true", stars2, ok)
        }

        // Отправка без allow_paid_stars → ALLOW_PAYMENT_REQUIRED 10.
        err = paidFlowSend(t, r, ctx, alice, bob, 9001, 0)
        if err == nil || !strings.Contains(err.Error(), "ALLOW_PAYMENT_REQUIRED") || !strings.Contains(err.Error(), "(10)") {
                t.Fatalf("unauthorized send err = %v, want ALLOW_PAYMENT_REQUIRED (10)", err)
        }

        // Авторизованная отправка: Alice платит 10, Bob получает 8 (850‰).
        if err := paidFlowSend(t, r, ctx, alice, bob, 9002, 10); err != nil {
                t.Fatalf("authorized send: %v", err)
        }
        if got, want := paidFlowBalance(t, stars, alice.ID), int64(domain.DefaultStarsStartingGrant-10); got != want {
                t.Fatalf("alice balance = %d, want %d", got, want)
        }
        if got, want := paidFlowBalance(t, stars, bob.ID), int64(10*850/1000); got != want {
                t.Fatalf("bob balance = %d, want %d (850 permille)", got, want)
        }
        revenue, err := r.onAccountGetPaidMessagesRevenue(WithUserID(ctx, bob.ID), &tg.AccountGetPaidMessagesRevenueRequest{
                UserID: &tg.InputUser{UserID: alice.ID, AccessHash: alice.AccessHash},
        })
        if err != nil {
                t.Fatalf("get revenue: %v", err)
        }
        if revenue.StarsAmount != 10 {
                t.Fatalf("revenue = %d, want 10 (брутто потраченное)", revenue.StarsAmount)
        }

        // Недостаток звёзд: опустим баланс Alice ниже ставки через вычитание
        // (дебет всем доступным через служебный debit напрямую в ledger).
        if _, err := stars.Debit(context.Background(), alice.ID, paidFlowBalance(t, stars, alice.ID), domain.StarsReasonAdjust, domain.Peer{}, 0, "", ""); err != nil {
                t.Fatalf("drain alice: %v", err)
        }
        err = paidFlowSend(t, r, ctx, alice, bob, 9003, 10)
        if err == nil || !strings.Contains(err.Error(), "BALANCE_TOO_LOW") {
                t.Fatalf("insufficient send err = %v, want BALANCE_TOO_LOW", err)
        }

        // Возврат стартового баланса (грант уже погашен при первом списании —
        // EnsureGrant был бы no-op) и проверка «получатель платы пишет первым»:
        // Bob пишет Alice — она освобождается от платы Bob'а (протокол paid-messages).
        if _, err := stars.Credit(context.Background(), alice.ID, domain.DefaultStarsStartingGrant, domain.StarsReasonAdjust, domain.Peer{}, 0, "", ""); err != nil {
                t.Fatalf("regrant alice: %v", err)
        }
        if err := paidFlowSend(t, r, ctx, bob, alice, 9004, 0); err != nil {
                t.Fatalf("fee owner first message: %v", err)
        }
        if got := paidFlowBalance(t, stars, bob.ID); got != int64(10*850/1000) {
                t.Fatalf("bob balance after own send = %d, want unchanged %d", got, 10*850/1000)
        }
        // Alice теперь в исключениях Bob'а — её отправка бесплатна и без allow_paid_stars.
        if err := paidFlowSend(t, r, ctx, alice, bob, 9005, 0); err != nil {
                t.Fatalf("exempt-by-first-message send: %v", err)
        }
        if got := paidFlowBalance(t, stars, alice.ID); got != domain.DefaultStarsStartingGrant {
                t.Fatalf("alice balance after free send = %d, want %d", got, domain.DefaultStarsStartingGrant)
        }

        // Возврат: Bob нажимает «Не брать плату» с refund_charged — Alice получает
        // все потраченные на Bob'а звёзды (10), Bob теряет свою нетто-выручку (8).
        ok, err := r.onAccountToggleNoPaidMessagesException(WithUserID(ctx, bob.ID), &tg.AccountToggleNoPaidMessagesExceptionRequest{
                RefundCharged: true,
                UserID:        &tg.InputUser{UserID: alice.ID, AccessHash: alice.AccessHash},
        })
        if err != nil || !ok {
                t.Fatalf("toggle exception: ok=%v err=%v", ok, err)
        }
        if got, want := paidFlowBalance(t, stars, alice.ID), domain.DefaultStarsStartingGrant+int64(10); got != want {
                t.Fatalf("alice balance after refund = %d, want %d", got, want)
        }
        if got, want := paidFlowBalance(t, stars, bob.ID), int64(0); got != want {
                t.Fatalf("bob balance after claw back = %d, want %d", got, want)
        }
        // Повторный вызов ничего не возвращает (защита от двойного возврата).
        if _, err := r.onAccountToggleNoPaidMessagesException(WithUserID(ctx, bob.ID), &tg.AccountToggleNoPaidMessagesExceptionRequest{
                RefundCharged: true,
                UserID:        &tg.InputUser{UserID: alice.ID, AccessHash: alice.AccessHash},
        }); err != nil {
                t.Fatalf("second toggle: %v", err)
        }
        if got := paidFlowBalance(t, stars, alice.ID); got != domain.DefaultStarsStartingGrant+int64(10) {
                t.Fatalf("alice balance after second toggle = %d, want unchanged %d", got, domain.DefaultStarsStartingGrant+10)
        }
        revenue, err = r.onAccountGetPaidMessagesRevenue(WithUserID(ctx, bob.ID), &tg.AccountGetPaidMessagesRevenueRequest{
                UserID: &tg.InputUser{UserID: alice.ID, AccessHash: alice.AccessHash},
        })
        if err != nil {
                t.Fatalf("get revenue after refund: %v", err)
        }
        if revenue.StarsAmount != 0 {
                t.Fatalf("revenue after refund = %d, want 0", revenue.StarsAmount)
        }
}

// TestPaidMessagesToggleRejectsMonoforumFlags — пользовательские вызовы не
// могут требовать оплату (require_payment) и не поддерживают parent_peer.
func TestPaidMessagesToggleRejectsMonoforumFlags(t *testing.T) {
        ctx := context.Background()
        r, _, alice, bob := newPaidMessagesFlowRouter(t)
        _, err := r.onAccountToggleNoPaidMessagesException(WithUserID(ctx, alice.ID), &tg.AccountToggleNoPaidMessagesExceptionRequest{
                RequirePayment: true,
                UserID:         &tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash},
        })
        if err == nil || !strings.Contains(err.Error(), "PRIVACY_KEY_INVALID") {
                t.Fatalf("require_payment err = %v, want PRIVACY_KEY_INVALID", err)
        }
        _, err = r.onAccountToggleNoPaidMessagesException(WithUserID(ctx, alice.ID), &tg.AccountToggleNoPaidMessagesExceptionRequest{
                ParentPeer: &tg.InputPeerChannel{ChannelID: 77, AccessHash: 1},
                UserID:     &tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash},
        })
        if err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
                t.Fatalf("parent_peer err = %v, want PEER_ID_INVALID", err)
        }
}
