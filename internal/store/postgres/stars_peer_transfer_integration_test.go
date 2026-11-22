package postgres

// Раунд 37 (проблемы B/C): атомарный парный перевод «аккаунт -> аккаунт».
//
// Проблема B: платные сообщения — раньше сервер отвечал
// PAYMENT_UNSUPPORTED без списания. Теперь rpc-слой вызывает
// StarsStore.TransferToUser: списание отправителю и зачисление получателю
// в одной транзакции.
//
// Проблема C: обе строки (дебет отправителя и кредит получателя) несут
// ОДИН публичный transaction_id — клиент сопоставляет переводы между
// аккаунтами по этому идентификатору. Resale-путь (покупка подарка на
// маркете) парится тем же механизмом через starsLedgerExtra.transactionID.

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestStarsPeerTransferPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())

	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1889"+suffix+"01", "TransferSender", "")
	recipient := createTestUser(t, ctx, users, "+1889"+suffix+"02", "TransferRecipient", "")

	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, sender.ID, 500, now); err != nil {
		t.Fatalf("grant sender: %v", err)
	}

	// 1) Успешный перевод: 500 - 120 = 380 у отправителя, 120 у получателя.
	result, err := stars.TransferToUser(ctx, domain.StarsPeerTransferRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID,
		Amount: 120, Reason: domain.StarsReasonPaidMessage, Date: now,
	})
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if result.TransactionID == "" {
		t.Fatalf("transfer returned empty public transaction id")
	}
	senderBalance, err := stars.GetBalance(ctx, sender.ID)
	if err != nil || senderBalance.Balance != 380 {
		t.Fatalf("sender balance = %d err %v, want 380", senderBalance.Balance, err)
	}
	recipientBalance, err := stars.GetBalance(ctx, recipient.ID)
	if err != nil || recipientBalance.Balance != 120 {
		t.Fatalf("recipient balance = %d err %v, want 120", recipientBalance.Balance, err)
	}

	// 2) Обе строки с одним публичным id; peer'ы указывают друг на друга;
	// reason одинаковый; суммы зеркальные.
	senderRows, err := stars.ListTransactionsByID(ctx, sender.ID, []string{result.TransactionID})
	if err != nil || len(senderRows) != 1 {
		t.Fatalf("sender rows by id = %d err %v, want 1", len(senderRows), err)
	}
	if senderRows[0].Amount != -120 || senderRows[0].Reason != domain.StarsReasonPaidMessage {
		t.Fatalf("sender row = %+v, want -120 paid_message", senderRows[0])
	}
	if senderRows[0].Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}) {
		t.Fatalf("sender row peer = %+v, want recipient", senderRows[0].Peer)
	}
	recipientRows, err := stars.ListTransactionsByID(ctx, recipient.ID, []string{result.TransactionID})
	if err != nil || len(recipientRows) != 1 {
		t.Fatalf("recipient rows by id = %d err %v, want 1", len(recipientRows), err)
	}
	if recipientRows[0].Amount != 120 {
		t.Fatalf("recipient row amount = %d, want 120", recipientRows[0].Amount)
	}
	if recipientRows[0].Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}) {
		t.Fatalf("recipient row peer = %+v, want sender", recipientRows[0].Peer)
	}

	// 3) Недостаточно средств: атомарность — ни balances, ни строки не меняются.
	_, err = stars.TransferToUser(ctx, domain.StarsPeerTransferRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID,
		Amount: 1_000_000, Reason: domain.StarsReasonPaidMessage, Date: now,
	})
	if !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("overdraft transfer err = %v, want ErrStarsInsufficient", err)
	}
	if bal, _ := stars.GetBalance(ctx, sender.ID); bal.Balance != 380 {
		t.Fatalf("sender balance after failed transfer = %d, want 380", bal.Balance)
	}

	// 4) Компенсационный возврат: обе строки с refund и новым общим id.
	refund, err := stars.RefundPeerTransfer(ctx, domain.StarsPeerTransferRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID,
		Amount: 120, Reason: domain.StarsReasonPaidMessage, Date: now,
	})
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if refund.TransactionID == "" || refund.TransactionID == result.TransactionID {
		t.Fatalf("refund id = %q, want new non-empty id distinct from transfer", refund.TransactionID)
	}
	if bal, _ := stars.GetBalance(ctx, sender.ID); bal.Balance != 500 {
		t.Fatalf("sender balance after refund = %d, want 500", bal.Balance)
	}
	if bal, _ := stars.GetBalance(ctx, recipient.ID); bal.Balance != 0 {
		t.Fatalf("recipient balance after refund = %d, want 0", bal.Balance)
	}
	refundSenderRows, err := stars.ListTransactionsByID(ctx, sender.ID, []string{refund.TransactionID})
	if err != nil || len(refundSenderRows) != 1 || !refundSenderRows[0].Refunded || refundSenderRows[0].Amount != 120 {
		t.Fatalf("refund sender row = %+v err %v, want +120 refunded", refundSenderRows, err)
	}
	refundRecipientRows, err := stars.ListTransactionsByID(ctx, recipient.ID, []string{refund.TransactionID})
	if err != nil || len(refundRecipientRows) != 1 || !refundRecipientRows[0].Refunded || refundRecipientRows[0].Amount != -120 {
		t.Fatalf("refund recipient row = %+v err %v, want -120 refunded", refundRecipientRows, err)
	}

	// 5) Невалидные запросы отклоняются до записи.
	if _, err := stars.TransferToUser(ctx, domain.StarsPeerTransferRequest{
		SenderUserID: sender.ID, RecipientUserID: sender.ID, Amount: 5,
		Reason: domain.StarsReasonPaidMessage, Date: now,
	}); !errors.Is(err, domain.ErrStarsInvalidAmount) {
		t.Fatalf("self-transfer err = %v, want ErrStarsInvalidAmount", err)
	}
}

// TestStarsResalePairedTransactionIDsPostgres — проблема C на маркет-пути:
// дебет покупателя и кредит продавца при покупке коллекционного подарка на
// маркете должны нести один публичный transaction_id.
func TestStarsResalePairedTransactionIDsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	seller := createTestUser(t, ctx, users, "+1888"+suffix+"01", "PairedSeller", "")
	buyer := createTestUser(t, ctx, users, "+1888"+suffix+"02", "PairedBuyer", "")
	sellerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: seller.ID}

	stars := NewStarsStore(pool)
	for _, u := range []domain.User{seller, buyer} {
		if _, _, err := stars.EnsureGrant(ctx, u.ID, 10000, now); err != nil {
			t.Fatalf("grant: %v", err)
		}
	}

	gifts := NewStarGiftStore(pool)
	base := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "PairedResale " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(base, "paired-resale.tgs"),
		Blob:     collectibleTestBlob(base, "paired-resale"), Animation: collectibleTestAnimation("paired-resale.tgs"),
		Actor: "integration", CommandID: "paired-resale-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 20, SlugPrefix: "prt-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+1, "model.tgs"), Blob: collectibleTestBlobPtr(base+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+4, "model-two.tgs"), Blob: collectibleTestBlobPtr(base+4, "model-two"), Animation: collectibleTestAnimationPtr("model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+3, "pattern.tgs"), Blob: collectibleTestBlobPtr(base+3, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+5, "pattern-two.tgs"), Blob: collectibleTestBlobPtr(base+5, "pattern-two"), Animation: collectibleTestAnimationPtr("pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 77, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 78, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "paired-resale-pool-" + suffix,
	}); err != nil {
		t.Fatalf("pool: %v", err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
	}))

	purchase := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
		BuyerUserID: seller.ID, To: sellerPeer, GiftID: entry.Gift.ID, IncludeUpgrade: true,
		CommandKey: "paired-resale-purchase-" + suffix, Date: now,
	})
	bought, err := lifecycle.PurchaseStarGift(ctx, purchase)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: seller.ID, Ref: domain.SavedStarGiftRef{Owner: sellerPeer, MsgID: bought.Saved.MsgID},
		RequirePrepaid: true, KeepOriginalDetails: true, CommandKey: "paired-resale-upgrade-" + suffix, Date: now + 1,
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	listed, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: seller.ID, Ref: domain.SavedStarGiftRef{Owner: sellerPeer, MsgID: upgraded.Saved.MsgID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, Date: now + 2,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if _, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: listed.Slug, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, FormID: 31001,
		CommandKey: "paired-resale-buy-" + suffix, Date: now + 3,
	}); err != nil {
		t.Fatalf("resale: %v", err)
	}

	// Проблема C: дебет покупателя и кредит продавца разделяют один публичный id.
	var buyerTxnID string
	var buyerAmount int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(transaction_id,''), amount FROM stars_transactions
WHERE user_id=$1 AND reason='gift_resale' AND amount < 0 ORDER BY id DESC LIMIT 1`, buyer.ID).
		Scan(&buyerTxnID, &buyerAmount); err != nil {
		t.Fatalf("buyer resale debit row: %v", err)
	}
	if buyerAmount != -500 || buyerTxnID == "" {
		t.Fatalf("buyer resale row = %d %q, want -500 with public id", buyerAmount, buyerTxnID)
	}
	var sellerTxnID string
	var sellerAmount int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(transaction_id,''), amount FROM stars_transactions
WHERE user_id=$1 AND reason='gift_resale' AND amount > 0 ORDER BY id DESC LIMIT 1`, seller.ID).
		Scan(&sellerTxnID, &sellerAmount); err != nil {
		t.Fatalf("seller resale credit row: %v", err)
	}
	if sellerTxnID != buyerTxnID {
		t.Fatalf("paired transaction ids differ: buyer %q seller %q", buyerTxnID, sellerTxnID)
	}
	if sellerAmount != 450 { // 90% proceeds permille от 500
		t.Fatalf("seller proceeds = %d, want 450 (900‰ of 500)", sellerAmount)
	}
}
