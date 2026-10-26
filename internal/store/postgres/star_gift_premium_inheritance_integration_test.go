package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// require_premium каталога должен наследоваться выпущенной коллекцией, а покупка
// такой коллекции (маркет и офферы) доступна только Premium-аккаунтам.
func TestStarGiftRequirePremiumCollectiblePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1882"+suffix+"01", "PremiumOwner", "")
	plainBuyer := createTestUser(t, ctx, users, "+1882"+suffix+"02", "PlainBuyer", "")
	premiumBuyer := createTestUser(t, ctx, users, "+1882"+suffix+"03", "PremiumBuyer", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	plainBuyerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: plainBuyer.ID}

	stars := NewStarsStore(pool)
	for _, user := range []domain.User{owner, plainBuyer, premiumBuyer} {
		if _, _, err := stars.EnsureGrant(ctx, user.ID, 10000, now); err != nil {
			t.Fatalf("grant stars to %d: %v", user.ID, err)
		}
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "PremiumGift " + suffix, Stars: 50, ConvertStars: 20, Enabled: true, RequirePremium: true,
		Document: collectibleTestDocument(baseDocumentID, "premium.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "premium"), Animation: collectibleTestAnimation("premium.tgs"),
		Actor: "integration", CommandID: "premium-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create premium catalog: %v", err)
	}
	catalogGift, found, err := gifts.CatalogRevision(ctx, entry.Gift.RevisionID)
	if err != nil || !found || !catalogGift.RequirePremium {
		t.Fatalf("catalog revision lost require_premium: %+v found=%v err=%v", catalogGift, found, err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "prem-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+2, "model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "model-two"), Animation: collectibleTestAnimationPtr("model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+3, "pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+4, "pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+4, "pattern-two"), Animation: collectibleTestAnimationPtr("pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 77, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 78, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "premium-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish premium pool: %v", err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 0,
	}))

	// Покупка обычного подарка с require_premium без Premium запрещена.
	plainPurchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
		BuyerUserID: plainBuyer.ID, To: plainBuyerPeer, GiftID: entry.Gift.ID,
		CommandKey: "plain-purchase-" + suffix, Date: now,
	})
	if _, err := lifecycle.PurchaseStarGift(ctx, plainPurchaseReq); !errors.Is(err, domain.ErrPremiumRequired) {
		t.Fatalf("non-premium regular purchase err = %v, want ErrPremiumRequired", err)
	}

	upgradeUnique := func(index int, msgDate int) domain.StarGiftUpgradeResult {
		t.Helper()
		purchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
			BuyerUserID: owner.ID, BuyerPremium: true, To: ownerPeer, GiftID: entry.Gift.ID,
			CommandKey: "premium-purchase-" + suffix + "-" + string(rune('a'+index)), Date: msgDate,
		})
		purchased, err := lifecycle.PurchaseStarGift(ctx, purchaseReq)
		if err != nil {
			t.Fatalf("premium purchase %d: %v", index, err)
		}
		upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
			UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: purchased.Saved.MsgID},
			ChargeStars: 100, FormID: int64(93100 + index), CommandKey: "premium-upgrade-" + suffix + "-" + string(rune('a'+index)),
			Date: msgDate + 1,
		})
		if err != nil {
			t.Fatalf("upgrade %d: %v", index, err)
		}
		// Наследование: коллекция обязана получить require_premium исходного подарка.
		if !upgraded.Unique.RequirePremium {
			t.Fatalf("upgraded unique %d lost require_premium: %+v", index, upgraded.Unique)
		}
		return upgraded
	}
	first := upgradeUnique(0, now+10)
	second := upgradeUnique(1, now+20)

	// Оффер на коллекцию с require_premium без Premium запрещён.
	_, err = lifecycle.SendStarGiftOffer(ctx, domain.StarGiftOfferRequest{
		BuyerUserID: plainBuyer.ID, Owner: ownerPeer, Slug: first.Unique.Slug,
		Price:    domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 300},
		Duration: 120, RandomID: 93150, Date: now + 30,
	})
	if !errors.Is(err, domain.ErrPremiumRequired) {
		t.Fatalf("non-premium offer err = %v, want ErrPremiumRequired", err)
	}

	// Premium-покупатель может отправить оффер, владелец принимает.
	acceptedOffer, err := lifecycle.SendStarGiftOffer(ctx, domain.StarGiftOfferRequest{
		BuyerUserID: premiumBuyer.ID, BuyerPremium: true, Owner: ownerPeer, Slug: first.Unique.Slug,
		Price:    domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 300},
		Duration: 120, RandomID: 93151, Date: now + 31,
	})
	if err != nil {
		t.Fatalf("premium offer: %v", err)
	}
	accepted, err := lifecycle.ResolveStarGiftOffer(ctx, domain.StarGiftResolveOfferRequest{
		OwnerUserID: owner.ID, OfferMsgID: acceptedOffer.Offer.OfferMsgID, Date: now + 32,
	})
	if err != nil || accepted.Offer.Status != "accepted" || accepted.Unique.Owner.ID != premiumBuyer.ID {
		t.Fatalf("accept premium offer = %+v err %v", accepted, err)
	}

	// Маркет: без Premium покупка коллекции запрещена, с Premium — разрешена.
	listed, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: second.Saved.MsgID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, Date: now + 40,
	})
	if err != nil || listed.ResellAmount == nil {
		t.Fatalf("listing = %+v err %v", listed, err)
	}
	if _, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: plainBuyer.ID, Slug: listed.Slug, To: plainBuyerPeer,
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, FormID: 93160,
		CommandKey: "premium-resale-plain-" + suffix, Date: now + 41,
	}); !errors.Is(err, domain.ErrPremiumRequired) {
		t.Fatalf("non-premium resale err = %v, want ErrPremiumRequired", err)
	}
	resold, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: premiumBuyer.ID, BuyerPremium: true, Slug: listed.Slug, To: domain.Peer{Type: domain.PeerTypeUser, ID: premiumBuyer.ID},
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, FormID: 93161,
		CommandKey: "premium-resale-ok-" + suffix, Date: now + 42,
	})
	if err != nil || resold.Unique.Owner.ID != premiumBuyer.ID {
		t.Fatalf("premium resale = %+v err %v", resold, err)
	}
	if !resold.Unique.RequirePremium {
		t.Fatalf("resold unique lost require_premium: %+v", resold.Unique)
	}
}
