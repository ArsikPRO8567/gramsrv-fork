package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// Раунд 31: увеличение ставки аукциона (update_bid) для АКТИВНОЙ и для
// ВОЗВРАЩЁННОЙ (перебитой) ставки.
//
// Официальные клиенты шлют update_bid=true в обоих случаях (Android
// GiftAuctionController.hasBid() = bid_amount > 0, bid_amount в user_state
// сохраняется и у возвращённой ставки). Для активной ставки атрибуты
// (получатель/сообщение/анонимность) не меняются («the value cannot be
// changed for existing bids»), для возвращённой — применяются из запроса:
// документация api/auctions описывает повторное участие как «новую ставку».
func TestStarGiftAuctionBidUpdateAndReturnedReentryPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	alpha := createTestUser(t, ctx, users, "+1887"+suffix+"01", "BidUpAlpha", "")
	beta := createTestUser(t, ctx, users, "+1887"+suffix+"02", "BidUpBeta", "")
	gamma := createTestUser(t, ctx, users, "+1887"+suffix+"03", "BidUpGamma", "")
	alphaPeer := domain.Peer{Type: domain.PeerTypeUser, ID: alpha.ID}
	betaPeer := domain.Peer{Type: domain.PeerTypeUser, ID: beta.ID}
	gammaPeer := domain.Peer{Type: domain.PeerTypeUser, ID: gamma.ID}

	stars := NewStarsStore(pool)
	for _, user := range []domain.User{alpha, beta, gamma} {
		if _, _, err := stars.EnsureGrant(ctx, user.ID, 100000, now); err != nil {
			t.Fatalf("grant stars: %v", err)
		}
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 950
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "BidUpdate " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 2, AvailabilityRemains: 2, GiftsPerRound: 1, AuctionStartDate: now - 10,
		AuctionSlug: "bidup-" + suffix, ReleasedBy: alphaPeer,
		Document: collectibleTestDocument(baseDocumentID, "bidup.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "bidup"), Animation: collectibleTestAnimation("bidup.tgs"),
		Actor: "integration", CommandID: "bidup-catalog-" + suffix,
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

	bid := func(userID int64, peer domain.Peer, amount int64, formID int64, update bool, msg string, hide bool, date int) {
		t.Helper()
		_, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: userID,
			GiftID: entry.Gift.ID, Peer: peer, BidAmount: amount, FormID: formID, Date: date,
			UpdateBid: update, Message: msg, HideName: hide})
		if err != nil {
			t.Fatalf("bid user=%d amount=%d update=%v: %v", userID, amount, update, err)
		}
	}
	assertRow := func(userID int64, wantActive, wantReturned bool, wantAmount int64, wantPeer domain.Peer, wantMsg string, wantHide bool) {
		t.Helper()
		var active, returned bool
		var amount int64
		var peerType string
		var peerID int64
		var msg string
		var hide bool
		if err := pool.QueryRow(ctx, `SELECT active,returned,amount,recipient_peer_type,recipient_peer_id,COALESCE(message,''),hide_name
FROM star_gift_auction_bids WHERE gift_id=$1 AND bidder_user_id=$2`, entry.Gift.ID, userID).
			Scan(&active, &returned, &amount, &peerType, &peerID, &msg, &hide); err != nil {
			t.Fatalf("load bid row user=%d: %v", userID, err)
		}
		wantP := domain.Peer{Type: domain.PeerType(peerType), ID: peerID}
		if active != wantActive || returned != wantReturned || amount != wantAmount ||
			wantP != wantPeer || msg != wantMsg || hide != wantHide {
			t.Fatalf("bid row user=%d = active=%v returned=%v amount=%d peer=%s/%d msg=%q hide=%v, want active=%v returned=%v amount=%d peer=%+v msg=%q hide=%v",
				userID, active, returned, amount, peerType, peerID, msg, hide, wantActive, wantReturned, wantAmount, wantPeer, wantMsg, wantHide)
		}
	}

	// Раунд 1: три участника (alpha 100, beta 101, gamma 102).
	bid(alpha.ID, alphaPeer, 100, 31001, false, "alpha-first", false, now)
	bid(beta.ID, betaPeer, 101, 31002, false, "beta-first", false, now+1)
	bid(gamma.ID, gammaPeer, 102, 31003, false, "gamma-first", false, now+2)

	// --- Увеличение АКТИВНОЙ ставки: атрибуты из запроса НЕ применяются. ---
	// alpha «повышает» 100 → 150, но в запросе подставляет чужого получателя,
	// чужое сообщение и hide_name=true (Android AuctionWearingSheet шлёт
	// hide_name=true, SendGiftSheet — peer+message на каждое повышение).
	bid(alpha.ID, betaPeer, 150, 31004, true, "hijack-message", true, now+3)
	assertRow(alpha.ID, true, false, 150, alphaPeer, "alpha-first", false)

	// --- Граница раунда 1: alpha (150) выигрывает подарок, beta (2-я позиция
	// активных) вылетает за gifts_left=1 и получает возврат
	// (returned=true). Аукцион продолжается (раунд 2, gifts_left=1). ---
	if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET next_round_at=$2 WHERE gift_id=$1`, entry.Gift.ID, now+10); err != nil {
		t.Fatalf("make round due: %v", err)
	}
	if err := lifecycle.SweepStarGiftLifecycle(ctx, now+10, 1000); err != nil {
		t.Fatalf("sweep settle round 1: %v", err)
	}
	assertRow(alpha.ID, false, false, 150, alphaPeer, "alpha-first", false)
	assertRow(beta.ID, false, true, 101, betaPeer, "beta-first", false)
	assertRow(gamma.ID, true, false, 102, gammaPeer, "gamma-first", false)
	var betaBalance int64
	if err := pool.QueryRow(ctx, `SELECT amount FROM stars_transactions WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, beta.ID).Scan(&betaBalance); err != nil {
		t.Fatalf("load beta refund txn: %v", err)
	}
	if betaBalance != 101 {
		t.Fatalf("beta refund amount = %d, want 101", betaBalance)
	}

	// --- Возвращённая ставка: повторное участие update_bid=true с НОВЫМИ
	// атрибутами (получатель gamma, своё сообщение, скрытое имя) — новая
	// ставка, списывается целиком, атрибуты применяются. ---
	bid(beta.ID, gammaPeer, 110, 31005, true, "beta-reentry", true, now+11)
	assertRow(beta.ID, true, false, 110, gammaPeer, "beta-reentry", true)
	if err := pool.QueryRow(ctx, `SELECT amount FROM stars_transactions WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, beta.ID).Scan(&betaBalance); err != nil {
		t.Fatalf("load beta reentry txn: %v", err)
	}
	if betaBalance != -110 {
		t.Fatalf("beta reentry debit = %d, want -110 (full amount after refund)", betaBalance)
	}

	// Активная ставка гаммы не задета возвратом беты.
	assertRow(gamma.ID, true, false, 102, gammaPeer, "gamma-first", false)
}
