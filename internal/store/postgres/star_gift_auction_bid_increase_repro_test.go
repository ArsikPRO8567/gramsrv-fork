package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// Репродукция: «пользователь не может увеличить ставку в действующем раунде —
// сервер вернёт STARGIFT_INVALID». Активная ставка в текущем раунде должна
// увеличиваться через update_bid в обоих режимах минимума (raise_min_bid true
// и false) без STARGIFT_INVALID.
func TestStarGiftAuctionBidIncreaseActiveRoundRepro(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	alpha := createTestUser(t, ctx, users, "+1891"+suffix+"01", "BidIncAlpha", "")
	alphaPeer := domain.Peer{Type: domain.PeerTypeUser, ID: alpha.ID}

	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, alpha.ID, 100000, now); err != nil {
		t.Fatalf("grant stars: %v", err)
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 940
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "BidInc " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 2, AvailabilityRemains: 2, GiftsPerRound: 1, AuctionStartDate: now - 10,
		AuctionSlug: "bidinc-" + suffix,
		Document:    collectibleTestDocument(baseDocumentID, "bidinc.tgs"),
		Blob:        collectibleTestBlob(baseDocumentID, "bidinc"), Animation: collectibleTestAnimation("bidinc.tgs"),
		Actor: "integration", CommandID: "bidinc-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create auction catalog: %v", err)
	}
	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)
	if _, err := lifecycle.StarGiftAuctionState(ctx, alpha.ID, entry.Gift.ID, "", now); err != nil {
		t.Fatalf("materialize auction row: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET raise_min_bid=true WHERE gift_id=$1`, entry.Gift.ID); err != nil {
		t.Fatalf("enable raise_min_bid: %v", err)
	}

	// Первая ставка в раунде 1.
	state, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: alpha.ID,
		GiftID: entry.Gift.ID, Peer: alphaPeer, BidAmount: 100, FormID: 22001, Date: now})
	if err != nil || state.UserState.BidAmount != 100 || !state.UserState.BidActive {
		t.Fatalf("alpha first bid = %+v err %v", state.UserState, err)
	}

	// Увеличение АКТИВНОЙ ставки в том же раунде: update_bid=true, 100 -> 150.
	state2, balance2, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: alpha.ID,
		GiftID: entry.Gift.ID, Peer: alphaPeer, BidAmount: 150, FormID: 22002, Date: now + 1, UpdateBid: true})
	if err != nil {
		t.Fatalf("increase own active bid in active round: %v", err)
	}
	if state2.UserState.BidAmount != 150 || !state2.UserState.BidActive {
		t.Fatalf("after increase = %+v, want amount 150 active", state2.UserState)
	}
	if balance2.Balance != 100000-150 {
		t.Fatalf("balance after increase = %d, want %d (delta debit only)", balance2.Balance, 100000-150)
	}

	// Повторное увеличение той же активной ставки.
	state3, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: alpha.ID,
		GiftID: entry.Gift.ID, Peer: alphaPeer, BidAmount: 200, FormID: 22003, Date: now + 2, UpdateBid: true})
	if err != nil {
		t.Fatalf("second increase of own active bid: %v", err)
	}
	if state3.UserState.BidAmount != 200 {
		t.Fatalf("after second increase = %d, want 200", state3.UserState.BidAmount)
	}

	// Несменяемый минимум (raise_min_bid=false): ставки ниже лидирующей
	// разрешены; увеличение собственной активной ставки тоже должно работать.
	if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET raise_min_bid=false,min_bid_amount=100 WHERE gift_id=$1`, entry.Gift.ID); err != nil {
		t.Fatalf("disable raise_min_bid: %v", err)
	}
	if _, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: alpha.ID,
		GiftID: entry.Gift.ID, Peer: alphaPeer, BidAmount: 250, FormID: 22004, Date: now + 3, UpdateBid: true}); err != nil {
		t.Fatalf("increase active bid in fixed-minimum mode: %v", err)
	}
	var storedAmount int64
	if err := pool.QueryRow(ctx, `SELECT amount FROM star_gift_auction_bids WHERE gift_id=$1 AND bidder_user_id=$2`,
		entry.Gift.ID, alpha.ID).Scan(&storedAmount); err != nil {
		t.Fatalf("load bid amount: %v", err)
	}
	if storedAmount != 250 {
		t.Fatalf("stored bid amount = %d, want 250", storedAmount)
	}

	// Другой участник перебивает активную ставку новой ставкой (update_bid=false).
	beta := createTestUser(t, ctx, users, "+1891"+suffix+"02", "BidIncBeta", "")
	if _, _, err := stars.EnsureGrant(ctx, beta.ID, 100000, now); err != nil {
		t.Fatalf("grant beta stars: %v", err)
	}
	stateBeta, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: beta.ID,
		GiftID: entry.Gift.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: beta.ID}, BidAmount: 251, FormID: 22005, Date: now + 4})
	if err != nil || stateBeta.UserState.BidAmount != 251 {
		t.Fatalf("beta outbid = %+v err %v", stateBeta.UserState, err)
	}
}
