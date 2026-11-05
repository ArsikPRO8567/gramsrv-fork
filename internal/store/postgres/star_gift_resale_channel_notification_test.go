package postgres

import (
        "context"
        "testing"
        "time"

        "telesrv/internal/domain"
)

// Раунд 24 (официальный дамп 198247): покупка коллекционного подарка на
// маркете сразу в канал — «помещение подарка на канал». Сервисное сообщение
// уходит от «Telegram» (777000) владельцу/админам канала с правом постить
// (saved=true, from_id=кто поместил, peer=канал, saved_id, окна
// capability-таймеров, без флага transferred); само-сообщения покупателя в
// «Избранное» больше нет.
func TestStarGiftResaleToChannelNotificationPostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        now := int(time.Now().Unix())
        users := NewUserStore(pool)
        actor := createTestUser(t, ctx, users, "+1884"+suffix+"01", "ResaleChannel", "")

        stars := NewStarsStore(pool)
        if _, _, err := stars.EnsureGrant(ctx, actor.ID, 100000, now); err != nil {
                t.Fatalf("grant stars: %v", err)
        }

        channelStore := NewChannelStore(pool)
        created, err := channelStore.CreateChannel(ctx, domain.CreateChannelRequest{
                CreatorUserID: actor.ID, Title: "Resale Source " + suffix, Megagroup: true, Date: now,
        })
        if err != nil {
                t.Fatalf("create source channel: %v", err)
        }
        createdTarget, err := channelStore.CreateChannel(ctx, domain.CreateChannelRequest{
                CreatorUserID: actor.ID, Title: "Resale Target " + suffix, Megagroup: true, Date: now,
        })
        if err != nil {
                t.Fatalf("create target channel: %v", err)
        }
        channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
        targetChannelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: createdTarget.Channel.ID}

        gifts := NewStarGiftStore(pool)
        baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 1100
        entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                Title: "ResaleChannel " + suffix, Stars: 50, ConvertStars: 20, Enabled: true, Limited: true,
                AvailabilityTotal: 5, AvailabilityRemains: 5,
                Document: collectibleTestDocument(baseDocumentID, "resale-channel.tgs"),
                Blob:     collectibleTestBlob(baseDocumentID, "resale-channel"), Animation: collectibleTestAnimation("resale-channel.tgs"),
                Actor: "integration", CommandID: "resale-channel-catalog-" + suffix,
        })
        if err != nil {
                t.Fatalf("create catalog: %v", err)
        }
        if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
                GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "resale-channel-" + suffix,
                Models: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectibleModel, Name: "M1", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestDocumentPtr(baseDocumentID+1, "rcm1.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "rcm1"), Animation: collectibleTestAnimationPtr("rcm1.tgs")},
                        {Kind: domain.StarGiftCollectibleModel, Name: "M2", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900,
                                Document: collectibleTestDocumentPtr(baseDocumentID+3, "rcm2.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "rcm2"), Animation: collectibleTestAnimationPtr("rcm2.tgs")},
                },
                Patterns: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectiblePattern, Name: "P1", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestPatternDocumentPtr(baseDocumentID+2, "rcp1.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "rcp1"), Animation: collectibleTestAnimationPtr("rcp1.tgs")},
                        {Kind: domain.StarGiftCollectiblePattern, Name: "P2", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900,
                                Document: collectibleTestPatternDocumentPtr(baseDocumentID+4, "rcp2.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+4, "rcp2"), Animation: collectibleTestAnimationPtr("rcp2.tgs")},
                },
                Backdrops: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "B1", BackdropID: 71, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "B2", BackdropID: 72, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900},
                },
                Actor: "integration", CommandID: "resale-channel-pool-" + suffix,
        }); err != nil {
                t.Fatalf("publish collectible pool: %v", err)
        }

        messages := NewMessageStore(pool)
        lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)
        upgrades := NewStarGiftUpgradeStore(pool, messages)

        purchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: actor.ID,
                To: channelPeer, GiftID: entry.Gift.ID, CommandKey: "resale-channel-purchase-" + suffix, Date: now})
        purchased, err := lifecycle.PurchaseStarGift(ctx, purchaseReq)
        if err != nil || purchased.Saved.Owner != channelPeer || purchased.Saved.SavedID <= 0 {
                t.Fatalf("purchase into source channel = %+v err %v", purchased, err)
        }
        upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                UserID: actor.ID, Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: purchased.Saved.SavedID},
                ChargeStars: 100, FormID: 21010, CommandKey: "resale-channel-upgrade-" + suffix, Date: now + 1,
        })
        if err != nil || upgraded.Unique.ID == 0 || upgraded.Unique.Owner != channelPeer {
                t.Fatalf("upgrade channel gift = %+v err %v", upgraded, err)
        }
        if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{ActorUserID: actor.ID,
                Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: upgraded.Saved.SavedID},
                Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 300}, Date: now + 2,
        }); err != nil {
                t.Fatalf("list channel collectible: %v", err)
        }

        resaleReq := domain.StarGiftResalePurchaseRequest{BuyerUserID: actor.ID, Slug: upgraded.Unique.Slug, To: targetChannelPeer,
                Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 300}, FormID: 21009,
                CommandKey: "resale-to-channel-" + suffix, Date: now + 3,
        }
        resold, err := lifecycle.PurchaseResaleStarGift(ctx, resaleReq)
        if err != nil || resold.Unique.Owner != targetChannelPeer || resold.Saved.Owner != targetChannelPeer ||
                // 100000 − 50 (покупка) − 100 (апгрейд) − 300 (маркет) = 99550.
                resold.Balance.Balance != 99550 {
                t.Fatalf("resale into target channel = %+v err %v", resold, err)
        }
        if resold.Send.SenderMessage.ID != 0 || resold.Send.RecipientMessage.ID != 0 {
                t.Fatalf("resale into channel must not send a private message: %+v", resold.Send)
        }

        // Само-сообщения покупателя о покупке в «Избранное» больше нет.
        var favoritesCopies int
        if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM message_boxes WHERE owner_user_id=$1 AND peer_id=$1
AND media #>> '{service_action,kind}'='star_gift_unique'
AND media #>> '{service_action,star_gift_unique,resale_amount,Amount}'='300'`, actor.ID).Scan(&favoritesCopies); err != nil {
                t.Fatalf("load resale favorites copies: %v", err)
        }
        if favoritesCopies != 0 {
                t.Fatalf("resale to channel must not post a buyer self-message to favorites, got %d", favoritesCopies)
        }

        // Владелец/админы целевого канала получили уведомление от «Telegram».
        var resaleNotifications, resaleDelivered int
        if err := pool.QueryRow(ctx, `SELECT COUNT(*),COUNT(*) FILTER (WHERE delivered_at>0 AND message_id>0)
FROM star_gift_channel_notification_jobs WHERE saved_gift_id=$1 AND action_kind='star_gift_unique'`,
                resold.Saved.SavedID).Scan(&resaleNotifications, &resaleDelivered); err != nil || resaleNotifications == 0 || resaleDelivered != resaleNotifications {
                t.Fatalf("resale-to-channel notification jobs=%d delivered=%d err=%v", resaleNotifications, resaleDelivered, err)
        }
        var resaleNotificationBoxes int
        if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM message_boxes m
JOIN star_gift_channel_notification_jobs j ON j.target_user_id=m.owner_user_id AND j.message_id=m.box_id
WHERE j.saved_gift_id=$1 AND j.action_kind='star_gift_unique' AND m.peer_id=$2
AND media #>> '{service_action,kind}'='star_gift_unique'
AND (media #>> '{service_action,star_gift_unique,from_user_id}')::bigint=$3
AND media #>> '{service_action,star_gift_unique,peer,Type}'='channel'
AND (media #>> '{service_action,star_gift_unique,peer,ID}')::bigint=$4
AND (media #>> '{service_action,star_gift_unique,saved_id}')::bigint=$1
AND media #>> '{service_action,star_gift_unique,saved}'='true'
AND media #>> '{service_action,star_gift_unique,transferred}' IS NULL`,
                resold.Saved.SavedID, domain.OfficialSystemUserID, actor.ID, targetChannelPeer.ID).Scan(&resaleNotificationBoxes); err != nil || resaleNotificationBoxes != 1 {
                t.Fatalf("resale-to-channel 777000 notification boxes=%d err=%v", resaleNotificationBoxes, err)
        }

        // Повторный запрос — идемпотентный реплей без дублей уведомлений.
        replay, err := lifecycle.PurchaseResaleStarGift(ctx, resaleReq)
        if err != nil || !replay.Duplicate || replay.Unique.ID != resold.Unique.ID {
                t.Fatalf("resale-to-channel replay = %+v err %v", replay, err)
        }
        if err := pool.QueryRow(ctx, `SELECT COUNT(*),COUNT(*) FILTER (WHERE delivered_at>0) FROM star_gift_channel_notification_jobs
WHERE saved_gift_id=$1 AND action_kind='star_gift_unique'`, resold.Saved.SavedID).Scan(&resaleNotifications, &resaleDelivered); err != nil ||
                resaleDelivered != resaleNotifications {
                t.Fatalf("resale-to-channel replay duplicated notifications jobs=%d delivered=%d err=%v", resaleNotifications, resaleDelivered, err)
        }
}
