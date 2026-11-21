package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// Раунд 34 (исправление «время раундов не влияет на итоговый вариант»): строка
// star_gift_auctions создаётся лениво с дефолтной сеткой 3600 секунд, поэтому
// после смены длительности раундов (общей или индивидуальной) сохранённые
// границы next_round_at/end_date обязаны пересчитываться — аукцион реально
// идёт по новым длительностям, а не по часу на раунд.
func TestStarGiftAuctionRoundDurationsRecomputePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())

	gifts := NewStarGiftStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, nil, 0)
	baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 3000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "RoundDurations " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 4, AvailabilityRemains: 4, GiftsPerRound: 1, AuctionStartDate: now - 10,
		AuctionSlug: "durations-" + suffix,
		Document:    collectibleTestDocument(baseDocumentID, "durations.tgs"),
		Blob:        collectibleTestBlob(baseDocumentID, "durations"), Animation: collectibleTestAnimation("durations.tgs"),
		Actor: "integration", CommandID: "durations-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create auction catalog: %v", err)
	}
	giftID := entry.Gift.ID

	// Ленивое создание строки: дефолтная сетка 3600 секунд (как до раунда 34).
	initial, err := lifecycle.SetStarGiftAuctionSettings(ctx, domain.StarGiftAuctionRuntimeWrite{
		GiftID: giftID, MinBidAmount: func() *int64 { v := int64(150); return &v }(),
	})
	if err != nil {
		t.Fatalf("ensure auction row: %v", err)
	}
	if initial.RoundDuration != 3600 {
		t.Fatalf("initial round duration = %d, want default 3600", initial.RoundDuration)
	}
	if initial.NextRoundAt != initial.StartDate+3600 {
		t.Fatalf("initial next_round_at = %d, want start+3600 = %d", initial.NextRoundAt, initial.StartDate+3600)
	}
	if initial.EndDate != initial.StartDate+initial.TotalRounds*3600 {
		t.Fatalf("initial end_date = %d, want start+total*3600 = %d", initial.EndDate, initial.StartDate+initial.TotalRounds*3600)
	}

	// Общая длительность 120 секунд: границы обязаны пересчитаться.
	updated, err := lifecycle.SetStarGiftAuctionSettings(ctx, domain.StarGiftAuctionRuntimeWrite{
		GiftID: giftID, RoundDuration: func() *int { v := 120; return &v }(),
	})
	if err != nil {
		t.Fatalf("set round duration: %v", err)
	}
	if updated.RoundDuration != 120 {
		t.Fatalf("round duration = %d, want 120", updated.RoundDuration)
	}
	if updated.NextRoundAt != updated.StartDate+120 {
		t.Fatalf("next_round_at = %d, want start+120 = %d", updated.NextRoundAt, updated.StartDate+120)
	}
	if want := updated.StartDate + updated.TotalRounds*120; updated.EndDate != want {
		t.Fatalf("end_date = %d, want start+total*120 = %d", updated.EndDate, want)
	}
	for round := 1; round <= updated.TotalRounds; round++ {
		if duration := updated.RoundDurationFor(round); duration != 120 {
			t.Fatalf("round %d duration = %d, want 120", round, duration)
		}
	}

	// Индивидуальные длительности (jsonb): r1=60, r2=180, пропущенные — общие.
	updated, err = lifecycle.SetStarGiftAuctionSettings(ctx, domain.StarGiftAuctionRuntimeWrite{
		GiftID: giftID, RoundDurationsJSON: `{"round_1":60,"round_2":180}`,
	})
	if err != nil {
		t.Fatalf("set round durations json: %v", err)
	}
	if updated.NextRoundAt != updated.StartDate+60 {
		t.Fatalf("jsonb next_round_at = %d, want start+60 = %d", updated.NextRoundAt, updated.StartDate+60)
	}
	if want := updated.StartDate + 60 + 180 + (updated.TotalRounds-2)*120; updated.EndDate != want {
		t.Fatalf("jsonb end_date = %d, want %d", updated.EndDate, want)
	}
	if updated.RoundDurationFor(1) != 60 || updated.RoundDurationFor(2) != 180 || updated.RoundDurationFor(3) != 120 {
		t.Fatalf("round durations for = %d/%d/%d, want 60/180/120",
			updated.RoundDurationFor(1), updated.RoundDurationFor(2), updated.RoundDurationFor(3))
	}

	// Ручной end_date той же команды приоритетнее производного.
	manualEnd := now + 99999
	updated, err = lifecycle.SetStarGiftAuctionSettings(ctx, domain.StarGiftAuctionRuntimeWrite{
		GiftID: giftID, RoundDuration: func() *int { v := 90; return &v }(), EndDate: &manualEnd,
	})
	if err != nil {
		t.Fatalf("set manual end: %v", err)
	}
	if updated.EndDate != manualEnd {
		t.Fatalf("manual end_date = %d, want %d", updated.EndDate, manualEnd)
	}
	// jsonb round_1=60 задан выше — индивидуальная длительность раунда 1
	// приоритетнее общей (start+60), общая 90 действует для раундов без
	// значения в jsonb.
	if updated.NextRoundAt != updated.StartDate+60 {
		t.Fatalf("manual end next_round_at = %d, want start+60 = %d", updated.NextRoundAt, updated.StartDate+60)
	}

	// Раунд 35: явное количество раундов замещает выведенное из
	// supply/gifts_per_round и пересчитывает end_date. jsonb из шага выше
	// задаёт r1=60 и r2=180, current_round=1 → end = start+60+180 при
	// total_rounds=2.
	rounds := 2
	updated, err = lifecycle.SetStarGiftAuctionSettings(ctx, domain.StarGiftAuctionRuntimeWrite{
		GiftID: giftID, TotalRounds: &rounds,
	})
	if err != nil {
		t.Fatalf("set total rounds: %v", err)
	}
	if updated.TotalRounds != 2 {
		t.Fatalf("total_rounds = %d, want 2", updated.TotalRounds)
	}
	if want := updated.StartDate + 60 + 180; updated.EndDate != want {
		t.Fatalf("rounds end_date = %d, want %d", updated.EndDate, want)
	}
	// Нулевое количество раундов отклоняется (валидация до записи).
	if _, err := lifecycle.SetStarGiftAuctionSettings(ctx, domain.StarGiftAuctionRuntimeWrite{
		GiftID: giftID, TotalRounds: func() *int { v := 0; return &v }(),
	}); !errors.Is(err, domain.ErrStarGiftInvalid) {
		t.Fatalf("zero total rounds: err=%v, want ErrStarGiftInvalid", err)
	}
}

// Раунд 34: лимит возможных улучшенных подарков опубликованной ревизии —
// повышение разрешено (supply каталога увеличен), значение ниже уже
// выпущенного количества отклоняется.
func TestStarGiftCollectibleSupplyUpdatePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())

	gifts := NewStarGiftStore(pool)
	_ = now
	baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 3100
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Supply " + suffix, Stars: 100, Enabled: true, Limited: true,
		AvailabilityTotal: 5, AvailabilityRemains: 5,
		Document: collectibleTestDocument(baseDocumentID, "supply.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "supply"), Animation: collectibleTestAnimation("supply.tgs"),
		Actor: "integration", CommandID: "supply-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	giftID := entry.Gift.ID
	slugPrefix := "supply-" + suffix
	_, err = gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: giftID, UpgradeStars: 50, SupplyTotal: 5, SlugPrefix: slugPrefix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "M1", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+1, "m1.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "m1"), Animation: collectibleTestAnimationPtr("m1.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "M2", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900,
				Document: collectibleTestDocumentPtr(baseDocumentID+2, "m2.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "m2"), Animation: collectibleTestAnimationPtr("m2.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "P1", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+3, "p1.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "p1"), Animation: collectibleTestAnimationPtr("p1.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "P2", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+4, "p2.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+4, "p2"), Animation: collectibleTestAnimationPtr("p2.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "B1", BackdropID: 1, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				CenterColor: 0x6f5bea, EdgeColor: 0x34278f, PatternColor: 0xa89df5, TextColor: 0xffffff},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "B2", BackdropID: 2, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900,
				CenterColor: 0x32a86b, EdgeColor: 0x17613e, PatternColor: 0x8ee0b3, TextColor: 0xffffff},
		},
		Actor: "integration", CommandID: "supply-collectible-" + suffix,
	})
	if err != nil {
		t.Fatalf("publish collectible revision: %v", err)
	}

	// Повышение лимита (supply каталога увеличен).
	updated, err := gifts.SetCollectibleSupplyTotal(ctx, domain.StarGiftCollectibleSupplyWrite{
		GiftID: giftID, SupplyTotal: 12, Actor: "integration", CommandID: "supply-raise-" + suffix,
	})
	if err != nil {
		t.Fatalf("raise supply: %v", err)
	}
	if updated.SupplyTotal != 12 || updated.Issued != 0 {
		t.Fatalf("raised supply = %d/%d, want 12/0", updated.SupplyTotal, updated.Issued)
	}
	active, found, err := gifts.ActiveCollectibleRevision(ctx, giftID)
	if err != nil || !found {
		t.Fatalf("active collectible revision: found=%v err=%v", found, err)
	}
	if active.SupplyTotal != 12 {
		t.Fatalf("active supply_total = %d, want 12", active.SupplyTotal)
	}

	// Ниже выпущенного количества — отказ. Имитируем выдачу двух апгрейдов
	// (issued=2) прямым обновлением issued, как это делает апгрейд-путь.
	// Guard 0093/0189 разрешает изменение issued строго шагом +1.
	for step := 0; step < 2; step++ {
		if _, err := pool.Exec(ctx, `UPDATE star_gift_collectible_revisions SET issued=issued+1 WHERE id=$1`, active.ID); err != nil {
			t.Fatalf("simulate issued step %d: %v", step, err)
		}
	}
	if _, err := gifts.SetCollectibleSupplyTotal(ctx, domain.StarGiftCollectibleSupplyWrite{
		GiftID: giftID, SupplyTotal: 1, Actor: "integration", CommandID: "supply-lower-" + suffix,
	}); !errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
		t.Fatalf("lower below issued: err=%v, want ErrStarGiftCollectibleInvalid", err)
	}
	// Любое понижение лимита запрещено (guard 0189 + чистая ошибка в адаптере).
	if _, err := gifts.SetCollectibleSupplyTotal(ctx, domain.StarGiftCollectibleSupplyWrite{
		GiftID: giftID, SupplyTotal: 6, Actor: "integration", CommandID: "supply-decrease-" + suffix,
	}); !errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
		t.Fatalf("decrease supply: err=%v, want ErrStarGiftCollectibleInvalid", err)
	}
	// Повышение при ненулевом issued — разрешено (лимит >= issued).
	updated, err = gifts.SetCollectibleSupplyTotal(ctx, domain.StarGiftCollectibleSupplyWrite{
		GiftID: giftID, SupplyTotal: 20, Actor: "integration", CommandID: "supply-raise-issued-" + suffix,
	})
	if err != nil || updated.SupplyTotal != 20 || updated.Issued != 2 {
		t.Fatalf("raise with issued = %+v err %v, want 20/2", updated, err)
	}

	// Список ревизий (раунд 34) — источник для копирования блоками.
	summaries, scope, err := gifts.CollectibleRevisionSummaries(ctx, giftID, "")
	if err != nil {
		t.Fatalf("revision summaries: %v", err)
	}
	if scope != "gift" {
		t.Fatalf("scope = %q, want gift", scope)
	}
	if len(summaries) != 1 {
		t.Fatalf("revision summaries = %d entries, want 1", len(summaries))
	}
	if summaries[0].RevisionID != active.ID || summaries[0].Revision != 1 || summaries[0].Status != "published" ||
		summaries[0].ModelCount != 2 || summaries[0].PatternCount != 2 || summaries[0].BackdropCount != 2 {
		t.Fatalf("summary = %+v", summaries[0])
	}
	// Раунд 35/36: список по slug коллекции — ревизия находится даже у
	// подарка с другим gift_id (slug-режим, объединён с gift_id).
	bySlug, scope, err := gifts.CollectibleRevisionSummaries(ctx, giftID+1000, slugPrefix)
	if err != nil {
		t.Fatalf("revision summaries by slug: %v", err)
	}
	if scope != "slug" {
		t.Fatalf("scope by slug = %q, want slug", scope)
	}
	if len(bySlug) != 1 || bySlug[0].RevisionID != active.ID || bySlug[0].SlugPrefix != slugPrefix || bySlug[0].GiftID != giftID {
		t.Fatalf("summaries by slug = %+v", bySlug)
	}
	// Раунд 36: несуществующие slug И gift_id дают fallback — последние
	// ревизии всех коллекций (scope "all"), выбор не остаётся пустым.
	fallback, scope, err := gifts.CollectibleRevisionSummaries(ctx, giftID+1000, "foreign-slug-"+suffix)
	if err != nil {
		t.Fatalf("revision summaries fallback: %v", err)
	}
	if scope != "all" || len(fallback) == 0 {
		t.Fatalf("fallback = scope %q, %d entries, want all/non-empty", scope, len(fallback))
	}
}

// Раунд 34: редактирование параметров с ОПУБЛИКОВАННОЙ заменой анимации и без
// неё. Без анимации документ копируется (фикс SQLSTATE-ошибки чтения blob:
// pgx-параметр $1::text не должен получать int64); с анимацией — новый
// документ/blob и новые анимационные столбцы.
func TestStarGiftUpdateCatalogSettingsAnimationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())

	gifts := NewStarGiftStore(pool)
	_ = now
	baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 3200
	animation := collectibleTestAnimation("settings.tgs")
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Settings " + suffix, Stars: 100, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "settings.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "settings"), Animation: animation,
		Actor: "integration", CommandID: "settings-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	giftID := entry.Gift.ID
	originalDocumentID := entry.Gift.Sticker.ID

	// Только настройки: документ анимации КОПИРУЕТСЯ (новый id, те же данные).
	copied, err := gifts.UpdateCatalogSettings(ctx, domain.StarGiftCatalogSettingsWrite{
		GiftID: giftID, Title: "Settings " + suffix, Stars: 150, ConvertStars: 50, Enabled: true,
		Actor: "integration", CommandID: "settings-copy-" + suffix,
	})
	if err != nil {
		t.Fatalf("settings-only update (blob load): %v", err)
	}
	if copied.Gift.Sticker.ID == originalDocumentID {
		t.Fatalf("copied document id = %d, want a fresh copy", copied.Gift.Sticker.ID)
	}
	var copiedSHA []byte
	var originalSHA []byte
	if err := pool.QueryRow(ctx, `SELECT animation_sha256 FROM star_gift_catalog_revisions WHERE id=$1`, copied.Gift.RevisionID).Scan(&copiedSHA); err != nil {
		t.Fatalf("load copied revision: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT animation_sha256 FROM star_gift_catalog_revisions WHERE id=$1`, entry.Gift.RevisionID).Scan(&originalSHA); err != nil {
		t.Fatalf("load original revision: %v", err)
	}
	if string(copiedSHA) != string(originalSHA) {
		t.Fatalf("copied animation sha mismatch")
	}

	// С новой анимацией: новые документ/blob и новые анимационные столбцы.
	newJSON := []byte(`{"v":"5.7","w":512,"h":512,"fr":60,"ip":0,"op":45,"layers":[{"nm":"replacement"}]}`)
	newTGS := []byte("replacement-tgs-bytes")
	sum := make([]byte, 32)
	copy(sum, newTGS)
	newAnimation := domain.StarGiftAnimation{
		SourceName: "replacement.tgs", SourceFormat: domain.StarGiftAnimationTGS,
		JSON: newJSON, TGS: newTGS, SHA256: sum, Width: 512, Height: 512, FrameRate: 60,
	}
	newDocument := collectibleTestDocument(baseDocumentID+50, "replacement.tgs")
	newBlob := collectibleTestBlob(baseDocumentID+50, "replacement")
	replaced, err := gifts.UpdateCatalogSettings(ctx, domain.StarGiftCatalogSettingsWrite{
		GiftID: giftID, Title: "Settings " + suffix, Stars: 200, ConvertStars: 40, Enabled: true,
		Animation: &newAnimation, Document: &newDocument, Blob: &newBlob,
		Actor: "integration", CommandID: "settings-replace-" + suffix,
	})
	if err != nil {
		t.Fatalf("settings update with animation: %v", err)
	}
	if replaced.Gift.Sticker.ID == copied.Gift.Sticker.ID || replaced.Gift.Sticker.ID == originalDocumentID {
		t.Fatalf("replaced document id = %d, want a fresh document", replaced.Gift.Sticker.ID)
	}
	var revisionJSON string
	var revisionSHA []byte
	if err := pool.QueryRow(ctx, `SELECT animation_json::text, animation_sha256 FROM star_gift_catalog_revisions WHERE id=$1`, replaced.Gift.RevisionID).Scan(&revisionJSON, &revisionSHA); err != nil {
		t.Fatalf("load replaced revision: %v", err)
	}
	if string(revisionSHA) != string(sum) {
		t.Fatalf("replaced animation sha mismatch")
	}
	// jsonb нормализует документ (порядок ключей/пробелы) — сравниваем
	// семантически после разбора, а не побайтово.
	var stored, want map[string]any
	if err := json.Unmarshal([]byte(revisionJSON), &stored); err != nil {
		t.Fatalf("unmarshal stored animation: %v", err)
	}
	if err := json.Unmarshal(newJSON, &want); err != nil {
		t.Fatalf("unmarshal expected animation: %v", err)
	}
	if !reflect.DeepEqual(stored, want) {
		t.Fatalf("replaced animation json mismatch: stored=%s", revisionJSON)
	}
	var activeJSON string
	if err := pool.QueryRow(ctx, `
SELECT r.animation_json::text FROM star_gift_catalog c
JOIN star_gift_catalog_revisions r ON r.id = c.active_revision_id
WHERE c.gift_id=$1`, giftID).Scan(&activeJSON); err != nil {
		t.Fatalf("load active revision animation: %v", err)
	}
	var active map[string]any
	if err := json.Unmarshal([]byte(activeJSON), &active); err != nil {
		t.Fatalf("unmarshal active animation: %v", err)
	}
	if !reflect.DeepEqual(active, want) {
		t.Fatalf("active revision animation json mismatch: stored=%s", activeJSON)
	}
	var blobCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM file_blobs WHERE location_key=$1`,
		"doc:"+strconv.FormatInt(replaced.Gift.Sticker.ID, 10)).Scan(&blobCount); err != nil {
		t.Fatalf("count replaced blobs: %v", err)
	}
	if blobCount != 1 {
		t.Fatalf("replacement blob rows = %d, want 1", blobCount)
	}
}

// Раунд 36: настройка остатка подарков (gifts_left) и последнего выпущенного
// номера (last_gift_num) — коррекция рабочего состояния для перезапуска
// аукциона. Отрицательные значения отклоняются, запись поднимает version.
func TestStarGiftAuctionRuntimeCountersPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())

	gifts := NewStarGiftStore(pool)
	baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 370
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Counters " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 5, AvailabilityRemains: 5, GiftsPerRound: 1, AuctionStartDate: now - 5,
		AuctionSlug: "counters-" + suffix,
		Document:    collectibleTestDocument(baseDocumentID, "counters.tgs"),
		Blob:        collectibleTestBlob(baseDocumentID, "counters"), Animation: collectibleTestAnimation("counters.tgs"),
		Actor: "integration", CommandID: "counters-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create auction catalog: %v", err)
	}
	lifecycle := NewStarGiftLifecycleStore(pool, NewMessageStore(pool), 0)
	if _, err := lifecycle.StarGiftAuctionState(ctx, entry.Gift.ID, entry.Gift.ID, "", now); err != nil {
		t.Fatalf("materialize auction row: %v", err)
	}

	var versionBefore int
	if err := pool.QueryRow(ctx, `SELECT version FROM star_gift_auctions WHERE gift_id=$1`, entry.Gift.ID).Scan(&versionBefore); err != nil {
		t.Fatalf("load version: %v", err)
	}

	giftsLeft, lastGiftNum := 3, 2
	state, err := lifecycle.SetStarGiftAuctionSettings(ctx, domain.StarGiftAuctionRuntimeWrite{
		GiftID: entry.Gift.ID, GiftsLeft: &giftsLeft, LastGiftNum: &lastGiftNum,
	})
	if err != nil {
		t.Fatalf("set counters: %v", err)
	}
	if state.GiftsLeft != 3 || state.LastGiftNum != 2 {
		t.Fatalf("counters = %d/%d, want 3/2", state.GiftsLeft, state.LastGiftNum)
	}
	var versionAfter int
	if err := pool.QueryRow(ctx, `SELECT version FROM star_gift_auctions WHERE gift_id=$1`, entry.Gift.ID).Scan(&versionAfter); err != nil {
		t.Fatalf("reload version: %v", err)
	}
	if versionAfter != versionBefore+1 {
		t.Fatalf("version = %d, want %d", versionAfter, versionBefore+1)
	}

	// Отрицательный остаток отклоняется чистой ошибкой (shape_check 0095).
	negative := -1
	if _, err := lifecycle.SetStarGiftAuctionSettings(ctx, domain.StarGiftAuctionRuntimeWrite{
		GiftID: entry.Gift.ID, GiftsLeft: &negative,
	}); !errors.Is(err, domain.ErrStarGiftInvalid) {
		t.Fatalf("negative gifts left: err=%v, want ErrStarGiftInvalid", err)
	}
}
