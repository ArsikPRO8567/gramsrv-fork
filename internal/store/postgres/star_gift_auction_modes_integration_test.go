package postgres

import (
        "context"
        "testing"
        "time"

        "telesrv/internal/domain"
)

// Раунд 21: два режима минимальной ставки (raise_min_bid), продление раундов
// при изменении топа (round_extends) и бесплатный апгрейд аукционных подарков
// при нулевой цене улучшения в ревизии.
func TestStarGiftAuctionBidModesPostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        now := int(time.Now().Unix())
        users := NewUserStore(pool)
        alpha := createTestUser(t, ctx, users, "+1883"+suffix+"01", "AuctionAlpha", "")
        beta := createTestUser(t, ctx, users, "+1883"+suffix+"02", "AuctionBeta", "")
        gamma := createTestUser(t, ctx, users, "+1883"+suffix+"03", "AuctionGamma", "")
        alphaPeer := domain.Peer{Type: domain.PeerTypeUser, ID: alpha.ID}

        stars := NewStarsStore(pool)
        for _, user := range []domain.User{alpha, beta, gamma} {
                if _, _, err := stars.EnsureGrant(ctx, user.ID, 100000, now); err != nil {
                        t.Fatalf("grant stars: %v", err)
                }
        }

        gifts := NewStarGiftStore(pool)
        baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 900
        // Аукционный подарок с ревизией улучшений ценой 0 — бесплатные апгрейды.
        entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                Title: "AuctionModes " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
                AvailabilityTotal: 2, AvailabilityRemains: 2, GiftsPerRound: 1, AuctionStartDate: now - 10,
                AuctionSlug: "modes-" + suffix,
                Document:    collectibleTestDocument(baseDocumentID, "modes.tgs"),
                Blob:        collectibleTestBlob(baseDocumentID, "modes"), Animation: collectibleTestAnimation("modes.tgs"),
                Actor: "integration", CommandID: "modes-catalog-" + suffix,
        })
        if err != nil {
                t.Fatalf("create auction catalog: %v", err)
        }
        if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
                GiftID: entry.Gift.ID, UpgradeStars: 0, SupplyTotal: 5, SlugPrefix: "modes-life-" + suffix,
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
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "B1", BackdropID: 91, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "B2", BackdropID: 92, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900},
                },
                Actor: "integration", CommandID: "modes-pool-" + suffix,
        }); err != nil {
                t.Fatalf("publish collectible pool: %v", err)
        }

        messages := NewMessageStore(pool)
        lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)
        // ensure создаёт строку аукциона лениво — материализуем её до админ-настройки.
        if _, err := lifecycle.StarGiftAuctionState(ctx, alpha.ID, entry.Gift.ID, "", now); err != nil {
                t.Fatalf("materialize auction row: %v", err)
        }
        if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET raise_min_bid=true WHERE gift_id=$1`, entry.Gift.ID); err != nil {
                t.Fatalf("enable raise_min_bid: %v", err)
        }

        // --- Режим повышения: минимум = цена ревизии (100) до первой ставки. ---
        state, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: alpha.ID,
                GiftID: entry.Gift.ID, Peer: alphaPeer, BidAmount: 100, FormID: 21001, Date: now, Message: "alpha"})
        if err != nil || state.UserState.BidAmount != 100 {
                t.Fatalf("alpha first bid = %+v err %v", state.UserState, err)
        }
        if state.MinBidAmount != 101 {
                t.Fatalf("raise mode min bid after first bid = %d, want 101 (top+1)", state.MinBidAmount)
        }
        // Ставка ниже нового пола отклоняется.
        if _, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: beta.ID,
                GiftID: entry.Gift.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: beta.ID}, BidAmount: 100, FormID: 21002, Date: now + 1}); err == nil {
                t.Fatalf("bid equal to top must be rejected in raise mode")
        }
        stateBeta, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: beta.ID,
                GiftID: entry.Gift.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: beta.ID}, BidAmount: 101, FormID: 21003, Date: now + 1})
        if err != nil || stateBeta.UserState.BidAmount != 101 {
                t.Fatalf("beta raise bid = %+v err %v", stateBeta.UserState, err)
        }
        if stateBeta.MinBidAmount != 102 {
                t.Fatalf("raise mode min bid after second bid = %d, want 102", stateBeta.MinBidAmount)
        }

        // --- Граница раунда: победитель выявлен, минимум откатился к цене ревизии. ---
        if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET next_round_at=$2 WHERE gift_id=$1`, entry.Gift.ID, now+2); err != nil {
                t.Fatalf("make round due: %v", err)
        }
        if err := lifecycle.SweepStarGiftLifecycle(ctx, now+2, 1000); err != nil {
                t.Fatalf("sweep settle: %v", err)
        }
        acquired, err := lifecycle.StarGiftAuctionAcquired(ctx, beta.ID, entry.Gift.ID)
        if err != nil || len(acquired) != 1 || acquired[0].BidAmount != 101 {
                t.Fatalf("round 1 winner = %+v err %v", acquired, err)
        }
        // База минимума в таблице откатилась к цене ревизии…
        var storedMinBid int64
        if err := pool.QueryRow(ctx, `SELECT min_bid_amount FROM star_gift_auctions WHERE gift_id=$1`, entry.Gift.ID).Scan(&storedMinBid); err != nil {
                t.Fatalf("load stored min bid: %v", err)
        }
        if storedMinBid != 100 {
                t.Fatalf("stored min bid after round 1 = %d, want 100 (revision price reset)", storedMinBid)
        }
        // …но ставка альфы (100) переносится в раунд 2, поэтому в режиме повышения
        // фактический пол для клиента — «топ-1 + 1» = 101.
        stateRound2, err := lifecycle.StarGiftAuctionState(ctx, alpha.ID, entry.Gift.ID, "", now+2)
        if err != nil {
                t.Fatalf("round 2 state: %v", err)
        }
        if stateRound2.CurrentRound != 2 {
                t.Fatalf("current round = %d, want 2", stateRound2.CurrentRound)
        }
        if stateRound2.MinBidAmount != 101 {
                t.Fatalf("round 2 min bid = %d, want 101 (carried top + 1)", stateRound2.MinBidAmount)
        }
        // В новом раунде минимум выше цены ревизии из-за переносимой ставки альфы.
        stateAlpha2, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: alpha.ID,
                GiftID: entry.Gift.ID, Peer: alphaPeer, BidAmount: 101, FormID: 21004, Date: now + 3, UpdateBid: true})
        if err != nil || stateAlpha2.UserState.BidAmount != 101 {
                t.Fatalf("alpha round 2 bid = %+v err %v", stateAlpha2.UserState, err)
        }

        // --- Бесплатный апгрейд: цена улучшения в ревизии 0. ---
        // Наградное сообщение несёт prepaid_upgrade + can_upgrade (клиент
        // показывает бесплатное улучшение).
        var prepaidFlags int
        if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM message_boxes WHERE owner_user_id=$1
AND media #>> '{service_action,kind}'='star_gift'
AND media #>> '{service_action,star_gift,auction_acquired}'='true'
AND media #>> '{service_action,star_gift,prepaid_upgrade}'='true'
AND media #>> '{service_action,star_gift,can_upgrade}'='true'`, beta.ID).Scan(&prepaidFlags); err != nil {
                t.Fatalf("load award flags: %v", err)
        }
        if prepaidFlags == 0 {
                t.Fatalf("award message lost prepaid_upgrade/can_upgrade flags")
        }
        var winnerMsgID int
        if err := pool.QueryRow(ctx, `SELECT msg_id FROM peer_star_gifts WHERE gift_id=$1 AND owner_peer_id=$2 AND lifecycle_status='active'`,
                entry.Gift.ID, beta.ID).Scan(&winnerMsgID); err != nil {
                t.Fatalf("load winner aggregate: %v", err)
        }
        // Бесплатный апгрейд идёт в prepaid-форме (ChargeStars 0, FormID 0) —
        // права сверяются с активной ревизией (цена 0).
        upgraded, err := NewStarGiftUpgradeStore(pool, messages).UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                UserID: beta.ID, Ref: domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: beta.ID}, MsgID: winnerMsgID},
                RequirePrepaid: true, ChargeStars: 0, CommandKey: "free-upgrade-" + suffix, Date: now + 4,
        })
        if err != nil {
                t.Fatalf("free upgrade (revision price 0): %v", err)
        }
        if upgraded.Unique.ID == 0 {
                t.Fatalf("free upgrade produced no unique gift: %+v", upgraded)
        }
        // Повтор бесплатного апгрейда — детерминированный реплей той же квитанции.
        replayed, err := NewStarGiftUpgradeStore(pool, messages).UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                UserID: beta.ID, Ref: domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: beta.ID}, MsgID: winnerMsgID},
                RequirePrepaid: true, ChargeStars: 0, CommandKey: "free-upgrade-" + suffix, Date: now + 5,
        })
        if err != nil || !replayed.Duplicate || replayed.Unique.ID != upgraded.Unique.ID {
                t.Fatalf("free upgrade replay = %+v err %v", replayed, err)
        }
}

// Раунд 21: продление раунда при изменении топ-N в последние threshold секунд.
func TestStarGiftAuctionRoundExtensionPostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        now := int(time.Now().Unix())
        users := NewUserStore(pool)
        alpha := createTestUser(t, ctx, users, "+1884"+suffix+"01", "ExtAlpha", "")
        beta := createTestUser(t, ctx, users, "+1884"+suffix+"02", "ExtBeta", "")
        gamma := createTestUser(t, ctx, users, "+1884"+suffix+"03", "ExtGamma", "")
        alphaPeer := domain.Peer{Type: domain.PeerTypeUser, ID: alpha.ID}

        stars := NewStarsStore(pool)
        for _, user := range []domain.User{alpha, beta, gamma} {
                if _, _, err := stars.EnsureGrant(ctx, user.ID, 100000, now); err != nil {
                        t.Fatalf("grant stars: %v", err)
                }
        }

        gifts := NewStarGiftStore(pool)
        baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 950
        entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                Title: "AuctionExt " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
                AvailabilityTotal: 3, AvailabilityRemains: 3, GiftsPerRound: 1, AuctionStartDate: now - 10,
                AuctionSlug: "ext-" + suffix,
                Document:    collectibleTestDocument(baseDocumentID, "ext.tgs"),
                Blob:        collectibleTestBlob(baseDocumentID, "ext"), Animation: collectibleTestAnimation("ext.tgs"),
                Actor: "integration", CommandID: "ext-catalog-" + suffix,
        })
        if err != nil {
                t.Fatalf("create auction catalog: %v", err)
        }
        messages := NewMessageStore(pool)
        lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)
        // ensure создаёт строку аукциона лениво — материализуем её до админ-настройки.
        if _, err := lifecycle.StarGiftAuctionState(ctx, alpha.ID, entry.Gift.ID, "", now); err != nil {
                t.Fatalf("materialize auction row: %v", err)
        }
        // Окно продления: топ-2, +30 секунд в последние 60 секунд раунда.
        if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET round_extends=$2, next_round_at=$3 WHERE gift_id=$1`,
                entry.Gift.ID, `{"round_1":{"window":30,"top":2,"threshold":60}}`, now+40); err != nil {
                t.Fatalf("configure round extension: %v", err)
        }

        // Ставка альфы (остаток 40 <= threshold 60): вход в топ-2, но топ ещё
        // не менялся — первая ставка не продлевает раунд.
        stateA, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: alpha.ID,
                GiftID: entry.Gift.ID, Peer: alphaPeer, BidAmount: 100, FormID: 22001, Date: now})
        if err != nil {
                t.Fatalf("alpha bid: %v", err)
        }
        if stateA.NextRoundAt != now+40 {
                t.Fatalf("first bid must not extend: next_round_at = %d, want %d", stateA.NextRoundAt, now+40)
        }
        // Бета перебивает альфу и входит в топ — состав топа изменился → +30 сек.
        stateB, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: beta.ID,
                GiftID: entry.Gift.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: beta.ID}, BidAmount: 101, FormID: 22002, Date: now + 1})
        if err != nil {
                t.Fatalf("beta bid: %v", err)
        }
        if stateB.NextRoundAt != now+70 {
                t.Fatalf("top change must extend: next_round_at = %d, want %d", stateB.NextRoundAt, now+70)
        }
        if stateB.RoundExtendSeconds != 30 {
                t.Fatalf("round_extend_seconds = %d, want 30", stateB.RoundExtendSeconds)
        }
        // Гамма ставит цену ревизии (фиксированный минимум) ниже топа — топ-2
        // не изменился → продления нет.
        stateG, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: gamma.ID,
                GiftID: entry.Gift.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: gamma.ID}, BidAmount: 100, FormID: 22003, Date: now + 2})
        if err != nil {
                t.Fatalf("gamma low bid: %v", err)
        }
        if stateG.NextRoundAt != now+70 {
                t.Fatalf("bid outside top must not extend: next_round_at = %d, want %d", stateG.NextRoundAt, now+70)
        }
}
