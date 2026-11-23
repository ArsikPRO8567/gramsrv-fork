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
	appprivacy "telesrv/internal/app/privacy"
	appusers "telesrv/internal/app/users"
	appstars "telesrv/internal/app/stars"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// newPaidMessageRouter (раунд 37) — полноценный контур платных сообщений:
// реальный memory MessageStore, связанный AttachStarsStore со звёздным
// леджером, приватность и настройки аккаунта получателя с ценой за сообщения
// не-контактов. Стартовый баланс выдается лениво при первом чтении
// (domain.DefaultStarsStartingGrant, mirrors прод-гейт).
func newPaidMessageRouter(t *testing.T, recipientPrice int64) (*Router, *memory.MessageStore, *memory.StarsStore, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	usersStore := memory.NewUserStore()
	alice, err := usersStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550009101", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create Alice: %v", err)
	}
	bob, err := usersStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550009102", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create Bob: %v", err)
	}
	users := appusers.NewService(usersStore)
	contacts := memory.NewContactStore()
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), contacts).ConfigureReadModels(users, nil)
	settingsStore := memory.NewPasswordStore()
	account := appaccount.NewService(settingsStore, appaccount.WithAccountSettings(settingsStore))
	if recipientPrice > 0 {
		if _, err := account.SetGlobalPrivacy(ctx, bob.ID, domain.GlobalPrivacy{NoncontactPeersPaidStars: recipientPrice}); err != nil {
			t.Fatalf("set Bob paid requirement: %v", err)
		}
	}
	messages := memory.NewMessageStore()
	stars := memory.NewStarsStore()
	messages.AttachStarsStore(stars)
	router := New(Config{}, Deps{
		Account:  account,
		Privacy:  privacy,
		Users:    users,
		Messages: appmessages.NewService(messages, nil),
		Stars:    appstars.NewService(stars),
	}, zaptest.NewLogger(t), clock.System)
	return router, messages, stars, alice, bob
}

// findUpdatesMessage извлекает tg.Message обновления new message из ответа
// sendMessage (UpdateNewMessage для приватных сообщений).
func findUpdatesMessage(t *testing.T, updates tg.UpdatesClass, _ int64) (*tg.Message, bool) {
	t.Helper()
	typed, ok := updates.(*tg.Updates)
	if !ok {
		return nil, false
	}
	for _, update := range typed.Updates {
		newMessage, ok := update.(*tg.UpdateNewMessage)
		if !ok {
			continue
		}
		message, ok := newMessage.Message.(*tg.Message)
		if !ok {
			continue
		}
		return message, true
	}
	return nil, false
}

// TestMessagesSendMessagePaidMessageChargesSender — платное сообщение
// работает: сервер списывает Stars отправителя, зачисляет получателю нетто
// (850‰, комиссия сервера) и помечает сообщение paid_message_stars. Ранее
// гейт проверял лимит, но отказывал PAYMENT_UNSUPPORTED — списание не было
// реализовано.
func TestMessagesSendMessagePaidMessageChargesSender(t *testing.T) {
	const price = int64(7)
	r, _, stars, alice, bob := newPaidMessageRouter(t, price)

	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "paid hello",
		RandomID: 9001,
	}
	req.SetAllowPaidStars(10)
	updates, err := r.onMessagesSendMessage(WithUserID(context.Background(), alice.ID), req)
	if err != nil {
		t.Fatalf("paid message send: %v", err)
	}
	if updates == nil {
		t.Fatal("paid message returned no updates")
	}
	// Стартовый грант выдается лениво при чтении баланса гейтом.
	if balance, err := stars.GetBalance(context.Background(), alice.ID); err != nil || balance.Balance != domain.DefaultStarsStartingGrant-price {
		t.Fatalf("sender balance = %d err %v, want %d", balance.Balance, err, domain.DefaultStarsStartingGrant-price)
	}
	if balance, err := stars.GetBalance(context.Background(), bob.ID); err != nil || balance.Balance != price*850/1000 {
		t.Fatalf("recipient balance = %d err %v, want %d (850 permille)", balance.Balance, err, price*850/1000)
	}
	senderMessage, ok := findUpdatesMessage(t, updates, alice.ID)
	if !ok {
		t.Fatalf("sender echo message not found in updates: %#v", updates)
	}
	if stars2, paid := senderMessage.GetPaidMessageStars(); !paid || stars2 != price {
		t.Fatalf("sender message paid_message_stars = %d/%v, want %d/true", stars2, paid, price)
	}
	recipientMessage, ok := findUpdatesMessage(t, updates, bob.ID)
	if !ok {
		t.Fatalf("recipient message not found in updates: %#v", updates)
	}
	if stars2, paid := recipientMessage.GetPaidMessageStars(); !paid || stars2 != price {
		t.Fatalf("recipient message paid_message_stars = %d/%v, want %d/true", stars2, paid, price)
	}
}

// TestMessagesSendMessagePaidMessageRequiresAuthorization — при цене выше
// авторизации сервер отвечает ALLOW_PAYMENT_REQUIRED с суммой за всё
// сообщение (клиент показывает платёжную форму), а не PAYMENT_UNSUPPORTED.
func TestMessagesSendMessagePaidMessageRequiresAuthorization(t *testing.T) {
	const price = int64(7)
	r, _, _, alice, bob := newPaidMessageRouter(t, price)
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "underpaid",
		RandomID: 9002,
	}
	req.SetAllowPaidStars(3)
	_, err := r.onMessagesSendMessage(WithUserID(context.Background(), alice.ID), req)
	if err == nil || !strings.Contains(err.Error(), "ALLOW_PAYMENT_REQUIRED") {
		t.Fatalf("underpaid err = %v, want ALLOW_PAYMENT_REQUIRED", err)
	}
	if !strings.Contains(err.Error(), "7") {
		t.Fatalf("underpaid err must carry the required amount: %v", err)
	}
}

// TestMessagesSendMessagePaidMessageBalanceTooLow — недостаточный баланс
// откатывает отправку целиком: BALANCE_TOO_LOW, сообщения нет.
func TestMessagesSendMessagePaidMessageBalanceTooLow(t *testing.T) {
	const price = int64(7)
	r, _, stars, alice, bob := newPaidMessageRouter(t, price)
	ctx := context.Background()
	// Остается 3 звезды: ленивый грант погашается дебетом до ставки ниже цены.
	if _, _, err := stars.EnsureGrant(ctx, alice.ID, domain.DefaultStarsStartingGrant, 0); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	if _, err := stars.Debit(ctx, alice.ID, domain.DefaultStarsStartingGrant-3, domain.StarsReasonAdjust, domain.Peer{}, 0, "", ""); err != nil {
		t.Fatalf("drain to 3: %v", err)
	}

	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "cannot afford",
		RandomID: 9003,
	}
	req.SetAllowPaidStars(10)
	_, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), req)
	if err == nil || !strings.Contains(err.Error(), "BALANCE_TOO_LOW") {
		t.Fatalf("insufficient balance err = %v, want BALANCE_TOO_LOW", err)
	}
	if balance, err := stars.GetBalance(ctx, alice.ID); err != nil || balance.Balance != 3 {
		t.Fatalf("sender balance after failed send = %d err %v, want unchanged 3", balance.Balance, err)
	}
}
