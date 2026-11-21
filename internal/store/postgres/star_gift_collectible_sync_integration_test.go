package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestCollectibleRevisionSyncInPlace (раунд 37) — правка опубликованной
// ревизии «на месте»: та же строка ревизии, те же id строк атрибутов (ссылки
// выпущенных подарков продолжают работать), добавление и снятие строк,
// защита задействованных атрибутов.
func TestCollectibleRevisionSyncInPlace(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1779"+suffix+"41", "SyncSender", "")
	owner := createTestUser(t, ctx, users, "+1779"+suffix+"42", "SyncOwner", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, owner.ID})
	})

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "SyncGift", Stars: 50, ConvertStars: 25, Enabled: true,
		Document:  collectibleTestDocument(baseDocumentID, "gift.tgs"),
		Blob:      collectibleTestBlob(baseDocumentID, "gift"),
		Animation: collectibleTestAnimation("gift.tgs"),
		Actor:     "integration", CommandID: "catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog gift: %v", err)
	}
	revision, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 10, SlugPrefix: "sync-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Aurora", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Solar", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestDocumentPtr(baseDocumentID+2, "model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "model-two"), Animation: collectibleTestAnimationPtr("model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+3, "pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Rings", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+4, "pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+4, "pattern-two"), Animation: collectibleTestAnimationPtr("pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Midnight", BackdropID: 1, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 999},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Daylight", BackdropID: 2, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1},
		},
		Actor: "integration", CommandID: "collectibles-" + suffix,
	})
	if err != nil {
		t.Fatalf("publish collectible pool: %v", err)
	}
	modelIDs := map[string]int64{}
	for _, model := range revision.Models {
		modelIDs[model.Name] = model.ID
	}
	patternIDs := map[string]int64{}
	for _, pattern := range revision.Patterns {
		patternIDs[pattern.Name] = pattern.ID
	}
	if modelIDs["Aurora"] == 0 || modelIDs["Solar"] == 0 || patternIDs["Orbit"] == 0 || patternIDs["Rings"] == 0 {
		t.Fatalf("published attribute ids missing: %+v / %+v", modelIDs, patternIDs)
	}

	// Выпускаем подарок: он ссылается на строки атрибутов составным FK.
	messages := NewMessageStore(pool)
	saved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: 1700001000, ConvertStars: 25, Message: "original",
	})
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, 1700001001); err != nil {
		t.Fatalf("grant upgrade stars: %v", err)
	}
	upgrades := NewStarGiftUpgradeStore(pool, messages)
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved.MsgID},
		KeepOriginalDetails: true, ChargeStars: 100, FormID: 992,
		CommandKey: "paid-" + suffix, Date: 1700001002,
	})
	if err != nil || upgraded.Unique.Num != 1 {
		t.Fatalf("upgrade star gift: %+v err %v", upgraded, err)
	}
	// Апгрейд выбирает атрибуты взвешенно-случайно: фиксируем фактический
	// выбор и правим именно эти строки (инвариант — ссылки сохраняются).
	issuedModelID := upgraded.Unique.Model.ID
	issuedPatternID := upgraded.Unique.Pattern.ID
	if modelIDs["Aurora"] != issuedModelID && modelIDs["Solar"] != issuedModelID ||
		patternIDs["Orbit"] != issuedPatternID && patternIDs["Rings"] != issuedPatternID {
		t.Fatalf("issued gift attributes = model %d / pattern %d outside the published pool",
			issuedModelID, issuedPatternID)
	}

	// Правка «на месте»: Aurora переименовывается (та же строка), Solar
	// снимается (нет ссылок), добавляется новая модель Nova; фон Daylight
	// перекрашивается. Снимать задействованную Aurora нельзя.
	active, found, err := gifts.ActiveCollectibleRevision(ctx, entry.Gift.ID)
	if err != nil || !found {
		t.Fatalf("active revision = found %v err %v", found, err)
	}
	syncModels := make([]domain.StarGiftCollectibleAttribute, 0, len(active.Models)+1)
	for _, model := range active.Models {
		row := model
		if model.ID == issuedModelID {
			row.Name = "Aurora Renamed"
			row.RarityPermille = 620
		} else {
			// Незадействованная строка снимается.
			continue
		}
		syncModels = append(syncModels, row)
	}
	syncModels = append(syncModels, domain.StarGiftCollectibleAttribute{
		Kind: domain.StarGiftCollectibleModel, Name: "Nova", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 380,
		Document: collectibleTestDocumentPtr(baseDocumentID+5, "model-nova.tgs"),
		Blob:     collectibleTestBlobPtr(baseDocumentID+5, "model-nova"), Animation: collectibleTestAnimationPtr("model-nova.tgs"),
	})
	syncPatterns := append([]domain.StarGiftCollectibleAttribute(nil), active.Patterns...)
	syncBackdrops := make([]domain.StarGiftCollectibleAttribute, 0, len(active.Backdrops))
	for _, backdrop := range active.Backdrops {
		row := backdrop
		if backdrop.Name == "Daylight" {
			row.CenterColor = 0xffcc00
			row.EdgeColor = 0x886600
		}
		syncBackdrops = append(syncBackdrops, row)
	}
	synced, err := gifts.SyncActiveCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 120, SupplyTotal: 12, SlugPrefix: "sync-" + suffix,
		Models: syncModels, Patterns: syncPatterns, Backdrops: syncBackdrops,
		Actor: "integration", CommandID: "sync-" + suffix,
	})
	if err != nil {
		t.Fatalf("sync active revision: %v", err)
	}
	if synced.ID != revision.ID || synced.Revision != revision.Revision || synced.Issued != 1 {
		t.Fatalf("sync changed revision identity: id %d/%d rev %d/%d issued %d",
			synced.ID, revision.ID, synced.Revision, revision.Revision, synced.Issued)
	}
	if synced.UpgradeStars != 120 || synced.SupplyTotal != 12 {
		t.Fatalf("sync revision fields = %+v", synced)
	}
	syncedIDs := map[string]int64{}
	for _, model := range synced.Models {
		syncedIDs[model.Name] = model.ID
	}
	if syncedIDs["Aurora Renamed"] != issuedModelID {
		t.Fatalf("renamed model kept id = %d, want %d", syncedIDs["Aurora Renamed"], issuedModelID)
	}
	if _, removed := syncedIDs["Solar"]; removed {
		t.Fatalf("unreferenced model was not removed")
	}
	if syncedIDs["Nova"] == 0 {
		t.Fatalf("new model Nova was not inserted")
	}
	for _, backdrop := range synced.Backdrops {
		if backdrop.Name == "Daylight" && (backdrop.CenterColor != 0xffcc00 || backdrop.EdgeColor != 0x886600) {
			t.Fatalf("backdrop colors not updated: %+v", backdrop)
		}
		if backdrop.Name == "Midnight" && backdrop.ID == 0 {
			t.Fatalf("kept backdrop lost id")
		}
	}
	// Выпущенный подарок продолжает ссылаться на ту же строку и видит новое
	// имя/редкость (правка применилась к существующей строке).
	var issuedNameAfterSync string
	var issuedIDAfterSync int64
	if err := pool.QueryRow(ctx, `SELECT m.id, m.name FROM unique_star_gifts u
JOIN star_gift_collectible_models m ON m.id=u.model_attribute_id AND m.collectible_revision_id=u.collectible_revision_id
WHERE u.id=$1`, upgraded.Unique.ID).Scan(&issuedIDAfterSync, &issuedNameAfterSync); err != nil {
		t.Fatalf("load issued gift model: %v", err)
	}
	if issuedIDAfterSync != issuedModelID || issuedNameAfterSync != "Aurora Renamed" {
		t.Fatalf("issued gift model = %d %q, want %d %q", issuedIDAfterSync, issuedNameAfterSync, issuedModelID, "Aurora Renamed")
	}

	// Снятие задействованной строки — отказ с понятной ошибкой.
	blocked := make([]domain.StarGiftCollectibleAttribute, 0, len(syncModels))
	for _, model := range synced.Models {
		if model.ID == issuedModelID {
			continue
		}
		blocked = append(blocked, model)
	}
	// Пул обязан оставаться валидным (2+ selectable): добавляем ещё одну
	// новую строку, чтобы отказ пришёл именно от проверки ссылок.
	blocked = append(blocked, domain.StarGiftCollectibleAttribute{
		Kind: domain.StarGiftCollectibleModel, Name: "Extra", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 380,
		Document: collectibleTestDocumentPtr(baseDocumentID+6, "model-extra.tgs"),
		Blob:     collectibleTestBlobPtr(baseDocumentID+6, "model-extra"), Animation: collectibleTestAnimationPtr("model-extra.tgs"),
	})
	_, err = gifts.SyncActiveCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 120, SupplyTotal: 12, SlugPrefix: "sync-" + suffix,
		Models: blocked, Patterns: syncPatterns, Backdrops: syncBackdrops,
		Actor: "integration", CommandID: "sync-blocked-" + suffix,
	})
	if !errors.Is(err, domain.ErrStarGiftCollectibleAttributeInUse) {
		t.Fatalf("removing referenced attribute err = %v, want ErrStarGiftCollectibleAttributeInUse", err)
	}

	// Ссылки выпущенного подарка не пострадали после отклонённой команды.
	if err := pool.QueryRow(ctx, `SELECT m.name FROM unique_star_gifts u
JOIN star_gift_collectible_models m ON m.id=u.model_attribute_id AND m.collectible_revision_id=u.collectible_revision_id
WHERE u.id=$1`, upgraded.Unique.ID).Scan(&issuedNameAfterSync); err != nil || issuedNameAfterSync != "Aurora Renamed" {
		t.Fatalf("issued gift model after rejected sync = %q err %v", issuedNameAfterSync, err)
	}
}
