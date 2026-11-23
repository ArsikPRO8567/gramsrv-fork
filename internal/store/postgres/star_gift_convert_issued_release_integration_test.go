package postgres

import (
        "context"
        "fmt"
        "testing"
        "time"

        "telesrv/internal/domain"
)

// TestStarGiftConvertReleasesUpgradeSlotPostgres (раунд 41) — конвертация
// редкого подарка, имеющего улучшения (активный опубликованный пул
// коллекционных атрибутов), уменьшает общее число выпущенных улучшений
// (star_gift_collectible_revisions.issued) на 1: слот улучшения освобождается
// и может быть занят следующим улучшением. Оба пути конвертации (агрегатный
// ConvertStarGift и простой MarkConverted) ведут себя одинаково; счётчик не
// уходит в минус, а посторонние UPDATE по-прежнему отвергаются охраной 0196.
func TestStarGiftConvertReleasesUpgradeSlotPostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        users := NewUserStore(pool)
        sender := createTestUser(t, ctx, users, "+1779"+suffix+"51", "SlotSender", "")
        owner := createTestUser(t, ctx, users, "+1779"+suffix+"52", "SlotOwner", "")
        ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

        gifts := NewStarGiftStore(pool)
        baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
        entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                Title: "Nebula", Stars: 50, ConvertStars: 25, Enabled: true,
                Limited: true, AvailabilityTotal: 100, AvailabilityRemains: 100,
                Document:  collectibleTestDocument(baseDocumentID, "gift.tgs"),
                Blob:      collectibleTestBlob(baseDocumentID, "gift"),
                Animation: collectibleTestAnimation("gift.tgs"),
                Actor:     "integration", CommandID: "convert-slot-catalog-" + suffix,
        })
        if err != nil {
                t.Fatalf("create rare catalog gift: %v", err)
        }
        poolRevision, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
                GiftID: entry.Gift.ID, UpgradeStars: 10, SupplyTotal: 5, SlugPrefix: "nebula-" + suffix,
                Models: []domain.StarGiftCollectibleAttribute{{
                        Kind: domain.StarGiftCollectibleModel, Name: "Vega", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 700,
                        Document: collectibleTestDocumentPtr(baseDocumentID+11, "model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+11, "model"), Animation: collectibleTestAnimationPtr("model.tgs"),
                }, {
                        Kind: domain.StarGiftCollectibleModel, Name: "Altair", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 300,
                        Document: collectibleTestDocumentPtr(baseDocumentID+13, "model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+13, "model-two"), Animation: collectibleTestAnimationPtr("model-two.tgs"),
                }},
                Patterns: []domain.StarGiftCollectibleAttribute{{
                        Kind: domain.StarGiftCollectiblePattern, Name: "Tail", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 800,
                        Document: collectibleTestPatternDocumentPtr(baseDocumentID+12, "pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+12, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs"),
                }, {
                        Kind: domain.StarGiftCollectiblePattern, Name: "Halo", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 200,
                        Document: collectibleTestPatternDocumentPtr(baseDocumentID+14, "pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+14, "pattern-two"), Animation: collectibleTestAnimationPtr("pattern-two.tgs"),
                }},
                Backdrops: []domain.StarGiftCollectibleAttribute{{
                        Kind: domain.StarGiftCollectibleBackdrop, Name: "Void", BackdropID: 1, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff,
                        RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900,
                }, {
                        Kind: domain.StarGiftCollectibleBackdrop, Name: "Dawn", BackdropID: 2, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111,
                        RarityKind: domain.StarGiftRarityPermille, RarityPermille: 100,
                }},
                Actor: "integration", CommandID: "convert-slot-pool-" + suffix,
        })
        if err != nil {
                t.Fatalf("publish upgrade pool: %v", err)
        }

        messages := NewMessageStore(pool)
        upgrades := NewStarGiftUpgradeStore(pool, messages)
        stars := NewStarsStore(pool)
        if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, 1700002020); err != nil {
                t.Fatalf("grant owner balance: %v", err)
        }

        const giftCount = 6
        saved := make([]domain.SavedStarGift, 0, giftCount)
        for index := range giftCount {
                saved = append(saved, createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
                        Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
                        Date: 1700002030 + index, ConvertStars: 10,
                }))
        }

        // Два улучшения занимают два слота пула: issued = 2 из supply_total = 5.
        for index := range 2 {
                if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                        UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved[index].MsgID},
                        ChargeStars: 10, FormID: int64(990 + index), CommandKey: fmt.Sprintf("convert-slot-up-%d-%s", index, suffix),
                        Date: 1700002040 + index,
                }); err != nil {
                        t.Fatalf("upgrade gift %d: %v", index, err)
                }
        }
        var issued int
        if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, poolRevision.ID).Scan(&issued); err != nil || issued != 2 {
                t.Fatalf("issued after two upgrades = %d err %v, want 2", issued, err)
        }

        lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)

        // Конвертация (агрегатный путь) уменьшает выпущенные улучшения на 1.
        converted, err := lifecycle.ConvertStarGift(ctx, domain.StarGiftConvertRequest{
                ActorUserID: owner.ID,
                Ref:         domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved[2].MsgID},
                Date:        1700002050,
        })
        if err != nil || !converted.Saved.Converted {
                t.Fatalf("lifecycle convert = %+v err %v", converted, err)
        }
        if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, poolRevision.ID).Scan(&issued); err != nil || issued != 1 {
                t.Fatalf("issued after lifecycle convert = %d err %v, want 1", issued, err)
        }

        // Простой путь (MarkConverted) ведёт себя так же.
        if _, err := gifts.MarkConverted(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved[3].MsgID}); err != nil {
                t.Fatalf("mark converted: %v", err)
        }
        if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, poolRevision.ID).Scan(&issued); err != nil || issued != 0 {
                t.Fatalf("issued after simple convert = %d err %v, want 0", issued, err)
        }

        // Пол (floor): счётчик не уходит в минус при исчерпанных слотах.
        if _, err := lifecycle.ConvertStarGift(ctx, domain.StarGiftConvertRequest{
                ActorUserID: owner.ID,
                Ref:         domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved[4].MsgID},
                Date:        1700002051,
        }); err != nil {
                t.Fatalf("convert with empty pool: %v", err)
        }
        if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, poolRevision.ID).Scan(&issued); err != nil || issued != 0 {
                t.Fatalf("issued after floor convert = %d err %v, want 0", issued, err)
        }

        // Проекция доступности пула отражает освобождение слотов.
        availability, err := gifts.CollectibleAvailability(ctx, []int64{entry.Gift.ID})
        if err != nil {
                t.Fatalf("collectible availability: %v", err)
        }
        if got, ok := availability[entry.Gift.ID]; !ok || got.Issued != 0 || got.SupplyTotal != 5 {
                t.Fatalf("availability projection = %+v, want issued 0 supply 5", got)
        }

        // Освобождённый слот реально доступен: улучшение снова проходит
        // (issued 0 -> 1) без каких-либо административных правок пула.
        if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved[5].MsgID},
                ChargeStars: 10, FormID: int64(995), CommandKey: "convert-slot-re-up-" + suffix, Date: 1700002052,
        }); err != nil {
                t.Fatalf("upgrade after released slot: %v", err)
        }
        if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, poolRevision.ID).Scan(&issued); err != nil || issued != 1 {
                t.Fatalf("issued after re-upgrade = %d err %v, want 1", issued, err)
        }

        // Повторная конвертация уже сконвертированного подарка невозможна —
        // двойного освобождения слота не бывает.
        if _, err := lifecycle.ConvertStarGift(ctx, domain.StarGiftConvertRequest{
                ActorUserID: owner.ID,
                Ref:         domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved[2].MsgID},
                Date:        1700002053,
        }); err == nil {
                t.Fatalf("repeated convert must fail")
        }
        if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, poolRevision.ID).Scan(&issued); err != nil || issued != 1 {
                t.Fatalf("issued after repeated convert = %d err %v, want unchanged 1", issued, err)
        }
}
