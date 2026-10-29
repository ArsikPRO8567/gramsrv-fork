package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// sold_out активной ревизии каталога обязан отражать фактическое исчерпание
// лимита редкого подарка: последняя покупка в той же транзакции выставляет
// флаг, дальнейшие покупки запрещены.
func TestStarGiftSoldOutRevisionFlagPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	buyer := createTestUser(t, ctx, users, "+1779"+suffix+"01", "SoldOutBuyer", "")
	buyerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID}

	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, buyer.ID, 100000, now); err != nil {
		t.Fatalf("grant stars: %v", err)
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "LimitedGift " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Limited: true, AvailabilityRemains: 2, AvailabilityTotal: 2,
		Document: collectibleTestDocument(baseDocumentID, "limited.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "limited"), Animation: collectibleTestAnimation("limited.tgs"),
		Actor: "integration", CommandID: "soldout-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create limited catalog: %v", err)
	}
	catalogGift, found, err := gifts.CatalogRevision(ctx, entry.Gift.RevisionID)
	if err != nil || !found || !catalogGift.Limited || catalogGift.SoldOut {
		t.Fatalf("initial catalog state = %+v found=%v err=%v, want limited and not sold out", catalogGift, found, err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))

	revisionSoldOut := func() bool {
		t.Helper()
		var soldOut bool
		if err := pool.QueryRow(ctx, `SELECT r.sold_out FROM star_gift_catalog_revisions r
JOIN star_gift_catalog c ON c.active_revision_id = r.id WHERE c.gift_id=$1`, entry.Gift.ID).Scan(&soldOut); err != nil {
			t.Fatalf("load revision sold_out: %v", err)
		}
		return soldOut
	}

	purchase := func(index int) error {
		t.Helper()
		req := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
			BuyerUserID: buyer.ID, To: buyerPeer, GiftID: entry.Gift.ID,
			CommandKey: "soldout-purchase-" + suffix + "-" + string(rune('a'+index)), Date: now + index,
		})
		_, err := lifecycle.PurchaseStarGift(ctx, req)
		return err
	}

	// Первая покупка: остаток 2 -> 1, sold_out ещё не выставлен.
	if err := purchase(0); err != nil {
		t.Fatalf("purchase 0: %v", err)
	}
	var remains int
	if err := pool.QueryRow(ctx, `SELECT availability_remains FROM star_gift_catalog WHERE gift_id=$1`, entry.Gift.ID).Scan(&remains); err != nil {
		t.Fatalf("load remains: %v", err)
	}
	if remains != 1 {
		t.Fatalf("availability_remains after first purchase = %d, want 1", remains)
	}
	if revisionSoldOut() {
		t.Fatalf("revision sold_out flipped before supply exhausted (remains=%d)", remains)
	}
	// Вторая покупка исчерпывает лимит: sold_out=true фиксируется той же
	// транзакцией, что и обнуление остатка.
	if err := purchase(1); err != nil {
		t.Fatalf("purchase 1: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT availability_remains FROM star_gift_catalog WHERE gift_id=$1`, entry.Gift.ID).Scan(&remains); err != nil {
		t.Fatalf("load remains 2: %v", err)
	}
	if remains != 0 {
		t.Fatalf("availability_remains after second purchase = %d, want 0", remains)
	}
	if !revisionSoldOut() {
		t.Fatalf("sold_out flag not set on active revision after supply exhausted")
	}
	// Третья покупка запрещена: и остатком, и терминальным флагом.
	if err := purchase(2); !errors.Is(err, domain.ErrStarGiftInvalid) && !errors.Is(err, domain.ErrStarGiftUnavailable) {
		t.Fatalf("purchase after exhaustion err = %v, want unavailable/invalid", err)
	}
}

// theme_available уникального подарка выводится из активного флага
// peer_color_available ревизии каталога; палитра сообщений выводится из фона
// и узора коллекции; theme_peer фиксирует установку темы чата.
func TestStarGiftThemeAvailableFromPeerColorRevisionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1780"+suffix+"01", "ThemeOwner", "")
	other := createTestUser(t, ctx, users, "+1780"+suffix+"02", "ThemeOther", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	stars := NewStarsStore(pool)
	for _, user := range []domain.User{owner, other} {
		if _, _, err := stars.EnsureGrant(ctx, user.ID, 100000, now); err != nil {
			t.Fatalf("grant stars: %v", err)
		}
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "PaletteGift " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		PeerColorAvailable: true,
		Document:           collectibleTestDocument(baseDocumentID, "palette.tgs"),
		Blob:               collectibleTestBlob(baseDocumentID, "palette"), Animation: collectibleTestAnimation("palette.tgs"),
		Actor: "integration", CommandID: "theme-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create palette catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "theme-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+14, "model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+14, "model-two"), Animation: collectibleTestAnimationPtr("model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+2, "pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Rings", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+15, "pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+15, "pattern-two"), Animation: collectibleTestAnimationPtr("pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 71, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 73, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "theme-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish theme pool: %v", err)
	}

	// Контроль: подарок без peer_color_available не наследует theme_available.
	plainEntry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "PlainGift " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID+10, "plain.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID+10, "plain"), Animation: collectibleTestAnimation("plain.tgs"),
		Actor: "integration", CommandID: "plain-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create plain catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: plainEntry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "plain-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+11, "plain-model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+11, "plain-model"), Animation: collectibleTestAnimationPtr("plain-model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+13, "plain-model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+13, "plain-model-two"), Animation: collectibleTestAnimationPtr("plain-model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+12, "plain-pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+12, "plain-pattern"), Animation: collectibleTestAnimationPtr("plain-pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Rings", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+16, "plain-pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+16, "plain-pattern-two"), Animation: collectibleTestAnimationPtr("plain-pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 74, CenterColor: 0x445566, EdgeColor: 0x556677, PatternColor: 0x667788, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 75, CenterColor: 0x8899aa, EdgeColor: 0x99aabb, PatternColor: 0xccddee, TextColor: 0x222222, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "plain-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish plain pool: %v", err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 0,
	}))

	purchaseAndUpgrade := func(giftID int64, tag string) domain.StarGiftUpgradeResult {
		t.Helper()
		purchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
			BuyerUserID: owner.ID, To: ownerPeer, GiftID: giftID,
			CommandKey: "theme-purchase-" + tag + "-" + suffix, Date: now + 10,
		})
		purchased, err := lifecycle.PurchaseStarGift(ctx, purchaseReq)
		if err != nil {
			t.Fatalf("purchase %s: %v", tag, err)
		}
		upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
			UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: purchased.Saved.MsgID},
			ChargeStars: 100, FormID: int64(93200), CommandKey: "theme-upgrade-" + tag + "-" + suffix,
			Date: now + 11,
		})
		if err != nil {
			t.Fatalf("upgrade %s: %v", tag, err)
		}
		return upgraded
	}

	upgraded := purchaseAndUpgrade(entry.Gift.ID, "palette")
	if !upgraded.Unique.ThemeAvailable {
		t.Fatalf("palette unique lost theme_available: %+v", upgraded.Unique)
	}
	plainUpgraded := purchaseAndUpgrade(plainEntry.Gift.ID, "plain")
	if plainUpgraded.Unique.ThemeAvailable {
		t.Fatalf("plain unique must not carry theme_available: %+v", plainUpgraded.Unique)
	}

	// Палитра сообщений выводится из фона и узора коллекции (атрибуты
	// выбираются случайно, поэтому проверяем самосогласованность с фоном).
	// Официальный сервер отдаёт три цвета: [центр, край, text_color].
	backdrop := upgraded.Unique.Backdrop
	palette := domain.PeerColorCollectibleFromGift(upgraded.Unique)
	if !palette.Valid() || palette.CollectibleID != upgraded.Unique.ID ||
		palette.AccentColor != backdrop.CenterColor || len(palette.Colors) != 3 ||
		palette.Colors[0] != backdrop.CenterColor || palette.Colors[1] != backdrop.EdgeColor ||
		palette.Colors[2] != backdrop.TextColor ||
		palette.GiftEmojiID != upgraded.Unique.Model.Document.ID ||
		palette.BackgroundEmojiID != upgraded.Unique.Pattern.Document.ID {
		t.Fatalf("derived palette = %+v, backdrop = %+v", palette, backdrop)
	}

	// getUniqueGiftChatThemes: только theme_available-коллекции владельца.
	themeGifts, err := gifts.ListUniqueThemeAvailable(ctx, owner.ID, 0)
	if err != nil {
		t.Fatalf("list theme available: %v", err)
	}
	foundTheme := false
	for _, gift := range themeGifts {
		if gift.ID == upgraded.Unique.ID {
			foundTheme = true
		}
		if gift.ID == plainUpgraded.Unique.ID {
			t.Fatalf("plain unique leaked into theme list: %+v", gift)
		}
	}
	if !foundTheme {
		t.Fatalf("palette unique missing from theme list: %+v", themeGifts)
	}
	otherThemes, err := gifts.ListUniqueThemeAvailable(ctx, other.ID, 0)
	if err != nil || len(otherThemes) != 0 {
		t.Fatalf("stranger theme list = %+v err=%v, want empty", otherThemes, err)
	}

	// theme_peer: установка темы чата фиксируется и переносится.
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: other.ID}
	if err := gifts.SetUniqueGiftThemePeer(ctx, upgraded.Unique.ID, peer); err != nil {
		t.Fatalf("set theme peer: %v", err)
	}
	stored, found, err := gifts.UniqueByID(ctx, upgraded.Unique.ID)
	if err != nil || !found || stored.ThemePeer != peer {
		t.Fatalf("theme peer not recorded: found=%v err=%v gift=%+v", found, err, stored)
	}
	if err := gifts.SetUniqueGiftThemePeer(ctx, plainUpgraded.Unique.ID, peer); err != nil {
		t.Fatalf("move theme peer: %v", err)
	}
	cleared, _, err := gifts.UniqueByID(ctx, upgraded.Unique.ID)
	if err != nil || cleared.ThemePeer.ID != 0 {
		t.Fatalf("old theme peer not cleared: %+v err=%v", cleared, err)
	}
	if err := gifts.SetUniqueGiftThemePeer(ctx, 0, peer); err != nil {
		t.Fatalf("clear theme peer: %v", err)
	}
	plainStored, _, err := gifts.UniqueByID(ctx, plainUpgraded.Unique.ID)
	if err != nil || plainStored.ThemePeer.ID != 0 {
		t.Fatalf("theme peer not cleared by reset: %+v err=%v", plainStored, err)
	}

	// Палитра сообщений пользователя: снапшот сохраняется и читается назад.
	usersStore := NewUserStore(pool)
	if _, err := usersStore.UpdateColor(ctx, owner.ID, false, domain.PeerColor{Collectible: palette}); err != nil {
		t.Fatalf("update color with collectible: %v", err)
	}
	reloaded, found, err := usersStore.ByID(ctx, owner.ID)
	if err != nil || !found {
		t.Fatalf("reload user: found=%v err=%v", found, err)
	}
	if reloaded.Color.HasColor || reloaded.Color.Collectible.CollectibleID != palette.CollectibleID ||
		reloaded.Color.Collectible.GiftEmojiID != palette.GiftEmojiID ||
		reloaded.Color.Collectible.BackgroundEmojiID != palette.BackgroundEmojiID ||
		reloaded.Color.Collectible.AccentColor != palette.AccentColor ||
		len(reloaded.Color.Collectible.Colors) != len(palette.Colors) {
		t.Fatalf("persisted palette = %+v, want %+v and plain color unset", reloaded.Color, palette)
	}
	for i, v := range palette.Colors {
		if reloaded.Color.Collectible.Colors[i] != v {
			t.Fatalf("persisted palette colors = %+v, want %+v", reloaded.Color.Collectible.Colors, palette.Colors)
		}
	}
	// Обычная палитра снимает коллекционную (взаимоисключающе).
	if _, err := usersStore.UpdateColor(ctx, owner.ID, false, domain.PeerColor{HasColor: true, Color: 4}); err != nil {
		t.Fatalf("update color plain: %v", err)
	}
	reloaded, _, err = usersStore.ByID(ctx, owner.ID)
	if err != nil {
		t.Fatalf("reload user after plain color: %v", err)
	}
	if !reloaded.Color.Collectible.Empty() || !reloaded.Color.HasColor || reloaded.Color.Color != 4 {
		t.Fatalf("plain color must clear collectible palette: %+v", reloaded.Color)
	}
	// Профильная палитра коллекцией запрещена (официальное правило).
	if _, err := usersStore.UpdateColor(ctx, owner.ID, true, domain.PeerColor{Collectible: palette}); !errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
		t.Fatalf("profile collectible palette err = %v, want ErrStarGiftCollectibleInvalid", err)
	}
}
