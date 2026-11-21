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
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// newPaidMessageRouter (раунд 37) — полноценный контур платных сообщений:
// реальный memory MessageStore с внутренним Stars-балансом, приватность и
// настройки аккаунта получателя с ценой за сообщения не-контактов.
func newPaidMessageRouter(t *testing.T, recipientPrice int64) (*Router, *memory.MessageStore, domain.User, domain.User) {
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
	router := New(Config{}, Deps{
		Account:  account,
		Privacy:  privacy,
		Users:    users,
		Messages: appmessages.NewService(messages, nil),
	}, zaptest.NewLogger(t), clock.System)
	return router, messages, alice, bob
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
// работает: сервер списывает Stars отправителя, зачисляет получателю и
// помечает сообщение paid_message_stars. Ранее гейт проверял лимит, но
// отказывал PAYMENT_UNSUPPORTED — списание не было реализовано.
func TestMessagesSendMessagePaidMessageChargesSender(t *testing.T) {
	const price = int64(7)
	r, messages, alice, bob := newPaidMessageRouter(t, price)
	messages.SetStarsBalance(alice.ID, 100)
	messages.SetStarsBalance(bob.ID, 0)

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
	if balance := messages.StarsBalance(alice.ID); balance != 100-price {
		t.Fatalf("sender balance = %d, want %d", balance, 100-price)
	}
	if balance := messages.StarsBalance(bob.ID); balance != price {
		t.Fatalf("recipient balance = %d, want %d", balance, price)
	}
	senderMessage, ok := findUpdatesMessage(t, updates, alice.ID)
	if !ok {
		t.Fatalf("sender echo message not found in updates: %#v", updates)
	}
	if stars, paid := senderMessage.GetPaidMessageStars(); !paid || stars != price {
		t.Fatalf("sender message paid_message_stars = %d/%v, want %d/true", stars, paid, price)
	}
	recipientMessage, ok := findUpdatesMessage(t, updates, bob.ID)
	if !ok {
		t.Fatalf("recipient message not found in updates: %#v", updates)
	}
	if stars, paid := recipientMessage.GetPaidMessageStars(); !paid || stars != price {
		t.Fatalf("recipient message paid_message_stars = %d/%v, want %d/true", stars, paid, price)
	}
}

// TestMessagesSendMessagePaidMessageRequiresAuthorization — при цене выше
// авторизации сервер отвечает ALLOW_PAYMENT_REQUIRED с суммой за всё
// сообщение (клиент показывает платёжную форму), а не PAYMENT_UNSUPPORTED.
func TestMessagesSendMessagePaidMessageRequiresAuthorization(t *testing.T) {
	const price = int64(7)
	r, _, alice, bob := newPaidMessageRouter(t, price)
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
	r, messages, alice, bob := newPaidMessageRouter(t, price)
	messages.SetStarsBalance(alice.ID, 3)

	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "cannot afford",
		RandomID: 9003,
	}
	req.SetAllowPaidStars(10)
	_, err := r.onMessagesSendMessage(WithUserID(context.Background(), alice.ID), req)
	if err == nil || !strings.Contains(err.Error(), "BALANCE_TOO_LOW") {
		t.Fatalf("insufficient balance err = %v, want BALANCE_TOO_LOW", err)
	}
	if balance := messages.StarsBalance(alice.ID); balance != 3 {
		t.Fatalf("sender balance after failed send = %d, want unchanged 3", balance)
	}
}
