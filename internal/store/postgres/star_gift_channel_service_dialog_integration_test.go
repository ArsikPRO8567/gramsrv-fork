package postgres

// Integration coverage for the channel-gift service-dialog contract:
//  1. the 777000-dialog notification carries the channel namespace
//     (peer_channel_id + saved_id) and never the user-namespace gift_msg_id,
//     so official clients build inputSavedStarGiftChat for upgrade/convert
//     and the ResolveUserMessageRef alias still resolves the dialog message;
//  2. prepaid channel purchases follow the official prepaid wire format
//     (inner gift.stars = pure price, outer action upgrade_stars = prepaid);
//  3. channel-owned transfers: the recipient copy is sent from 777000 with
//     from_id = channel, the owner receives a separate service message, and a
//     self-withdrawal produces exactly one message.

import (
        "context"
        "fmt"
        "testing"
        "time"

        "github.com/jackc/pgx/v5/pgxpool"

        "telesrv/internal/domain"
)

func TestStarGiftChannelGiftServiceDialogOpsPostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        now := int(time.Now().Unix())
        users := NewUserStore(pool)
        owner := createTestUser(t, ctx, users, "+1889"+suffix+"01", "ChannelOpsOwner", "")
        buyer := createTestUser(t, ctx, users, "+1889"+suffix+"02", "ChannelOpsBuyer", "")
        if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, buyer.ID, 100000, now); err != nil {
                t.Fatalf("grant buyer stars: %v", err)
        }
        if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, owner.ID, 100000, now); err != nil {
                t.Fatalf("grant owner stars: %v", err)
        }
        channelStore := NewChannelStore(pool)
        created, err := channelStore.CreateChannel(ctx, domain.CreateChannelRequest{
                CreatorUserID: owner.ID, Title: "Ops Channel " + suffix, Megagroup: true, Date: now,
        })
        if err != nil {
                t.Fatalf("create channel: %v", err)
        }
        channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}

        gifts := NewStarGiftStore(pool)
        baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 700
        entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                Title: "Ops Gift " + suffix, Stars: 60, ConvertStars: 25, Enabled: true, Limited: true,
                AvailabilityTotal: 10, AvailabilityRemains: 10,
                Document: collectibleTestDocument(baseDocumentID, "ops-gift.tgs"), Blob: collectibleTestBlob(baseDocumentID, "ops-gift"),
                Animation: collectibleTestAnimation("ops-gift.tgs"), Actor: "integration", CommandID: "ops-gift-" + suffix,
        })
        if err != nil {
                t.Fatalf("create catalog: %v", err)
        }
        if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
                GiftID: entry.Gift.ID, UpgradeStars: 40, SupplyTotal: 10, SlugPrefix: "ops-life-" + suffix,
                Models: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectibleModel, Name: "Ops Model", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestDocumentPtr(baseDocumentID+1, "ops-model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "ops-model"), Animation: collectibleTestAnimationPtr("ops-model.tgs")},
                        {Kind: domain.StarGiftCollectibleModel, Name: "Ops Model Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestDocumentPtr(baseDocumentID+3, "ops-model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "ops-model-two"), Animation: collectibleTestAnimationPtr("ops-model-two.tgs")},
                },
                Patterns: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectiblePattern, Name: "Ops Pattern", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestPatternDocumentPtr(baseDocumentID+2, "ops-pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "ops-pattern"), Animation: collectibleTestAnimationPtr("ops-pattern.tgs")},
                        {Kind: domain.StarGiftCollectiblePattern, Name: "Ops Pattern Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestPatternDocumentPtr(baseDocumentID+4, "ops-pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+4, "ops-pattern-two"), Animation: collectibleTestAnimationPtr("ops-pattern-two.tgs")},
                },
                Backdrops: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "Ops Backdrop", BackdropID: 90, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "Ops Backdrop Two", BackdropID: 91, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
                },
                Actor: "integration", CommandID: "ops-pool-" + suffix,
        }); err != nil {
                t.Fatalf("publish pool: %v", err)
        }

        messages := NewMessageStore(pool)
        lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
                StarsProceedsPermille: 900, TONProceedsPermille: 900,
        }))
        upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
                TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
        }))

        // --- Purchase #1: plain upgradable gift for the channel, from buyer.
        req1 := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: buyer.ID, To: channelPeer,
                GiftID: entry.Gift.ID, CommandKey: "ops-plain-" + suffix, Date: now + 1})
        purchased1, err := lifecycle.PurchaseStarGift(ctx, req1)
        if err != nil {
                t.Fatalf("plain channel purchase: %v", err)
        }
        if _, err := lifecycle.dispatchChannelStarGiftNotifications(ctx, now+2, 64, purchased1.Saved.ID); err != nil {
                t.Fatalf("dispatch plain notifications: %v", err)
        }

        // The owner's 777000-dialog notification message must carry the channel
        // namespace (peer_channel_id + saved_id) and must not carry gift_msg_id
        // (that is the user-namespace field).
        notificationBoxID := int64(0)
        if err := pool.QueryRow(ctx, `SELECT message_id FROM star_gift_channel_notification_jobs
WHERE saved_gift_id=$1 AND target_user_id=$2`, purchased1.Saved.ID, owner.ID).Scan(&notificationBoxID); err != nil {
                t.Fatalf("load notification job: %v", err)
        }
        var mediaJSON string
        if err := pool.QueryRow(ctx, `SELECT media::text FROM message_boxes WHERE owner_user_id=$1 AND box_id=$2`,
                owner.ID, notificationBoxID).Scan(&mediaJSON); err != nil {
                t.Fatalf("load notification media: %v", err)
        }
        media, err := decodeMessageMedia(mediaJSON)
        if err != nil || media == nil || media.ServiceAction == nil || media.ServiceAction.StarGift == nil {
                t.Fatalf("decode notification media: %v", err)
        }
        action := media.ServiceAction.StarGift
        if action.PeerChannelID != created.Channel.ID || action.SavedID != purchased1.Saved.SavedID {
                t.Fatalf("notification action lacks channel namespace: peer_channel_id=%d saved_id=%d", action.PeerChannelID, action.SavedID)
        }
        if action.GiftMsgID != 0 {
                t.Fatalf("notification action carries user-namespace gift_msg_id=%d (channel gift)", action.GiftMsgID)
        }
        t.Logf("plain action: can_upgrade=%v convert_stars=%d stars=%d upgrade_price=%d prepaid=%v upgrade_stars=%d saved=%v",
                action.CanUpgrade, action.ConvertStars, action.Stars, action.UpgradePriceStars, action.PrepaidUpgrade, action.UpgradeStars, action.Saved)

        // inputSavedStarGiftChat path: convert by {channel, saved_id}.
        chatRef := domain.SavedStarGiftRef{Owner: channelPeer, SavedID: purchased1.Saved.SavedID}
        if _, err := lifecycle.ConvertStarGift(ctx, domain.StarGiftConvertRequest{ActorUserID: owner.ID, Ref: chatRef, Date: now + 3}); err != nil {
                t.Fatalf("convert by chat ref: %v", err)
        }

        // --- Purchase #2: gift with prepaid upgrade; upgrade via prepaid path.
        req2 := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: buyer.ID, To: channelPeer,
                GiftID: entry.Gift.ID, IncludeUpgrade: true, CommandKey: "ops-prepaid-" + suffix, Date: now + 4})
        purchased2, err := lifecycle.PurchaseStarGift(ctx, req2)
        if err != nil {
                t.Fatalf("prepaid channel purchase: %v", err)
        }
        if _, err := lifecycle.dispatchChannelStarGiftNotifications(ctx, now+5, 64, purchased2.Saved.ID); err != nil {
                t.Fatalf("dispatch prepaid notifications: %v", err)
        }
        var prepaidBoxID int64
        var prepaidMediaJSON string
        if err := pool.QueryRow(ctx, `SELECT j.message_id, m.media::text FROM star_gift_channel_notification_jobs j
JOIN message_boxes m ON m.owner_user_id=j.target_user_id AND m.box_id=j.message_id
WHERE j.saved_gift_id=$1 AND j.target_user_id=$2`, purchased2.Saved.ID, owner.ID).Scan(&prepaidBoxID, &prepaidMediaJSON); err != nil {
                t.Fatalf("load prepaid notification: %v", err)
        }
        prepaidMedia, err := decodeMessageMedia(prepaidMediaJSON)
        if err != nil || prepaidMedia == nil || prepaidMedia.ServiceAction == nil || prepaidMedia.ServiceAction.StarGift == nil {
                t.Fatalf("decode prepaid media: %v", err)
        }
        prepaidAction := prepaidMedia.ServiceAction.StarGift
        t.Logf("prepaid action: can_upgrade=%v convert_stars=%d stars=%d upgrade_price=%d prepaid=%v upgrade_stars=%d gift_msg_id=%d peer=%d saved=%d",
                prepaidAction.CanUpgrade, prepaidAction.ConvertStars, prepaidAction.Stars, prepaidAction.UpgradePriceStars,
                prepaidAction.PrepaidUpgrade, prepaidAction.UpgradeStars, prepaidAction.GiftMsgID, prepaidAction.PeerChannelID, prepaidAction.SavedID)
        if prepaidAction.PeerChannelID != created.Channel.ID || prepaidAction.SavedID != purchased2.Saved.SavedID {
                t.Fatalf("prepaid notification action lacks channel namespace: %+v", prepaidAction)
        }
        // Официальный wire-формат предоплаты: внутренний stars — чистая цена
        // подарка, внешняя upgrade_stars — сумма предоплаты (цена ревизии
        // коллекции = 40, каталог несёт Stars=60).
        if prepaidAction.Stars != entry.Gift.Stars || prepaidAction.UpgradeStars != 40 {
                t.Fatalf("prepaid action price format wrong: stars=%d (want %d) upgrade_stars=%d (want 40)",
                        prepaidAction.Stars, entry.Gift.Stars, prepaidAction.UpgradeStars)
        }
        if prepaidAction.GiftMsgID != 0 {
                t.Fatalf("prepaid action carries gift_msg_id=%d", prepaidAction.GiftMsgID)
        }

        // inputSavedStarGiftUser path with the 777000-dialog message id (the
        // fallback official clients use when the peer/saved_id pair is absent).
        userRef, found, err := gifts.ResolveUserMessageRef(ctx, owner.ID, int(prepaidBoxID))
        if err != nil || !found {
                t.Fatalf("resolve user msg ref for prepaid notification: found=%v err=%v", found, err)
        }
        if userRef.Owner != channelPeer || userRef.SavedID != purchased2.Saved.SavedID {
                t.Fatalf("user msg ref = %+v", userRef)
        }
        upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                UserID: owner.ID, Ref: userRef, RequirePrepaid: true, ChargeStars: 0, FormID: 0,
                CommandKey: "ops-prepaid-upgrade-" + suffix, Date: now + 6,
        })
        if err != nil {
                t.Fatalf("prepaid upgrade by user-msg ref: %v", err)
        }
        if upgraded.Saved.UniqueGiftID == 0 {
                t.Fatalf("prepaid upgrade did not mint collectible: %+v", upgraded.Saved)
        }

        // --- Purchase #3: upgrade by the chat ref through the paid-form path.
        req3 := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: buyer.ID, To: channelPeer,
                GiftID: entry.Gift.ID, CommandKey: "ops-paid-upgrade-" + suffix, Date: now + 7})
        purchased3, err := lifecycle.PurchaseStarGift(ctx, req3)
        if err != nil {
                t.Fatalf("paid channel purchase: %v", err)
        }
        if _, err := lifecycle.dispatchChannelStarGiftNotifications(ctx, now+8, 64, purchased3.Saved.ID); err != nil {
                t.Fatalf("dispatch paid notifications: %v", err)
        }
        chatRef3 := domain.SavedStarGiftRef{Owner: channelPeer, SavedID: purchased3.Saved.SavedID}
        upgraded3, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                UserID: owner.ID, Ref: chatRef3, ChargeStars: 40, FormID: 777777,
                CommandKey: "ops-paid-upgrade-run-" + suffix, Date: now + 9,
        })
        if err != nil {
                t.Fatalf("paid upgrade by chat ref: %v", err)
        }
        if upgraded3.Saved.UniqueGiftID == 0 {
                t.Fatalf("paid upgrade did not mint collectible: %+v", upgraded3.Saved)
        }

        fmt.Println("OK: channel gift convert/upgrade flows verified")
}

func TestStarGiftChannelGiftTransferServiceMessagesPostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        now := int(time.Now().Unix())
        users := NewUserStore(pool)
        owner := createTestUser(t, ctx, users, "+1890"+suffix+"01", "TransferOwner", "")
        recipient := createTestUser(t, ctx, users, "+1890"+suffix+"02", "TransferRecipient", "")
        if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, owner.ID, 100000, now); err != nil {
                t.Fatalf("grant owner stars: %v", err)
        }
        channelStore := NewChannelStore(pool)
        created, err := channelStore.CreateChannel(ctx, domain.CreateChannelRequest{
                CreatorUserID: owner.ID, Title: "Transfer Channel " + suffix, Megagroup: true, Date: now,
        })
        if err != nil {
                t.Fatalf("create channel: %v", err)
        }
        channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}

        gifts := NewStarGiftStore(pool)
        baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 900
        entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
                Title: "Transfer Gift " + suffix, Stars: 70, ConvertStars: 30, Enabled: true,
                Document: collectibleTestDocument(baseDocumentID, "tr-gift.tgs"), Blob: collectibleTestBlob(baseDocumentID, "tr-gift"),
                Animation: collectibleTestAnimation("tr-gift.tgs"), Actor: "integration", CommandID: "tr-gift-" + suffix,
        })
        if err != nil {
                t.Fatalf("create catalog: %v", err)
        }
        if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
                GiftID: entry.Gift.ID, UpgradeStars: 20, SupplyTotal: 5, SlugPrefix: "tr-life-" + suffix,
                Models: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectibleModel, Name: "Tr Model", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestDocumentPtr(baseDocumentID+1, "tr-model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "tr-model"), Animation: collectibleTestAnimationPtr("tr-model.tgs")},
                        {Kind: domain.StarGiftCollectibleModel, Name: "Tr Model Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestDocumentPtr(baseDocumentID+3, "tr-model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "tr-model-two"), Animation: collectibleTestAnimationPtr("tr-model-two.tgs")},
                },
                Patterns: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectiblePattern, Name: "Tr Pattern", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestPatternDocumentPtr(baseDocumentID+2, "tr-pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "tr-pattern"), Animation: collectibleTestAnimationPtr("tr-pattern.tgs")},
                        {Kind: domain.StarGiftCollectiblePattern, Name: "Tr Pattern Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
                                Document: collectibleTestPatternDocumentPtr(baseDocumentID+4, "tr-pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+4, "tr-pattern-two"), Animation: collectibleTestAnimationPtr("tr-pattern-two.tgs")},
                },
                Backdrops: []domain.StarGiftCollectibleAttribute{
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "Tr Backdrop", BackdropID: 92, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
                        {Kind: domain.StarGiftCollectibleBackdrop, Name: "Tr Backdrop Two", BackdropID: 93, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
                },
                Actor: "integration", CommandID: "tr-pool-" + suffix,
        }); err != nil {
                t.Fatalf("publish pool: %v", err)
        }

        messages := NewMessageStore(pool)
        lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
                StarsProceedsPermille: 900, TONProceedsPermille: 900,
        }))
        upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
                TransferStars: 0, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
        }))

        // Покупка от владельца в свой канал и немедленное улучшение до
        // коллекционного (передавать можно только уникальные подарки).
        req := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: owner.ID, To: channelPeer,
                GiftID: entry.Gift.ID, CommandKey: "tr-purchase-" + suffix, Date: now + 1})
        purchased, err := lifecycle.PurchaseStarGift(ctx, req)
        if err != nil {
                t.Fatalf("channel purchase: %v", err)
        }
        if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: purchased.Saved.SavedID},
                ChargeStars: 20, FormID: 424242, CommandKey: "tr-upgrade-" + suffix, Date: now + 2,
        }); err != nil {
                t.Fatalf("upgrade: %v", err)
        }

        // --- 1. Передача подарка канала пользователю: сообщение получателю — от
        // 777000 с from_id = канал; владельцу — отдельное сервисное сообщение с
        // peer = получатель.
        ownerBefore, err := countSystemDialogBoxes(ctx, pool, owner.ID)
        if err != nil {
                t.Fatal(err)
        }
        transfer, err := lifecycle.TransferStarGift(ctx, domain.StarGiftTransferRequest{
                ActorUserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: purchased.Saved.SavedID},
                To:         domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
                CommandKey: "tr-to-user-" + suffix, Date: now + 3,
        })
        if err != nil {
                t.Fatalf("transfer to user: %v", err)
        }
        if transfer.Send.RecipientMessage.OwnerUserID != recipient.ID || transfer.Send.SenderMessage.OwnerUserID != domain.OfficialSystemUserID {
                t.Fatalf("transfer message parties: sender=%d recipient=%d",
                        transfer.Send.SenderMessage.OwnerUserID, transfer.Send.RecipientMessage.OwnerUserID)
        }
        var recipientAction domain.MessageMedia
        if err := pool.QueryRow(ctx, `SELECT media FROM message_boxes WHERE owner_user_id=$1 AND box_id=$2`,
                recipient.ID, transfer.Send.RecipientMessage.ID).Scan(&recipientAction); err != nil {
                t.Fatalf("load recipient media: %v", err)
        }
        if recipientAction.ServiceAction == nil || recipientAction.ServiceAction.StarGiftUnique == nil ||
                recipientAction.ServiceAction.StarGiftUnique.FromPeer != channelPeer ||
                recipientAction.ServiceAction.StarGiftUnique.Peer.Type != "" {
                t.Fatalf("recipient transfer action wrong: %+v", recipientAction.ServiceAction)
        }
        ownerAfter, err := countSystemDialogBoxes(ctx, pool, owner.ID)
        if err != nil {
                t.Fatal(err)
        }
        if ownerAfter-ownerBefore != 1 {
                t.Fatalf("owner transfer notification delta = %d, want 1", ownerAfter-ownerBefore)
        }
        var ownerAction domain.MessageMedia
        if err := pool.QueryRow(ctx, `SELECT media FROM message_boxes
WHERE owner_user_id=$1 AND message_sender_id=$2 AND peer_id=$2 AND peer_type='user'
ORDER BY box_id DESC LIMIT 1`, owner.ID, domain.OfficialSystemUserID).Scan(&ownerAction); err != nil {
                t.Fatalf("load owner notification: %v", err)
        }
        wantOwnerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}
        if ownerAction.ServiceAction == nil || ownerAction.ServiceAction.StarGiftUnique == nil ||
                ownerAction.ServiceAction.StarGiftUnique.FromPeer != channelPeer ||
                ownerAction.ServiceAction.StarGiftUnique.Peer != wantOwnerPeer {
                t.Fatalf("owner transfer notification action wrong: %+v", ownerAction.ServiceAction)
        }

        // --- 2. Вторая покупка + передача самому владельцу (вывод в свой профиль):
        // ровно одно сообщение — «{канал} передал(а) Вам подарок!».
        req2 := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: owner.ID, To: channelPeer,
                GiftID: entry.Gift.ID, CommandKey: "tr-purchase2-" + suffix, Date: now + 4})
        purchased2, err := lifecycle.PurchaseStarGift(ctx, req2)
        if err != nil {
                t.Fatalf("second channel purchase: %v", err)
        }
        if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
                UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: purchased2.Saved.SavedID},
                ChargeStars: 20, FormID: 424243, CommandKey: "tr-upgrade2-" + suffix, Date: now + 5,
        }); err != nil {
                t.Fatalf("second upgrade: %v", err)
        }
        before, err := countSystemDialogBoxes(ctx, pool, owner.ID)
        if err != nil {
                t.Fatal(err)
        }
        if _, err := lifecycle.TransferStarGift(ctx, domain.StarGiftTransferRequest{
                ActorUserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: purchased2.Saved.SavedID},
                To:         domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
                CommandKey: "tr-to-self-" + suffix, Date: now + 6,
        }); err != nil {
                t.Fatalf("transfer to owner: %v", err)
        }
        after, err := countSystemDialogBoxes(ctx, pool, owner.ID)
        if err != nil {
                t.Fatal(err)
        }
        if after-before != 1 {
                t.Fatalf("self-withdrawal produced %d new 777000 boxes, want 1", after-before)
        }
        fmt.Println("OK: channel gift transfer service messages verified")
}

func countSystemDialogBoxes(ctx context.Context, pool *pgxpool.Pool, ownerID int64) (int, error) {
        var n int
        err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM message_boxes
WHERE owner_user_id=$1 AND message_sender_id=$2 AND peer_id=$2 AND peer_type='user'`,
                ownerID, domain.OfficialSystemUserID).Scan(&n)
        return n, err
}
