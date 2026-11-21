package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// awardMediaProbe разбирает media сервисного сообщения с блоком StarGift
// (private_messages.media -> service_action.star_gift, snake_case-ключи).
type awardMediaProbe struct {
	ServiceAction *struct {
		StarGift *struct {
			Stars             int64 `json:"stars"`
			UpgradeStars      int64 `json:"upgrade_stars"`
			UpgradePriceStars int64 `json:"upgrade_price_stars"`
		} `json:"star_gift"`
	} `json:"service_action"`
}

// Раунд 36, репродукция: «стоимость выданного аукционного подарка всегда на 1
// больше выигрышной ставки». Прогон полного цикла: ставка → граница раунда →
// settle (award) → dispatch (создание saved-подарка) → проверка bid_amount в
// star_gift_auction_acquired и paid_stars в peer_star_gifts.
func TestStarGiftAuctionAwardAmountRepro(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())

	users := NewUserStore(pool)
	alpha := createTestUser(t, ctx, users, "+1892"+suffix+"01", "AwardAlpha", "")
	alphaPeer := domain.Peer{Type: domain.PeerTypeUser, ID: alpha.ID}

	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, alpha.ID, 1000000, now); err != nil {
		t.Fatalf("grant stars: %v", err)
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 950
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "AwardAmount " + suffix, Stars: 500, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 2, AvailabilityRemains: 2, GiftsPerRound: 1, AuctionStartDate: now - 5,
		AuctionSlug: "awardamt-" + suffix,
		Document:    collectibleTestDocument(baseDocumentID, "award.tgs"),
		Blob:        collectibleTestBlob(baseDocumentID, "award"), Animation: collectibleTestAnimation("award.tgs"),
		Actor: "integration", CommandID: "award-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	giftID := entry.Gift.ID

	lifecycle := NewStarGiftLifecycleStore(pool, NewMessageStore(pool), 1_000_000)
	// Ставка ровно 777 (raise_min_bid по умолчанию true, минимум = 500).
	state, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{
		UserID: alpha.ID, GiftID: giftID, Peer: alphaPeer, BidAmount: 777, FormID: 39001, Date: now})
	if err != nil {
		t.Fatalf("bid: %v", err)
	}
	if state.UserState.BidAmount != 777 {
		t.Fatalf("user bid = %d, want 777", state.UserState.BidAmount)
	}

	// Завершаем раунд: граница (start+3600) уже прошла — settle при следующем
	// чтении состояния разыграет раунд и запишет acquired.
	if err := lifecycle.settleStarGiftAuction(ctx, giftID, now+3700); err != nil {
		t.Fatalf("settle: %v", err)
	}
	var acquiredBid int64
	var savedGiftID *int64
	if err := pool.QueryRow(ctx, `SELECT bid_amount,saved_gift_id FROM star_gift_auction_acquired WHERE gift_id=$1 AND bidder_user_id=$2`,
		giftID, alpha.ID).Scan(&acquiredBid, &savedGiftID); err != nil {
		t.Fatalf("acquired row: %v", err)
	}
	if acquiredBid != 777 {
		t.Fatalf("acquired bid_amount = %d, want 777", acquiredBid)
	}
	if savedGiftID == nil {
		// Полная выдача: dispatch создаёт saved-подарок и сервисные сообщения.
		if err := lifecycle.dispatchStarGiftAuctionAwards(ctx, giftID); err != nil {
			t.Fatalf("dispatch awards: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT saved_gift_id FROM star_gift_auction_acquired WHERE gift_id=$1 AND bidder_user_id=$2`,
			giftID, alpha.ID).Scan(&savedGiftID); err != nil || savedGiftID == nil {
			t.Fatalf("saved gift after dispatch: id=%v err=%v", savedGiftID, err)
		}
		// Цена на карточке профиля: paid_stars ровно = выигрышной ставке.
		var paidStars int64
		if err := pool.QueryRow(ctx, `SELECT COALESCE(paid_stars,0) FROM peer_star_gifts WHERE id=$1`, *savedGiftID).Scan(&paidStars); err != nil {
			t.Fatalf("paid stars: %v", err)
		}
		if paidStars != 777 {
			t.Fatalf("saved gift paid_stars = %d, want 777 (winning bid)", paidStars)
		}
		// Блок StarGift в сервисном сообщении победителя (от 777000):
		// stars ровно = выигрышной ставке, долей улучшения нет.
		var mediaJSON string
		if err := pool.QueryRow(ctx, `SELECT media::text FROM private_messages
WHERE sender_user_id=$1 AND recipient_user_id=$2 AND media::text LIKE '%"star_gift"%'
ORDER BY id DESC LIMIT 1`, domain.OfficialSystemUserID, alpha.ID).Scan(&mediaJSON); err != nil {
			t.Fatalf("load winner message: %v", err)
		}
		var winner awardMediaProbe
		if err := json.Unmarshal([]byte(mediaJSON), &winner); err != nil {
			t.Fatalf("unmarshal winner media: %v", err)
		}
		if winner.ServiceAction == nil || winner.ServiceAction.StarGift == nil {
			t.Fatalf("no star gift block in winner media: %s", mediaJSON)
		}
		if winner.ServiceAction.StarGift.Stars != 777 || winner.ServiceAction.StarGift.UpgradeStars != 0 {
			t.Fatalf("winner message stars=%d upgrade=%d, want 777/0",
				winner.ServiceAction.StarGift.Stars, winner.ServiceAction.StarGift.UpgradeStars)
		}
	}

	// Минимальная ставка следующего раунда в состоянии: после разыгранного
	// раунда с победителем минимум откатывается к цене ревизии (500), а не к
	// «топ-1 + 1» — топ больше не активен.
	stateAfter, err := lifecycle.StarGiftAuctionState(ctx, alpha.ID, giftID, "", now+3701)
	if err != nil {
		t.Fatalf("state after: %v", err)
	}
	if stateAfter.MinBidAmount != 500 {
		t.Fatalf("state min bid after award = %d, want 500 (revision price)", stateAfter.MinBidAmount)
	}
}

// Раунд 36: копия-получатель (обычное сервисное сообщение «вы купили подарок»)
// должна показывать стоимость РОВНО равной выигрышной ставке: клиент считает
// её как gift.stars + upgrade_stars (StarGiftSheet Gift2Value, tdesktop
// starsBid). Бесплатное улучшение (ревизия с ценой 0) делит ставку на доли:
// база = ставка − 1, upgrade_stars = 1, сумма = ставка.
func TestStarGiftAuctionAwardValueSplitPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())

	users := NewUserStore(pool)
	alpha := createTestUser(t, ctx, users, "+1893"+suffix+"01", "SplitAlpha", "")
	alphaPeer := domain.Peer{Type: domain.PeerTypeUser, ID: alpha.ID}
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, alpha.ID, 1000000, now); err != nil {
		t.Fatalf("grant stars: %v", err)
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 960
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Split " + suffix, Stars: 500, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 2, AvailabilityRemains: 2, GiftsPerRound: 1, AuctionStartDate: now - 5,
		AuctionSlug: "split-" + suffix,
		Document:    collectibleTestDocument(baseDocumentID, "split.tgs"),
		Blob:        collectibleTestBlob(baseDocumentID, "split"), Animation: collectibleTestAnimation("split.tgs"),
		Actor: "integration", CommandID: "split-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	giftID := entry.Gift.ID
	// Ревизия улучшений с нулевой ценой — бесплатное улучшение аукционного
	// экземпляра (раунд 21), источник прежнего бага «+1».
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: giftID, UpgradeStars: 0, SupplyTotal: 5, SlugPrefix: "split-life-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "M1", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+1, "sm1.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "sm1"), Animation: collectibleTestAnimationPtr("sm1.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "M2", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900,
				Document: collectibleTestDocumentPtr(baseDocumentID+2, "sm2.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "sm2"), Animation: collectibleTestAnimationPtr("sm2.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "P1", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+3, "sp1.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "sp1"), Animation: collectibleTestAnimationPtr("sp1.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "P2", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+4, "sp2.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+4, "sp2"), Animation: collectibleTestAnimationPtr("sp2.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "B1", BackdropID: 1, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "B2", BackdropID: 2, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 900,
				CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111},
		},
		Actor: "integration", CommandID: "split-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish collectible pool: %v", err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)
	if _, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{
		UserID: alpha.ID, GiftID: giftID, Peer: alphaPeer, BidAmount: 777, FormID: 39101, Date: now}); err != nil {
		t.Fatalf("bid: %v", err)
	}
	if err := lifecycle.settleStarGiftAuction(ctx, giftID, now+3700); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if err := lifecycle.dispatchStarGiftAuctionAwards(ctx, giftID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Копия-получатель: сервисное сообщение от имени победителя — его блок
	// StarGift должен нести stars = 776 и upgrade_stars = 1 (сумма 777).
	// Отправитель копии-получателя = сам победитель (alpha), у копии-победителя
	// отправитель 777000, поэтому фильтр по sender однозначен.
	var mediaJSON string
	if err := pool.QueryRow(ctx, `SELECT media::text FROM private_messages
WHERE sender_user_id=$1 AND recipient_user_id=$1 AND media::text LIKE '%"star_gift"%'
ORDER BY id DESC LIMIT 1`, alpha.ID).Scan(&mediaJSON); err != nil {
		t.Fatalf("load award message: %v", err)
	}
	var probe awardMediaProbe
	if err := json.Unmarshal([]byte(mediaJSON), &probe); err != nil {
		t.Fatalf("unmarshal media: %v", err)
	}
	if probe.ServiceAction == nil || probe.ServiceAction.StarGift == nil {
		t.Fatalf("no star gift block in media: %s", mediaJSON)
	}
	gift := probe.ServiceAction.StarGift
	// Официальная формула стоимости: gift.stars + upgrade_stars = ставка.
	if gift.Stars+gift.UpgradeStars != 777 {
		t.Fatalf("award value = stars %d + upgrade_stars %d, want sum 777", gift.Stars, gift.UpgradeStars)
	}
	// Бесплатное улучшение: внешняя доля положительна (кнопка «Улучшить
	// бесплатно») и цена платного улучшения в блоке тоже равна доле.
	if gift.UpgradeStars != 1 || gift.UpgradePriceStars != 1 {
		t.Fatalf("upgrade split = %d/%d, want 1/1", gift.UpgradeStars, gift.UpgradePriceStars)
	}
}
