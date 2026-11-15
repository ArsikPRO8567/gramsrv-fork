package postgres

import (
        "context"
        "testing"
        "time"

        "telesrv/internal/domain"
)

// Раунд 31: paid_stars (цена выигрышной ставки аукциона) сохраняется на
// экземпляре подарка и читается проекциями профиля; настройки вайтлиста,
// переданные вместе с ревизией каталога, апсертятся в gift_whitelists в той же
// транзакции и проецируются GetWhitelistInfo/GetWhitelistInfos как psql-строки.
func TestStarGiftPaidStarsAndWhitelistWritePostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        now := int(time.Now().Unix())

        users := NewUserStore(pool)
        owner := createTestUser(t, ctx, users, "+1884"+suffix+"01", "PaidStarsOwner", "")
        sender := createTestUser(t, ctx, users, "+1884"+suffix+"02", "PaidStarsSender", "")

        gifts := NewStarGiftStore(pool)
        baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 950
        // Аукционная ревизия: констрейнты auction_check/supply_check требуют
        // limited=true, slug, rounds, дату старта и положительную поставку.
        entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                Title: "PaidStars " + suffix, Stars: 500, Enabled: true, Limited: true, Auction: true,
                AvailabilityTotal: 3, AvailabilityRemains: 3, GiftsPerRound: 1, AuctionStartDate: now - 5,
                AuctionSlug: "paid-" + suffix,
                Document:    collectibleTestDocument(baseDocumentID, "paid.tgs"),
                Blob:        collectibleTestBlob(baseDocumentID, "paid"), Animation: collectibleTestAnimation("paid.tgs"),
                Actor: "integration", CommandID: "paid-catalog-" + suffix,
                // Настройки вайтлиста из админки пишутся вместе с ревизией.
                Whitelist: &domain.GiftWhitelistWrite{
                        AllowedBuyers: []string{" 777001 ", "", "777001", "777002"}, Hidden: true, Numbered: true,
                        DisallowUpgrades: true, DisallowResell: true, Apply: true,
                },
        })
        if err != nil {
                t.Fatalf("create auction catalog revision: %v", err)
        }

        // Выигрышная ставка сохраняется на экземпляре: карточка профиля показывает
        // её вместо фиксированной цены каталога (500).
        _, err = gifts.Create(ctx, domain.SavedStarGift{
                Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, FromUserID: sender.ID,
                GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID, MsgID: 9100 + int(owner.ID%1000),
                Date: now, ConvertStars: 0, GiftNum: 1, PaidStars: 650,
        })
        if err != nil {
                t.Fatalf("create saved gift with paid stars: %v", err)
        }
        page, err := gifts.ListByOwner(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, false, "", 10)
        if err != nil || len(page.Gifts) != 1 {
                t.Fatalf("list by owner: page=%+v err=%v", page, err)
        }
        if page.Gifts[0].PaidStars != 650 || page.Gifts[0].GiftNum != 1 {
                t.Fatalf("paid_stars round-trip failed: paid=%d num=%d", page.Gifts[0].PaidStars, page.Gifts[0].GiftNum)
        }
        byRef, found, err := gifts.GetByRef(ctx, domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, MsgID: page.Gifts[0].MsgID})
        if err != nil || !found {
                t.Fatalf("get by ref: found=%v err=%v", found, err)
        }
        if byRef.PaidStars != 650 {
                t.Fatalf("GetByRef paid_stars = %d, want 650", byRef.PaidStars)
        }

        // Вайтлист: строка записана ревизией, проекция совпадает с psql-семантикой.
        whitelist, err := gifts.GetWhitelistInfo(ctx, entry.Gift.ID, 777002)
        if err != nil {
                t.Fatalf("get whitelist info: %v", err)
        }
        if !whitelist.HasWhitelist || !whitelist.Allowed || !whitelist.Hidden || !whitelist.Numbered ||
                !whitelist.DisallowUpgrades || !whitelist.DisallowResell || !whitelist.ApplyWhitelist {
                t.Fatalf("whitelist projection for allowed buyer: %+v", whitelist)
        }
        stranger, err := gifts.GetWhitelistInfo(ctx, entry.Gift.ID, 999999)
        if err != nil {
                t.Fatalf("get whitelist info stranger: %v", err)
        }
        if !stranger.HasWhitelist || stranger.Allowed || !stranger.Hidden {
                t.Fatalf("whitelist projection for stranger: %+v", stranger)
        }
        batch, err := gifts.GetWhitelistInfos(ctx, []int64{entry.Gift.ID}, 777001)
        if err != nil || len(batch) != 1 {
                t.Fatalf("whitelist batch: %v %v", batch, err)
        }
        if !batch[entry.Gift.ID].Allowed {
                t.Fatalf("batch projection must allow whitelisted buyer: %+v", batch[entry.Gift.ID])
        }

        // Ревизия без блока вайтлиста не должна трогать существующую строку
        // (psql-настройки сохраняются при переиздании подарка).
        if _, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                GiftID: entry.Gift.ID, Title: "PaidStars " + suffix + " v2", Stars: 600, Enabled: true,
                AvailabilityTotal: 3, AvailabilityRemains: 3, GiftsPerRound: 1, AuctionStartDate: now - 5,
                AuctionSlug: "paid-" + suffix, Auction: true, Limited: true,
                Document:    collectibleTestDocument(baseDocumentID+10, "paid2.tgs"),
                Blob:        collectibleTestBlob(baseDocumentID+10, "paid2"), Animation: collectibleTestAnimation("paid2.tgs"),
                Actor: "integration", CommandID: "paid-catalog-v2-" + suffix,
        }); err != nil {
                t.Fatalf("create second revision: %v", err)
        }
        kept, err := gifts.GetWhitelistInfo(ctx, entry.Gift.ID, 777002)
        if err != nil {
                t.Fatalf("get whitelist after revision: %v", err)
        }
        if !kept.HasWhitelist || !kept.Numbered {
                t.Fatalf("revision without whitelist block must keep existing row: %+v", kept)
        }
}
