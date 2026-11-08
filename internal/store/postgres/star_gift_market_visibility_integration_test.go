package postgres

import (
        "context"
        "fmt"
        "testing"
        "time"

        "telesrv/internal/domain"
)

// TestStarGiftMarketShowsAllListedGiftsPostgres — раунд 28, отчёт «на маркете
// видно только 60 из 100 выставленных подарков». Маркет обязан показывать ВСЕ
// листинги типа подарка, включая экземпляры, скрытые из профилей продавцов
// (saveStarGift/unsave): скрытие из профиля не является снятием с продажи.
// Одновременно все счётчики обязаны совпадать: payments.resaleStarGifts.count
// == полный объём пагинации == availability_resale каталога == listed_count
// value-info (расхождение счётчиков давало «70 → 55» в клиенте). Сортировки
// по цене, номеру и дате не должны терять подарки.
func TestStarGiftMarketShowsAllListedGiftsPostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        now := int(time.Now().Unix())

        users := NewUserStore(pool)
        seller1 := createTestUser(t, ctx, users, "+1891"+suffix+"01", "MarketSeller1", "")
        seller2 := createTestUser(t, ctx, users, "+1891"+suffix+"02", "MarketSeller2", "")
        seller1Peer := domain.Peer{Type: domain.PeerTypeUser, ID: seller1.ID}
        seller2Peer := domain.Peer{Type: domain.PeerTypeUser, ID: seller2.ID}

        stars := NewStarsStore(pool)
        for _, u := range []domain.User{seller1, seller2} {
                if _, _, err := stars.EnsureGrant(ctx, u.ID, 500_000, now); err != nil {
                        t.Fatalf("grant: %v", err)
                }
        }

        gifts := NewStarGiftStore(pool)
        base := time.Now().UnixNano() & 0x7ffffffffffff000
        entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                Title: "MarketAll " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
                Document: collectibleTestDocument(base, "market-all.tgs"),
                Blob:     collectibleTestBlob(base, "market-all"), Animation: collectibleTestAnimation("market-all.tgs"),
                Actor: "integration", CommandID: "market-all-catalog-" + suffix,
        })
        if err != nil {
                t.Fatalf("catalog: %v", err)
        }
        if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
                GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 200, SlugPrefix: "mka-" + suffix,
                Models: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectibleModel, Name: "M1", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestDocumentPtr(base+1, "m1.tgs"), Blob: collectibleTestBlobPtr(base+1, "m1"), Animation: collectibleTestAnimationPtr("m1.tgs")},
                        {Kind: domain.StarGiftCollectibleModel, Name: "M2", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
                                Document: collectibleTestDocumentPtr(base+2, "m2.tgs"), Blob: collectibleTestBlobPtr(base+2, "m2"), Animation: collectibleTestAnimationPtr("m2.tgs")},
                },
                Patterns: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectiblePattern, Name: "P1", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestPatternDocumentPtr(base+3, "p1.tgs"), Blob: collectibleTestBlobPtr(base+3, "p1"), Animation: collectibleTestAnimationPtr("p1.tgs")},
                        {Kind: domain.StarGiftCollectiblePattern, Name: "P2", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
                                Document: collectibleTestPatternDocumentPtr(base+4, "p2.tgs"), Blob: collectibleTestBlobPtr(base+4, "p2"), Animation: collectibleTestAnimationPtr("p2.tgs")},
                },
                Backdrops: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "B1", BackdropID: 95, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "B2", BackdropID: 96, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500},
                },
                Actor: "integration", CommandID: "market-all-pool-" + suffix,
        }); err != nil {
                t.Fatalf("pool: %v", err)
        }

        messages := NewMessageStore(pool)
        lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
                StarsProceedsPermille: 900, TONProceedsPermille: 900,
        }), WithStarGiftPriceLimits(domain.StarGiftPriceLimits{StarsMin: 10, StarsMax: 35000}))
        upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
                TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
        }))

        const totalGifts = 100
        const hiddenGifts = 40
        type listed struct {
                uniqueID int64
                num      int
                price    int64
                owner    domain.Peer
        }
        market := make([]listed, 0, totalGifts)
        for i := 0; i < totalGifts; i++ {
                // Продавцы чередуются: у обоих скрытая часть профиля не должна
                // влиять на видимость листингов в маркете.
                owner := seller1Peer
                if i%3 == 0 {
                        owner = seller2Peer
                }
                ownerUser := seller1.ID
                if owner == seller2Peer {
                        ownerUser = seller2.ID
                }
                purchase := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
                        BuyerUserID: ownerUser, To: owner, GiftID: entry.Gift.ID,
                        CommandKey: fmt.Sprintf("market-all-buy-%s-%02d", suffix, i), Date: now + i,
                })
                purchased, err := lifecycle.PurchaseStarGift(ctx, purchase)
                if err != nil {
                        t.Fatalf("purchase %d: %v", i, err)
                }
                upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                        UserID: ownerUser, Ref: domain.SavedStarGiftRef{Owner: owner, MsgID: purchased.Saved.MsgID},
                        ChargeStars: 100, FormID: int64(90000 + i), KeepOriginalDetails: true,
                        CommandKey: fmt.Sprintf("market-all-upgrade-%s-%02d", suffix, i), Date: now + 100 + i,
                })
                if err != nil {
                        t.Fatalf("upgrade %d: %v", i, err)
                }
                price := int64(100 + i)
                listRef := domain.SavedStarGiftRef{Owner: owner, MsgID: upgraded.Saved.UpgradeMsgID}
                if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
                        ActorUserID: ownerUser, Ref: listRef,
                        Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: price},
                        Date:   now + 300 + i,
                }); err != nil {
                        t.Fatalf("listing %d: %v", i, err)
                }
                market = append(market, listed{uniqueID: upgraded.Unique.ID, num: upgraded.Unique.Num, price: price, owner: owner})
        }

        // Продающие скрывают часть подарков из профиля (по slug'у уникального
        // подарка): листинг остаётся активным, и маркет обязан продолжать
        // показывать все 100 экземпляров.
        hiddenCount := 0
        for i := 0; i < totalGifts; i++ {
                if i%5 != 0 {
                        continue
                }
                var slug string
                if err := pool.QueryRow(ctx, `SELECT slug FROM unique_star_gifts WHERE id=$1`, market[i].uniqueID).Scan(&slug); err != nil {
                        t.Fatalf("slug %d: %v", i, err)
                }
                if _, err := gifts.SetUnsaved(ctx, domain.SavedStarGiftRef{Owner: market[i].owner, Slug: slug}, true); err != nil {
                        t.Fatalf("hide %d: %v", i, err)
                }
                hiddenCount++
        }
        if hiddenCount != 20 {
                t.Fatalf("hidden count = %d, want 20", hiddenCount)
        }

        // Профиль продавца показывает только видимые подарки (скрытые исключены).
        page1, err := gifts.ListByOwner(ctx, seller1Peer, true, "", 200)
        if err != nil {
                t.Fatalf("profile list: %v", err)
        }
        page2, err := gifts.ListByOwner(ctx, seller2Peer, true, "", 200)
        if err != nil {
                t.Fatalf("profile list 2: %v", err)
        }
        wantVisible1, wantVisible2 := 0, 0
        for i := 0; i < totalGifts; i++ {
                if i%5 == 0 {
                        continue
                }
                if market[i].owner == seller1Peer {
                        wantVisible1++
                } else {
                        wantVisible2++
                }
        }
        if len(page1.Gifts) != wantVisible1 || len(page2.Gifts) != wantVisible2 {
                t.Fatalf("profile visible gifts = %d/%d, want %d/%d", len(page1.Gifts), len(page2.Gifts), wantVisible1, wantVisible2)
        }

        // Пагинация без сортировки (по дате листинга) должна вернуть ВСЕ 100.
        paginate := func(filter domain.StarGiftResaleFilter) (map[int64]int64, []int64, int, error) {
                filter.GiftID = entry.Gift.ID
                if filter.Limit == 0 {
                        filter.Limit = 10
                }
                seen := make(map[int64]int64)
                orderedPrices := make([]int64, 0, totalGifts)
                count := -1
                pages := 0
                for {
                        page, err := lifecycle.ListResaleStarGifts(ctx, filter)
                        if err != nil {
                                return nil, nil, 0, err
                        }
                        if count == -1 {
                                count = page.Count
                        } else if page.Count != count {
                                return nil, nil, 0, fmt.Errorf("count changed across pages: %d -> %d", count, page.Count)
                        }
                        for _, gift := range page.Gifts {
                                if _, dup := seen[gift.ID]; dup {
                                        return nil, nil, 0, fmt.Errorf("duplicate gift %d across pages", gift.ID)
                                }
                                seen[gift.ID] = gift.ResellStars
                                orderedPrices = append(orderedPrices, gift.ResellStars)
                        }
                        pages++
                        if pages > totalGifts+2 {
                                return nil, nil, 0, fmt.Errorf("pagination does not terminate")
                        }
                        if page.NextOffset == "" {
                                break
                        }
                        filter.Offset = page.NextOffset
                }
                return seen, orderedPrices, count, nil
        }

        _, _, count, err := paginate(domain.StarGiftResaleFilter{})
        if err != nil {
                t.Fatalf("date pagination: %v", err)
        }
        if count != totalGifts {
                t.Fatalf("date-sorted market count = %d, want %d (раунд 28: скрытые из профиля подарки обязаны быть в маркете)", count, totalGifts)
        }

        // Сортировка по цене: все подарки, по возрастанию цены, без потерь.
        seen, orderedPrices, count, err := paginate(domain.StarGiftResaleFilter{SortByPrice: true})
        if err != nil {
                t.Fatalf("price pagination: %v", err)
        }
        if count != totalGifts || len(seen) != totalGifts {
                t.Fatalf("price-sorted market: count=%d items=%d, want %d/%d", count, len(seen), totalGifts, totalGifts)
        }
        if len(orderedPrices) != totalGifts {
                t.Fatalf("price-sorted market ordered prices = %d, want %d", len(orderedPrices), totalGifts)
        }
        prev := int64(0)
        for _, price := range orderedPrices {
                if price < prev {
                        t.Fatalf("price sort broken: %d after %d", price, prev)
                }
                prev = price
        }

        // Сортировка по номеру: все подарки, по возрастанию номера.
        seenNum := make(map[int64]int64)
        {
                filter := domain.StarGiftResaleFilter{GiftID: entry.Gift.ID, SortByNum: true, Limit: 10}
                pages := 0
                prevNum := 0
                for {
                        page, err := lifecycle.ListResaleStarGifts(ctx, filter)
                        if err != nil {
                                t.Fatalf("num pagination: %v", err)
                        }
                        if page.Count != totalGifts {
                                t.Fatalf("num-sorted market count = %d, want %d", page.Count, totalGifts)
                        }
                        for _, gift := range page.Gifts {
                                if _, dup := seenNum[gift.ID]; dup {
                                        t.Fatalf("duplicate gift %d in num sort", gift.ID)
                                }
                                seenNum[gift.ID] = int64(gift.Num)
                                if gift.Num < prevNum {
                                        t.Fatalf("num sort broken: %d after %d", gift.Num, prevNum)
                                }
                                prevNum = gift.Num
                        }
                        pages++
                        if pages > totalGifts+2 {
                                t.Fatalf("num pagination does not terminate")
                        }
                        if page.NextOffset == "" {
                                break
                        }
                        filter.Offset = page.NextOffset
                }
        }
        if len(seenNum) != totalGifts {
                t.Fatalf("num-sorted market items = %d, want %d", len(seenNum), totalGifts)
        }

        // Звёздный фильтр: все листинги в XTR → все 100.
        seen, _, count, err = paginate(domain.StarGiftResaleFilter{StarsOnly: true})
        if err != nil {
                t.Fatalf("stars-only pagination: %v", err)
        }
        if count != totalGifts || len(seen) != totalGifts {
                t.Fatalf("stars-only market: count=%d items=%d, want %d/%d", count, len(seen), totalGifts, totalGifts)
        }

        // Счётчик каталога «N for sale» обязан совпадать с маркетом — иначе
        // клиент показывает одно число на странице подарка и другое на маркете.
        var availabilityResale int64
        var resellMinStars int64
        if err := pool.QueryRow(ctx, `SELECT availability_resale, resell_min_stars FROM star_gift_catalog WHERE gift_id=$1`,
                entry.Gift.ID).Scan(&availabilityResale, &resellMinStars); err != nil {
                t.Fatalf("catalog projection: %v", err)
        }
        if availabilityResale != totalGifts {
                t.Fatalf("catalog availability_resale = %d, want %d (счётчики каталога и маркета обязаны совпадать)", availabilityResale, totalGifts)
        }
        if resellMinStars != 100 {
                t.Fatalf("catalog resell_min_stars = %d, want 100", resellMinStars)
        }

        // Value-info listed_count — тот же счётчик, что и на маркете.
        info, err := lifecycle.UniqueStarGiftValueInfo(ctx, market[0].uniqueID)
        if err != nil {
                t.Fatalf("value info: %v", err)
        }
        if info.ListedCount != totalGifts {
                t.Fatalf("value info listed_count = %d, want %d", info.ListedCount, totalGifts)
        }
}
