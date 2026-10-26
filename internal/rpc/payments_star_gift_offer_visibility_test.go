package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// offer_min_stars управляет кнопкой «Предложить сделку»: владелец не должен
// видеть флаг на собственном подарке, остальным зрителям и контекстам без
// зрителя он отдаётся как раньше.
func TestTgUniqueStarGiftOfferMinStarsVisibility(t *testing.T) {
	unique := domain.UniqueStarGift{
		ID: 5001, GiftID: 8002, Title: "Durov Cap", Slug: "DurovCap-1", Num: 1,
		Owner:          domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
		OfferMinStars:  500,
		RequirePremium: true,
	}

	ownerView := tgUniqueStarGiftForViewer(unique, 1001)
	if _, ok := ownerView.GetOfferMinStars(); ok {
		t.Fatalf("owner must not see offer_min_stars")
	}
	if !ownerView.RequirePremium {
		t.Fatalf("require_premium must survive the viewer projection")
	}

	otherView := tgUniqueStarGiftForViewer(unique, 2002)
	minStars, ok := otherView.GetOfferMinStars()
	if !ok || minStars != 500 {
		t.Fatalf("non-owner offer_min_stars = %d,%v want 500,true", minStars, ok)
	}

	rawView := tgUniqueStarGift(unique)
	if _, ok := rawView.GetOfferMinStars(); !ok {
		t.Fatalf("projection without viewer context must keep offer_min_stars")
	}

	// Канальный владелец: офферы не адресуются каналу, флаг никому не отдаётся.
	channelOwned := unique
	channelOwned.Owner = domain.Peer{Type: domain.PeerTypeChannel, ID: 900}
	channelView := tgUniqueStarGiftForViewer(channelOwned, 1001)
	if _, ok := channelView.GetOfferMinStars(); ok {
		t.Fatalf("channel-owned gifts must not advertise offer_min_stars")
	}
	channelRaw := tgUniqueStarGift(channelOwned)
	if _, ok := channelRaw.GetOfferMinStars(); ok {
		t.Fatalf("channel-owned gifts must not advertise offer_min_stars (raw)")
	}
}
