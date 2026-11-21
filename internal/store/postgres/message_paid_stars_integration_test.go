package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestPrivatePaidMessageChargesBothLedgers (раунд 37) — платные сообщения в
// личных чатах: списание отправителю и зачисление получателю происходят в
// одной транзакции с сообщением; обе строки леджера носят ОДИН публичный ID
// транзакции (требование «айди соответствующих транзакций должны совпадать»).
func TestPrivatePaidMessageChargesBothLedgers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1886"+suffix+"01", "PaidSender", "")
	recipient := createTestUser(t, ctx, users, "+1886"+suffix+"02", "PaidRecipient", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
		_, _ = pool.Exec(ctx, "DELETE FROM stars_transactions WHERE user_id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
		_, _ = pool.Exec(ctx, "DELETE FROM stars_balances WHERE user_id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	if _, err := pool.Exec(ctx, `INSERT INTO stars_balances(user_id,balance,granted) VALUES($1,500,true) ON CONFLICT(user_id) DO UPDATE SET balance=500,granted=true`, sender.ID); err != nil {
		t.Fatalf("seed sender balance: %v", err)
	}

	messages := NewMessageStore(pool)
	req := domain.SendPrivateTextRequest{
		SenderUserID:     sender.ID,
		RecipientUserID:  recipient.ID,
		RandomID:         88001,
		Message:          "paid hello",
		Date:             1700003000,
		PaidMessageStars: 25,
	}
	got, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("SendPrivateText paid: %v", err)
	}
	if got.SenderMessage.PaidMessageStars != 25 || got.RecipientMessage.PaidMessageStars != 25 {
		t.Fatalf("message paid stars = sender %d / recipient %d, want 25/25",
			got.SenderMessage.PaidMessageStars, got.RecipientMessage.PaidMessageStars)
	}
	var senderBalance, recipientBalance int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, sender.ID).Scan(&senderBalance); err != nil || senderBalance != 475 {
		t.Fatalf("sender balance = %d err %v, want 475", senderBalance, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, recipient.ID).Scan(&recipientBalance); err != nil || recipientBalance != 25 {
		t.Fatalf("recipient balance = %d err %v, want 25", recipientBalance, err)
	}
	var debitID, creditID string
	var debitAmount, creditAmount int64
	var debitPeer, creditPeer int64
	if err := pool.QueryRow(ctx, `SELECT transaction_id, amount, peer_id FROM stars_transactions WHERE user_id=$1 AND reason='paid_message'`,
		sender.ID).Scan(&debitID, &debitAmount, &debitPeer); err != nil {
		t.Fatalf("sender paid_message txn: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT transaction_id, amount, peer_id FROM stars_transactions WHERE user_id=$1 AND reason='paid_message'`,
		recipient.ID).Scan(&creditID, &creditAmount, &creditPeer); err != nil {
		t.Fatalf("recipient paid_message txn: %v", err)
	}
	if debitID == "" || debitID != creditID {
		t.Fatalf("transaction ids must match across accounts: sender %q recipient %q", debitID, creditID)
	}
	if len(debitID) != len("stars_")+60 || debitID[:6] != "stars_" {
		t.Fatalf("transaction id shape = %q, want stars_<60>", debitID)
	}
	if debitAmount != -25 || creditAmount != 25 {
		t.Fatalf("ledger amounts = %d/%d, want -25/25", debitAmount, creditAmount)
	}
	if debitPeer != recipient.ID || creditPeer != sender.ID {
		t.Fatalf("ledger peers = %d/%d, want %d/%d", debitPeer, creditPeer, recipient.ID, sender.ID)
	}

	// Повтор той же отправки (lost-response replay) не должен списывать снова.
	replay, err := messages.SendPrivateText(ctx, req)
	if err != nil || !replay.Duplicate {
		t.Fatalf("paid replay = %+v err %v, want duplicate", replay, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, sender.ID).Scan(&senderBalance); err != nil || senderBalance != 475 {
		t.Fatalf("sender balance after replay = %d err %v, want 475", senderBalance, err)
	}
	var txnCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM stars_transactions WHERE user_id=$1 AND reason='paid_message'`, sender.ID).Scan(&txnCount); err != nil || txnCount != 1 {
		t.Fatalf("sender paid_message txn count = %d err %v, want 1", txnCount, err)
	}

	// Недостаточный баланс: отправка целиком откатывается (сообщения нет,
	// балансы не изменились).
	if _, err := pool.Exec(ctx, `UPDATE stars_balances SET balance=10 WHERE user_id=$1`, sender.ID); err != nil {
		t.Fatalf("drain sender balance: %v", err)
	}
	_, err = messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID, RandomID: 88002,
		Message: "too expensive", Date: 1700003001, PaidMessageStars: 25,
	})
	if !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("insufficient balance err = %v, want ErrStarsInsufficient", err)
	}
	var boxCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM message_boxes WHERE owner_user_id=$1 AND private_message_id IN (
SELECT id FROM private_messages WHERE sender_user_id=$1 AND random_id=88002)`, sender.ID).Scan(&boxCount); err != nil || boxCount != 0 {
		t.Fatalf("failed paid send must not leave boxes: count=%d err %v", boxCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, recipient.ID).Scan(&recipientBalance); err != nil || recipientBalance != 25 {
		t.Fatalf("recipient balance after failed send = %d err %v, want 25", recipientBalance, err)
	}

	// Само-сообщение и недоставленное (заблокировавший получатель) — бесплатно.
	selfSent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: sender.ID, RandomID: 88003,
		Message: "note", Date: 1700003002, PaidMessageStars: 25,
	})
	if err != nil || selfSent.SenderMessage.PaidMessageStars != 0 {
		t.Fatalf("self paid message = %+v err %v, want free", selfSent, err)
	}
	blocked, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID, RandomID: 88004,
		Message: "blocked", Date: 1700003003, PaidMessageStars: 25, RecipientBlocked: true,
	})
	if err != nil || blocked.SenderMessage.PaidMessageStars != 0 {
		t.Fatalf("blocked recipient paid message = %+v err %v, want free", blocked, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, sender.ID).Scan(&senderBalance); err != nil || senderBalance != 10 {
		t.Fatalf("sender balance after free paths = %d err %v, want 10", senderBalance, err)
	}
}
