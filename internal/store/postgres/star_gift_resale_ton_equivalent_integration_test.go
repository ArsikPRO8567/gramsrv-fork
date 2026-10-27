package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// Эквивалентные цены рынка: листинг в звёздах принимает обе валюты (TON по
// серверному курсу TELESRV_STARGIFT_RESELL_RATE_*), листинг в TON —
// resale_ton_only, оплата только нанотонами, а звёздная цена — серверный
// эквивалент для отображения. Продавец получает цену листинга в валюте
// листинга независимо от валюты оплаты.
func TestStarGiftResaleTonEquivalentPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	seller := createTestUser(t, ctx, users, "+1883"+suffix+"01", "TonSeller", "")
	buyer := createTestUser(t, ctx, users, "+1883"+suffix+"02", "TonBuyer", "")
	sellerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: seller.ID}
	buyerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID}

	stars := NewStarsStore(pool)
	for _, user := range []domain.User{seller, buyer} {
		if _, _, err := stars.EnsureGrant(ctx, user.ID, 10000, now); err != nil {
			t.Fatalf("grant stars to %d: %v", user.ID, err)
		}
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "TonGift " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "ton.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "ton"), Animation: collectibleTestAnimation("ton.tgs"),
		Actor: "integration", CommandID: "ton-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create ton catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "ton-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Ton Model", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+1, "ton-model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "ton-model"), Animation: collectibleTestAnimationPtr("ton-model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Ton Model Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+2, "ton-model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "ton-model-two"), Animation: collectibleTestAnimationPtr("ton-model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Ton Pattern", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+3, "ton-pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "ton-pattern"), Animation: collectibleTestAnimationPtr("ton-pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Ton Pattern Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+4, "ton-pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+4, "ton-pattern-two"), Animation: collectibleTestAnimationPtr("ton-pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Ton Night", BackdropID: 87, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Ton Day", BackdropID: 88, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "ton-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish ton pool: %v", err)
	}

	messages := NewMessageStore(pool)
	// Стартовый TON-грант 100 TON — чтобы хватило на TON-оплату обоих покупок.
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 100_000_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 0,
	}))

	upgrade := func(index int, msgDate int) domain.StarGiftUpgradeResult {
		t.Helper()
		purchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
			BuyerUserID: seller.ID, To: sellerPeer, GiftID: entry.Gift.ID,
			CommandKey: "ton-purchase-" + suffix + "-" + string(rune('a'+index)), Date: msgDate,
		})
		purchased, err := lifecycle.PurchaseStarGift(ctx, purchaseReq)
		if err != nil {
			t.Fatalf("purchase %d: %v", index, err)
		}
		upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
			UserID: seller.ID, Ref: domain.SavedStarGiftRef{Owner: sellerPeer, MsgID: purchased.Saved.MsgID},
			ChargeStars: 100, FormID: int64(93200 + index), CommandKey: "ton-upgrade-" + suffix + "-" + string(rune('a'+index)),
			Date: msgDate + 1,
		})
		if err != nil {
			t.Fatalf("upgrade %d: %v", index, err)
		}
		return upgraded
	}
	first := upgrade(0, now+10)
	second := upgrade(1, now+20)

	rate := domain.CurrentStarGiftExchangeRate()
	if !rate.Valid() {
		t.Fatalf("exchange rate must be valid: %+v", rate)
	}

	// --- Листинг в TON: resale_ton_only, оплата только нанотонами ---
	tonAmount := int64(2_000_000_000) // 2 TON
	listedTon, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: seller.ID, Ref: domain.SavedStarGiftRef{Owner: sellerPeer, MsgID: first.Saved.MsgID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: tonAmount}, Date: now + 40,
	})
	if err != nil {
		t.Fatalf("ton listing: %v", err)
	}
	if !listedTon.ResaleTonOnly {
		t.Fatalf("ton listing must set resale_ton_only: %+v", listedTon)
	}
	if listedTon.ResellTon != tonAmount || listedTon.ResellStars != rate.StarsForTon(tonAmount) {
		t.Fatalf("ton listing prices = %d/%d, want %d/%d", listedTon.ResellStars, listedTon.ResellTon, rate.StarsForTon(tonAmount), tonAmount)
	}
	// Оплата звёздами по TON-листингу запрещена.
	if _, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: listedTon.Slug, To: buyerPeer,
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: listedTon.ResellStars},
		FormID: 93210, CommandKey: "ton-listing-stars-" + suffix, Date: now + 41,
	}); !errors.Is(err, domain.ErrStarGiftResaleUnavailable) {
		t.Fatalf("stars payment on ton listing err = %v, want ErrStarGiftResaleUnavailable", err)
	}
	boughtTon, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: listedTon.Slug, To: buyerPeer,
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: tonAmount},
		FormID: 93211, CommandKey: "ton-listing-ton-" + suffix, Date: now + 42,
	})
	if err != nil || boughtTon.Unique.Owner.ID != buyer.ID {
		t.Fatalf("ton resale = %+v err %v", boughtTon, err)
	}
	if boughtTon.Unique.ResellAmount != nil {
		t.Fatalf("listing must be removed after sale: %+v", boughtTon.Unique)
	}
	// Продавец получил TON-поступление: 2 TON * 900/1000 = 1.8 TON.
	var sellerTon int64
	if err := pool.QueryRow(ctx, `SELECT balance_nanoton FROM ton_balances WHERE user_id=$1`, seller.ID).Scan(&sellerTon); err != nil {
		t.Fatalf("seller ton balance: %v", err)
	}
	if want := tonAmount / 1000 * 900; sellerTon != want {
		t.Fatalf("seller ton proceeds = %d, want %d", sellerTon, want)
	}

	// --- Листинг в звёздах: обе валюты, TON по серверному эквиваленту ---
	starsPrice := int64(80)
	listedStars, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: seller.ID, Ref: domain.SavedStarGiftRef{Owner: sellerPeer, MsgID: second.Saved.MsgID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: starsPrice}, Date: now + 50,
	})
	if err != nil {
		t.Fatalf("stars listing: %v", err)
	}
	if listedStars.ResaleTonOnly {
		t.Fatalf("stars listing must clear resale_ton_only: %+v", listedStars)
	}
	tonPrice := rate.TonForStars(starsPrice)
	if listedStars.ResellStars != starsPrice || listedStars.ResellTon != tonPrice {
		t.Fatalf("stars listing prices = %d/%d, want %d/%d", listedStars.ResellStars, listedStars.ResellTon, starsPrice, tonPrice)
	}
	var sellerStarsBefore int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, seller.ID).Scan(&sellerStarsBefore); err != nil {
		t.Fatalf("seller stars balance before: %v", err)
	}
	// Покупатель платит серверным TON-эквивалентом звёздного листинга.
	boughtStars, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: listedStars.Slug, To: buyerPeer,
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: tonPrice},
		FormID: 93212, CommandKey: "stars-listing-ton-" + suffix, Date: now + 51,
	})
	if err != nil || boughtStars.Unique.Owner.ID != buyer.ID {
		t.Fatalf("ton payment on stars listing = %+v err %v", boughtStars, err)
	}
	// Продавец получает цену листинга в звёздах: 80 * 900/1000 = 72.
	var sellerStarsAfter int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, seller.ID).Scan(&sellerStarsAfter); err != nil {
		t.Fatalf("seller stars balance after: %v", err)
	}
	if want := starsPrice * 900 / 1000; sellerStarsAfter-sellerStarsBefore != want {
		t.Fatalf("seller stars proceeds = %d, want %d", sellerStarsAfter-sellerStarsBefore, want)
	}
	// Покупатель: стартовый грант 100 TON минус обе TON-оплаты.
	var buyerTon int64
	if err := pool.QueryRow(ctx, `SELECT balance_nanoton FROM ton_balances WHERE user_id=$1`, buyer.ID).Scan(&buyerTon); err != nil {
		t.Fatalf("buyer ton balance: %v", err)
	}
	if want := int64(100_000_000_000) - tonAmount - tonPrice; buyerTon != want {
		t.Fatalf("buyer ton balance = %d, want %d", buyerTon, want)
	}
	// Неверный TON-эквивалент не принимается.
	third := upgrade(2, now+60)
	if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: seller.ID, Ref: domain.SavedStarGiftRef{Owner: sellerPeer, MsgID: third.Saved.MsgID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: starsPrice}, Date: now + 61,
	}); err != nil {
		t.Fatalf("relist third: %v", err)
	}
	if _, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: third.Unique.Slug, To: buyerPeer,
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: tonPrice + 1},
		FormID: 93213, CommandKey: "stars-listing-ton-bad-" + suffix, Date: now + 62,
	}); !errors.Is(err, domain.ErrStarGiftResaleUnavailable) {
		t.Fatalf("wrong ton equivalent err = %v, want ErrStarGiftResaleUnavailable", err)
	}
}
