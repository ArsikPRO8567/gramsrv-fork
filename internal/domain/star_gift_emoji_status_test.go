package domain

import "testing"

// Round 10: the collectible emoji status caption must read
// "{gift title}-{unique number}" (e.g. "Star-22") so clients render the
// collectible number above the status instead of the bare gift name.
func TestCollectibleEmojiStatusTitleCarriesUniqueNumber(t *testing.T) {
	gift := UniqueStarGift{
		ID: 555, GiftID: 8001, Title: "Star", Slug: "Star-22", Num: 22,
		Model:   StarGiftCollectibleAttribute{Kind: StarGiftCollectibleModel, Document: &Document{ID: 900, AccessHash: 9}},
		Pattern: StarGiftCollectibleAttribute{Kind: StarGiftCollectiblePattern, Document: &Document{ID: 901, AccessHash: 10}},
		Backdrop: StarGiftCollectibleAttribute{Kind: StarGiftCollectibleBackdrop,
			CenterColor: 0xe4e4ef, EdgeColor: 0x8f8fa8, PatternColor: 0x8558d6, TextColor: 0x4d3386},
	}
	status, ok := CollectibleEmojiStatus(gift)
	if !ok {
		t.Fatalf("CollectibleEmojiStatus = not valid for a fully populated collectible")
	}
	if status.Title != "Star-22" {
		t.Fatalf("emoji status title = %q, want %q", status.Title, "Star-22")
	}
	if status.Slug != "Star-22" || status.DocumentID != 900 || status.PatternDocumentID != 901 {
		t.Fatalf("emoji status snapshot = %+v", status)
	}

	legacy := gift
	legacy.Num = 0
	legacyStatus, ok := CollectibleEmojiStatus(legacy)
	if !ok || legacyStatus.Title != "Star" {
		t.Fatalf("legacy collectible (num=0) title = %q ok=%v, want bare gift title", legacyStatus.Title, ok)
	}

	// A collectible without published model/pattern facts has no palette and
	// no renderable status (nil-safety regression guard).
	bare := UniqueStarGift{ID: 556, Title: "Bare", Num: 3}
	if status, ok := CollectibleEmojiStatus(bare); ok {
		t.Fatalf("bare collectible unexpectedly renderable: %+v", status)
	}
	if palette := PeerColorCollectibleFromGift(bare); palette.Valid() {
		t.Fatalf("bare collectible palette must not validate: %+v", palette)
	}
}
