package memory

import (
	"context"
	"slices"
	"testing"

	"telesrv/internal/domain"
)

// Четыре непересекающиеся категории подарков в профиле и клиентские комбинации
// exclude-флагов payments.getSavedStarGifts: обычные (бесконечные), редкие без
// улучшения, редкие с доступным улучшением, уникальные.
func TestStarGiftProfileCategoryFilters(t *testing.T) {
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	store := NewStarGiftStore()
	store.SeedCatalog([]domain.StarGift{
		{ID: 8001, RevisionID: 9001, Stars: 50, ConvertStars: 25, Title: "UnlimitedUpgradable",
			UpgradeStars: 25, UpgradeTotal: 10, UpgradeIssued: 0},
		{ID: 8002, RevisionID: 9002, Stars: 100, ConvertStars: 50, Title: "LimitedUpgradable", Limited: true,
			AvailabilityTotal: 1000, UpgradeStars: 25, UpgradeTotal: 10, UpgradeIssued: 0},
		{ID: 8003, RevisionID: 9003, Stars: 100, ConvertStars: 50, Title: "LimitedNonUpgradeable", Limited: true,
			AvailabilityTotal: 1000},
	})

	create := func(giftID, revisionID int64, msgID int, uniqueGiftID int64) int64 {
		id, err := store.Create(ctx, domain.SavedStarGift{
			Owner: owner, GiftID: giftID, RevisionID: revisionID, MsgID: msgID,
			Date: 1700000000 + msgID, UniqueGiftID: uniqueGiftID,
		})
		if err != nil {
			t.Fatalf("create gift %d: %v", giftID, err)
		}
		return id
	}
	unlimitedID := create(8001, 9001, 101, 0)         // обычный (бесконечный)
	limitedUpgradableID := create(8002, 9002, 102, 0) // редкий с улучшением (не улучшенный)
	limitedPlainID := create(8003, 9003, 103, 0)      // редкий без улучшения
	uniqueID := create(8002, 9002, 104, 777)          // уникальный (улучшенный)

	expect := func(name string, filter domain.SavedStarGiftFilter, want []int64) {
		t.Helper()
		filter.Owner = owner
		filter.Limit = 100
		page, err := store.ListByOwnerFiltered(ctx, filter)
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

	all := []int64{unlimitedID, limitedUpgradableID, limitedPlainID, uniqueID}
	expect("all gifts", domain.SavedStarGiftFilter{}, all)
	// Обычные: только бесконечные.
	expect("regular/unlimited", domain.SavedStarGiftFilter{ExcludeUnique: true, ExcludeUpgradable: true, ExcludeUnupgradable: true},
		[]int64{unlimitedID})
	// Редкие: ограниченные, не имеющие улучшения.
	expect("rare non-upgradable", domain.SavedStarGiftFilter{ExcludeUnlimited: true, ExcludeUnique: true, ExcludeUpgradable: true},
		[]int64{limitedPlainID})
	// Не улучшенные: ограниченные, имеющие улучшение.
	expect("rare upgradable", domain.SavedStarGiftFilter{ExcludeUnlimited: true, ExcludeUnique: true, ExcludeUnupgradable: true},
		[]int64{limitedUpgradableID})
	// Уникальные: только улучшенные.
	expect("unique", domain.SavedStarGiftFilter{ExcludeUnlimited: true, ExcludeUpgradable: true, ExcludeUnupgradable: true},
		[]int64{uniqueID})
	// Промежуточные комбинации остаются осмысленными.
	expect("all rare", domain.SavedStarGiftFilter{ExcludeUnlimited: true, ExcludeUnique: true},
		[]int64{limitedUpgradableID, limitedPlainID})
	expect("non-rare", domain.SavedStarGiftFilter{ExcludeUpgradable: true, ExcludeUnupgradable: true},
		[]int64{unlimitedID, uniqueID})
	expect("exclude unlimited only", domain.SavedStarGiftFilter{ExcludeUnlimited: true},
		[]int64{limitedUpgradableID, limitedPlainID, uniqueID})
}
