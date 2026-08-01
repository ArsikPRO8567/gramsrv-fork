package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestStarsFriendGiftPurchaseAtomicReplayAndValidationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	buyer, err := users.Create(ctx, domain.User{AccessHash: 94101, Phone: "+1665941" + suffix + "01", FirstName: "GiftBuyer"})
	if err != nil {
		t.Fatalf("create buyer: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 94102, Phone: "+1665941" + suffix + "02", FirstName: "GiftRecipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM stars_gift_purchase_commands WHERE buyer_user_id=$1", buyer.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM stars_gift_purchase_forms WHERE buyer_user_id=$1", buyer.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM stars_transactions WHERE user_id=$1", recipient.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM stars_balances WHERE user_id=$1", recipient.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=ANY($1::bigint[])", []int64{buyer.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	store := NewStarsGiftPurchaseStore(pool, messages)
	issued, err := store.IssueStarsGiftPurchaseForm(ctx, domain.StarsGiftPurchaseForm{
		BuyerUserID: buyer.ID, RecipientUserID: recipient.ID,
		Stars: 2500, Currency: "USD", Amount: 199,
		IssuedAt: 1_700_000_000, ExpiresAt: 1_700_000_600,
	})
	if err != nil || issued.FormID == 0 {
		t.Fatalf("issue form = %+v err=%v", issued, err)
	}
	var origin [8]byte
	origin[0] = 9
	req := domain.StarsGiftPurchaseRequest{
		StarsGiftPurchaseForm: domain.StarsGiftPurchaseForm{
			FormID: issued.FormID, BuyerUserID: buyer.ID, RecipientUserID: recipient.ID,
			Stars: 2500, Currency: "USD", Amount: 199,
		},
		Date: 1_700_000_100, OriginAuthKeyID: origin, OriginSessionID: 77,
	}
	first, err := store.PurchaseStarsGift(ctx, req)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if first.Duplicate || first.RecipientBalance.Balance != 2500 || first.TransactionID == "" ||
		first.Send.SenderEvent.PtsCount != 1 || first.Send.RecipientEvent.PtsCount != 1 {
		t.Fatalf("first purchase = %+v", first)
	}
	if first.Send.SenderMessage.Pts <= 0 || first.Send.RecipientMessage.Pts <= 0 ||
		first.Send.SenderMessage.UID == 0 || first.Send.SenderMessage.UID != first.Send.RecipientMessage.UID {
		t.Fatalf("bilateral send = %+v", first.Send)
	}
	action := first.Send.RecipientMessage.Media.ServiceAction.GiftStars
	if action == nil || action.Stars != 2500 || action.Currency != "USD" || action.Amount != 199 ||
		action.TransactionID != first.TransactionID || action.BalanceAfter != 2500 {
		t.Fatalf("recipient gift action = %+v", action)
	}

	replay, err := store.PurchaseStarsGift(ctx, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Duplicate || replay.TransactionID != first.TransactionID ||
		replay.Send.SenderMessage.ID != first.Send.SenderMessage.ID || replay.Send.SenderEvent.Pts != first.Send.SenderEvent.Pts {
		t.Fatalf("replay = %+v, first=%+v", replay, first)
	}

	var balance, txnCount, commandCount int64
	if err := pool.QueryRow(ctx, "SELECT balance FROM stars_balances WHERE user_id=$1", recipient.ID).Scan(&balance); err != nil {
		t.Fatalf("load recipient balance: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM stars_transactions WHERE user_id=$1 AND reason='gift'", recipient.ID).Scan(&txnCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM stars_gift_purchase_commands WHERE buyer_user_id=$1", buyer.ID).Scan(&commandCount); err != nil {
		t.Fatal(err)
	}
	if balance != 2500 || txnCount != 1 || commandCount != 1 {
		t.Fatalf("replay footprint balance=%d txns=%d commands=%d", balance, txnCount, commandCount)
	}
	for _, userID := range []int64{buyer.ID, recipient.ID} {
		var eventCount, outboxCount int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM user_update_events WHERE user_id=$1", userID).Scan(&eventCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM dispatch_outbox WHERE target_user_id=$1", userID).Scan(&outboxCount); err != nil {
			t.Fatal(err)
		}
		if eventCount != 1 || outboxCount != 1 {
			t.Fatalf("user %d event/outbox=%d/%d, want 1/1", userID, eventCount, outboxCount)
		}
	}

	tampered := req
	tampered.Amount++
	if _, err := store.PurchaseStarsGift(ctx, tampered); !errors.Is(err, domain.ErrStarsGiftFormInvalid) {
		t.Fatalf("tampered replay err=%v", err)
	}
	expired, err := store.IssueStarsGiftPurchaseForm(ctx, domain.StarsGiftPurchaseForm{
		BuyerUserID: buyer.ID, RecipientUserID: recipient.ID,
		Stars: 1000, Currency: "USD", Amount: 99,
		IssuedAt: 1_699_999_000, ExpiresAt: 1_699_999_600,
	})
	if err != nil {
		t.Fatalf("issue expired form: %v", err)
	}
	expiredReq := req
	expiredReq.FormID, expiredReq.Stars, expiredReq.Amount = expired.FormID, 1000, 99
	if _, err := store.PurchaseStarsGift(ctx, expiredReq); !errors.Is(err, domain.ErrStarsGiftFormExpired) {
		t.Fatalf("expired form err=%v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT balance FROM stars_balances WHERE user_id=$1", recipient.ID).Scan(&balance); err != nil || balance != 2500 {
		t.Fatalf("balance after failures=%d err=%v", balance, err)
	}
}
