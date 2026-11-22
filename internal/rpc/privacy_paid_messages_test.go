package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	appprivacy "telesrv/internal/app/privacy"
	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newPrivateRequirementRouter(t *testing.T, senderPremium bool) (*Router, *appaccount.Service, *appprivacy.Service, *memory.ContactStore, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	usersStore := memory.NewUserStore()
	aliceInput := domain.User{AccessHash: 11, Phone: "15550008101", FirstName: "Alice"}
	if senderPremium {
		aliceInput.PremiumUntil = int(time.Now().Add(time.Hour).Unix())
	}
	alice, err := usersStore.Create(ctx, aliceInput)
	if err != nil {
		t.Fatalf("create Alice: %v", err)
	}
	bob, err := usersStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550008102", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create Bob: %v", err)
	}
	users := appusers.NewService(usersStore)
	contacts := memory.NewContactStore()
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), contacts).ConfigureReadModels(users, nil)
	settingsStore := memory.NewPasswordStore()
	account := appaccount.NewService(settingsStore, appaccount.WithAccountSettings(settingsStore))
	router := New(Config{}, Deps{
		Account:  account,
		Privacy:  privacy,
		Users:    users,
		Messages: &captureMessages{},
	}, zaptest.NewLogger(t), clock.System)
	return router, account, privacy, contacts, alice, bob
}

// newPrivateLedgerRouter 复用上面的骨架并接入真实账本（memory StarsStore）：
// 问题 B 的付费消息扣费与问题 C 的配对交易 ID 都在这条链路上验证。
func newPrivateLedgerRouter(t *testing.T, senderPremium bool) (*Router, *appaccount.Service, *memory.StarsStore, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	usersStore := memory.NewUserStore()
	aliceInput := domain.User{AccessHash: 11, Phone: "15550008101", FirstName: "Alice"}
	if senderPremium {
		aliceInput.PremiumUntil = int(time.Now().Add(time.Hour).Unix())
	}
	alice, err := usersStore.Create(ctx, aliceInput)
	if err != nil {
		t.Fatalf("create Alice: %v", err)
	}
	bob, err := usersStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550008102", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create Bob: %v", err)
	}
	users := appusers.NewService(usersStore)
	contacts := memory.NewContactStore()
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), contacts).ConfigureReadModels(users, nil)
	settingsStore := memory.NewPasswordStore()
	account := appaccount.NewService(settingsStore, appaccount.WithAccountSettings(settingsStore))
	starsStore := memory.NewStarsStore()
	router := New(Config{}, Deps{
		Account:  account,
		Privacy:  privacy,
		Users:    users,
		Messages: &captureMessages{},
		Stars:    appstars.NewService(starsStore, appstars.WithStartingGrant(0)),
	}, zaptest.NewLogger(t), clock.System)
	return router, account, starsStore, alice, bob
}

func TestPrivateContactRequirementUsesNoPaidMessagesReadModel(t *testing.T) {
	ctx := context.Background()
	r, account, privacy, contacts, alice, bob := newPrivateRequirementRouter(t, false)
	if _, err := account.SetGlobalPrivacy(ctx, bob.ID, domain.GlobalPrivacy{NoncontactPeersPaidStars: 7}); err != nil {
		t.Fatalf("set Bob paid requirement: %v", err)
	}
	full, err := r.onUsersGetFullUser(WithUserID(ctx, alice.ID), &tg.InputUser{
		UserID: bob.ID, AccessHash: bob.AccessHash,
	})
	if err != nil {
		t.Fatalf("get Bob full user: %v", err)
	}
	if stars, ok := full.FullUser.GetSendPaidMessagesStars(); !ok || stars != 7 {
		t.Fatalf("full user paid stars = %d, %v; want 7, true", stars, ok)
	}
	projected, ok := full.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("projected user = %T, want *tg.User", full.Users[0])
	}
	if stars, ok := projected.GetSendPaidMessagesStars(); !ok || stars != 7 {
		t.Fatalf("user paid stars = %d, %v; want 7, true", stars, ok)
	}
	requirements, err := r.onUsersGetRequirementsToContact(WithUserID(ctx, alice.ID), []tg.InputUserClass{
		&tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash},
	})
	if err != nil {
		t.Fatalf("get requirements: %v", err)
	}
	if paid, ok := requirements[0].(*tg.RequirementToContactPaidMessages); !ok || paid.StarsAmount != 7 {
		t.Fatalf("requirement = %#v, want paid 7", requirements[0])
	}

	send := func(randomID int64, allow int64) error {
		req := &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
			Message:  "hello",
			RandomID: randomID,
		}
		if allow != 0 {
			req.SetAllowPaidStars(allow)
		}
		_, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), req)
		return err
	}

	if err := send(81001, 0); err == nil || !strings.Contains(err.Error(), "ALLOW_PAYMENT_REQUIRED") || !strings.Contains(err.Error(), "(7)") {
		t.Fatalf("non-exempt send err = %v, want ALLOW_PAYMENT_REQUIRED 7", err)
	}
	if err := send(81002, 7); err == nil || !strings.Contains(err.Error(), "PAYMENT_UNSUPPORTED") {
		t.Fatalf("authorized paid send err = %v, want explicit unsupported ledger", err)
	}

	if _, err := privacy.SetRules(ctx, bob.ID, domain.PrivacyKeyNoPaidMessages, []domain.PrivacyRule{{
		Kind:    domain.PrivacyRuleAllowUsers,
		UserIDs: []int64{alice.ID},
	}, {
		Kind: domain.PrivacyRuleDisallowAll,
	}}); err != nil {
		t.Fatalf("allow Alice in NoPaidMessages: %v", err)
	}
	if err := send(81003, 0); err != nil {
		t.Fatalf("explicit no-paid exception send: %v", err)
	}
	full, err = r.onUsersGetFullUser(WithUserID(ctx, alice.ID), &tg.InputUser{
		UserID: bob.ID, AccessHash: bob.AccessHash,
	})
	if err != nil {
		t.Fatalf("get exempt Bob full user: %v", err)
	}
	if _, ok := full.FullUser.GetSendPaidMessagesStars(); ok || full.FullUser.ContactRequirePremium {
		t.Fatalf("exempt full user still carries contact restriction: %+v", full.FullUser)
	}

	if _, err := privacy.SetRules(ctx, bob.ID, domain.PrivacyKeyNoPaidMessages, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("clear explicit exception: %v", err)
	}
	if _, err := contacts.Upsert(ctx, bob.ID, domain.ContactInput{
		ContactUserID: alice.ID,
		FirstName:     "Alice",
	}); err != nil {
		t.Fatalf("Bob add Alice contact: %v", err)
	}
	if err := send(81004, 0); err != nil {
		t.Fatalf("recipient contact must be free: %v", err)
	}
}

// TestPaidMessageLedgerTransfer 问题 B+C：接入真实账本后，授权的付费消息
// 发送成功并原子扣费；发送方借记行与接收方贷记行共享同一个公开交易 ID。
func TestPaidMessageLedgerTransfer(t *testing.T) {
	ctx := context.Background()
	r, account, starsStore, alice, bob := newPrivateLedgerRouter(t, false)
	if _, err := account.SetGlobalPrivacy(ctx, bob.ID, domain.GlobalPrivacy{NoncontactPeersPaidStars: 7}); err != nil {
		t.Fatalf("set Bob paid requirement: %v", err)
	}
	// 起始授予关闭（WithStartingGrant(0)）：先手动给 Alice 足够余额。
	if _, _, err := starsStore.EnsureGrant(ctx, alice.ID, 100, int(time.Now().Unix())); err != nil {
		t.Fatalf("grant Alice stars: %v", err)
	}
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:           &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:        "paid hello",
		RandomID:       83001,
		AllowPaidStars: 7,
	})
	if err != nil {
		t.Fatalf("paid send err = %v, want success with ledger charge", err)
	}
	if sent == nil {
		t.Fatalf("paid send returned nil updates")
	}
	// 余额：Alice 100 - 7 = 93；Bob 0 + 7 = 7。
	aliceBalance, err := starsStore.GetBalance(ctx, alice.ID)
	if err != nil || aliceBalance.Balance != 93 {
		t.Fatalf("alice balance = %d, err %v; want 93", aliceBalance.Balance, err)
	}
	bobBalance, err := starsStore.GetBalance(ctx, bob.ID)
	if err != nil || bobBalance.Balance != 7 {
		t.Fatalf("bob balance = %d, err %v; want 7", bobBalance.Balance, err)
	}
	// 问题 C：两侧行共享同一个公开交易 ID，reason=paid_message，peer 互指。
	senderPage, err := starsStore.ListTransactions(ctx, alice.ID, domain.StarsTransactionQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list alice txns: %v", err)
	}
	var senderRow domain.StarsTransaction
	for _, item := range senderPage.Transactions {
		if item.Amount == -7 {
			senderRow = item
			break
		}
	}
	if senderRow.ID == 0 {
		t.Fatalf("alice debit row missing; page %+v", senderPage.Transactions)
	}
	if senderRow.Reason != domain.StarsReasonPaidMessage {
		t.Fatalf("sender reason = %s, want paid_message", senderRow.Reason)
	}
	if senderRow.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID}) {
		t.Fatalf("sender peer = %+v, want Bob", senderRow.Peer)
	}
	if senderRow.TransactionID == "" {
		t.Fatalf("sender row carries no public transaction id")
	}
	recipientPage, err := starsStore.ListTransactions(ctx, bob.ID, domain.StarsTransactionQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list bob txns: %v", err)
	}
	if len(recipientPage.Transactions) != 1 {
		t.Fatalf("bob txn count = %d, want 1", len(recipientPage.Transactions))
	}
	recipientRow := recipientPage.Transactions[0]
	if recipientRow.Amount != 7 || recipientRow.Reason != domain.StarsReasonPaidMessage {
		t.Fatalf("recipient row = %+v, want +7 paid_message", recipientRow)
	}
	if recipientRow.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}) {
		t.Fatalf("recipient peer = %+v, want Alice", recipientRow.Peer)
	}
	if recipientRow.TransactionID != senderRow.TransactionID {
		t.Fatalf("paired transaction ids differ: sender %q recipient %q", senderRow.TransactionID, recipientRow.TransactionID)
	}
	// 余额不足：全新 router（无缓存的隐私/设置读模型），发送方没有余额 →
	// BALANCE_TOO_LOW。生产环境设置变更经 PostgreSQL read-model listener 失效
	// 缓存；内存测试没有 listener，避免复用已缓存的 router。
	r2, account2, _, alice2, bob2 := newPrivateLedgerRouter(t, false)
	if _, err := account2.SetGlobalPrivacy(ctx, bob2.ID, domain.GlobalPrivacy{NoncontactPeersPaidStars: 7}); err != nil {
		t.Fatalf("set Bob2 paid requirement: %v", err)
	}
	_, err = r2.onMessagesSendMessage(WithUserID(ctx, alice2.ID), &tg.MessagesSendMessageRequest{
		Peer:           &tg.InputPeerUser{UserID: bob2.ID, AccessHash: bob2.AccessHash},
		Message:        "cannot afford",
		RandomID:       83002,
		AllowPaidStars: 7,
	})
	if err == nil || !strings.Contains(err.Error(), "BALANCE_TOO_LOW") {
		t.Fatalf("insufficient paid send err = %v, want BALANCE_TOO_LOW", err)
	}
}

func TestPrivateContactRequirementPremiumGateUsesViewerFactsReadModel(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		senderPremium bool
		wantErr       bool
	}{
		{name: "non-premium blocked", wantErr: true},
		{name: "premium allowed", senderPremium: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, account, _, _, alice, bob := newPrivateRequirementRouter(t, tc.senderPremium)
			if _, err := account.SetGlobalPrivacy(ctx, bob.ID, domain.GlobalPrivacy{NewNoncontactPeersRequirePremium: true}); err != nil {
				t.Fatalf("set premium requirement: %v", err)
			}
			_, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
				Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
				Message:  "hello",
				RandomID: 82001,
			})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "PREMIUM_ACCOUNT_REQUIRED") {
					t.Fatalf("send err = %v, want PREMIUM_ACCOUNT_REQUIRED", err)
				}
			} else if err != nil {
				t.Fatalf("premium sender should pass: %v", err)
			}
		})
	}
}
