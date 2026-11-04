package domain

import "testing"

func TestSavedStarGiftListCursorV2RoundTrip(t *testing.T) {
	want := SavedStarGiftListCursor{PinnedOrder: 3, GiftDate: 1760000000, ID: 42}
	got, ok := DecodeSavedStarGiftListCursor(EncodeSavedStarGiftListCursorV2(want.PinnedOrder, want.GiftDate, want.ID))
	if !ok || got != want {
		t.Fatalf("v2 cursor round trip = %+v ok=%v, want %+v", got, ok, want)
	}

	unpinned := SavedStarGiftListCursor{GiftDate: 1760000001, ID: 7}
	got, ok = DecodeSavedStarGiftListCursor(EncodeSavedStarGiftListCursorV2(0, unpinned.GiftDate, unpinned.ID))
	if !ok || got != unpinned {
		t.Fatalf("v2 unpinned cursor round trip = %+v ok=%v, want %+v", got, ok, unpinned)
	}

	// v1-курсоры (раунды 20 и раньше) остаются читаемыми: GiftDate == 0.
	v1want := SavedStarGiftListCursor{PinnedOrder: 2, ID: 15}
	got, ok = DecodeSavedStarGiftListCursor("djE6MjoxNQ")
	if !ok || got != v1want {
		t.Fatalf("v1 cursor decode = %+v ok=%v, want %+v", got, ok, v1want)
	}

	for _, cursor := range []string{
		EncodeSavedStarGiftListCursorV2(-1, 100, 42),
		EncodeSavedStarGiftListCursorV2(1, -5, 42),
		EncodeSavedStarGiftListCursorV2(1, 100, 0),
	} {
		if got, ok := DecodeSavedStarGiftListCursor(cursor); ok {
			t.Fatalf("cursor %q decoded as %+v, want rejected", cursor, got)
		}
	}
}
