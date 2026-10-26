package postgres

import (
	"context"
	"slices"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// Четыре непересекающиеся категории подарков в профиле на реальном SQL:
// обычные (бесконечные) / редкие без улучшения / редкие с доступным улучшением /
// уникальные. Клиент комбинирует exclude-флаги payments.getSavedStarGifts.
func TestStarGiftProfileCategoryFiltersPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1883"+suffix+"01", "FilterOwner", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 10000, now); err != nil {
		t.Fatalf("grant stars: %v", err)
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	documentBase := func(index int64) int64 { return baseDocumentID + index*100 }
	poolAttributes := func(prefix string, docBase int64) domain.StarGiftCollectibleWrite {
		return domain.StarGiftCollectibleWrite{
			UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: prefix + suffix,
			Models: []domain.StarGiftCollectibleAttribute{
				{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
					Document: collectibleTestDocumentPtr(docBase+1, prefix+"-model.tgs"), Blob: collectibleTestBlobPtr(docBase+1, prefix+"-model"), Animation: collectibleTestAnimationPtr(prefix + "-model.tgs")},
				{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
					Document: collectibleTestDocumentPtr(docBase+2, prefix+"-model-two.tgs"), Blob: collectibleTestBlobPtr(docBase+2, prefix+"-model-two"), Animation: collectibleTestAnimationPtr(prefix + "-model-two.tgs")},
			},
			Patterns: []domain.StarGiftCollectibleAttribute{
				{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
					Document: collectibleTestPatternDocumentPtr(docBase+3, prefix+"-pattern.tgs"), Blob: collectibleTestBlobPtr(docBase+3, prefix+"-pattern"), Animation: collectibleTestAnimationPtr(prefix + "-pattern.tgs")},
				{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
					Document: collectibleTestPatternDocumentPtr(docBase+4, prefix+"-pattern-two.tgs"), Blob: collectibleTestBlobPtr(docBase+4, prefix+"-pattern-two"), Animation: collectibleTestAnimationPtr(prefix + "-pattern-two.tgs")},
			},
			Backdrops: []domain.StarGiftCollectibleAttribute{
				{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 77, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
				{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 78, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			},
			Actor: "integration", CommandID: prefix + "-pool",
		}
	}
	catalog := func(title string, docBase int64, limited bool) domain.StarGiftCatalogEntry {
		entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
			Title: title + " " + suffix, Stars: 50, ConvertStars: 20, Enabled: true, Limited: limited,
			AvailabilityTotal:   map[bool]int{true: 1000, false: 0}[limited],
			AvailabilityRemains: map[bool]int{true: 1000, false: 0}[limited],
			Document:            collectibleTestDocument(docBase, title+".tgs"),
			Blob:                collectibleTestBlob(docBase, title), Animation: collectibleTestAnimation(title + ".tgs"),
			Actor: "integration", CommandID: "filter-catalog-" + title + "-" + suffix,
		})
		if err != nil {
			t.Fatalf("create catalog %s: %v", title, err)
		}
		return entry
	}
	unlimitedEntry := catalog("filtU", documentBase(1), false)   // бесконечный, с пулом улучшений
	limitedEntry := catalog("filtL", documentBase(2), true)      // ограниченный, с пулом улучшений
	limitedPlainEntry := catalog("filtP", documentBase(3), true) // ограниченный, без пула (не улучшаемый)
	if _, err := gifts.PublishCollectibleRevision(ctx, withCollectibleGift(poolAttributes("filtu", documentBase(1)), unlimitedEntry.Gift.ID)); err != nil {
		t.Fatalf("publish unlimited pool: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, withCollectibleGift(poolAttributes("filtl", documentBase(2)), limitedEntry.Gift.ID)); err != nil {
		t.Fatalf("publish limited pool: %v", err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 0,
	}))

	purchase := func(giftID int64, index int, date int) domain.SavedStarGift {
		t.Helper()
		req := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
			BuyerUserID: owner.ID, To: ownerPeer, GiftID: giftID,
			CommandKey: "filter-purchase-" + suffix + "-" + string(rune('a'+index)), Date: date,
		})
		purchased, err := lifecycle.PurchaseStarGift(ctx, req)
		if err != nil {
			t.Fatalf("purchase %d: %v", giftID, err)
		}
		return purchased.Saved
	}
	unlimitedGift := purchase(unlimitedEntry.Gift.ID, 0, now+1)
	limitedUpgradableGift := purchase(limitedEntry.Gift.ID, 1, now+2)
	limitedUpgradableSecond := purchase(limitedEntry.Gift.ID, 3, now+3)
	limitedPlainGift := purchase(limitedPlainEntry.Gift.ID, 2, now+4)

	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: limitedUpgradableGift.MsgID},
		ChargeStars: 100, FormID: 93400, CommandKey: "filter-upgrade-" + suffix, Date: now + 4,
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	uniqueGiftID := upgraded.Saved.ID

	expect := func(name string, filter domain.SavedStarGiftFilter, want []int64) {
		t.Helper()
		filter.Owner = ownerPeer
		filter.Limit = 100
		page, err := gifts.ListByOwnerFiltered(ctx, filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := make([]int64, 0, len(page.Gifts))
		for _, gift := range page.Gifts {
			got = append(got, gift.ID)
		}
		slices.Sort(got)
		wantSorted := append([]int64(nil), want...)
		slices.Sort(wantSorted)
		if !slices.Equal(got, wantSorted) {
			t.Fatalf("%s: got %v, want %v", name, got, wantSorted)
		}
		if page.Count != len(want) {
			t.Fatalf("%s: count = %d, want %d", name, page.Count, len(want))
		}
	}

	all := []int64{unlimitedGift.ID, limitedUpgradableGift.ID, limitedUpgradableSecond.ID, limitedPlainGift.ID}
	expect("all gifts", domain.SavedStarGiftFilter{}, all)
	// Обычные: только бесконечные — включая улучшаемые бесконечные подарки.
	expect("regular/unlimited", domain.SavedStarGiftFilter{ExcludeUnique: true, ExcludeUpgradable: true, ExcludeUnupgradable: true},
		[]int64{unlimitedGift.ID})
	// Редкие: ограниченные, не имеющие улучшения.
	expect("rare non-upgradable", domain.SavedStarGiftFilter{ExcludeUnlimited: true, ExcludeUnique: true, ExcludeUpgradable: true},
		[]int64{limitedPlainGift.ID})
	// Не улучшенные: ограниченные, имеющие улучшение.
	expect("rare upgradable", domain.SavedStarGiftFilter{ExcludeUnlimited: true, ExcludeUnique: true, ExcludeUnupgradable: true},
		[]int64{limitedUpgradableSecond.ID})
	// Уникальные: только улучшенные.
	expect("unique", domain.SavedStarGiftFilter{ExcludeUnlimited: true, ExcludeUpgradable: true, ExcludeUnupgradable: true},
		[]int64{uniqueGiftID})
	// Промежуточные комбинации остаются осмысленными.
	expect("all rare", domain.SavedStarGiftFilter{ExcludeUnlimited: true, ExcludeUnique: true},
		[]int64{limitedUpgradableSecond.ID, limitedPlainGift.ID})
	expect("non-rare", domain.SavedStarGiftFilter{ExcludeUpgradable: true, ExcludeUnupgradable: true},
		[]int64{unlimitedGift.ID, limitedUpgradableGift.ID})
	expect("exclude unlimited only", domain.SavedStarGiftFilter{ExcludeUnlimited: true},
		[]int64{limitedUpgradableSecond.ID, limitedPlainGift.ID, limitedUpgradableGift.ID})
}

func withCollectibleGift(write domain.StarGiftCollectibleWrite, giftID int64) domain.StarGiftCollectibleWrite {
	write.GiftID = giftID
	return write
}
