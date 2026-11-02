package postgres

import "testing"

// Раунд 20: индивидуальные длительности раундов аукциона.
func TestParseAuctionRoundDurations(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[int]int
	}{
		{"object form", `{"round_1":120,"round_2":300,"round_4":1000}`, map[int]int{1: 120, 2: 300, 4: 1000}},
		// Форма файла-образца: массив из одного объекта.
		{"array form", `[{"round_1":120,"round_2":300,"round_4":1000}]`, map[int]int{1: 120, 2: 300, 4: 1000}},
		{"array merges entries", `[{"round_1":120},{"round_2":300}]`, map[int]int{1: 120, 2: 300}},
		{"null json", `null`, nil},
		{"empty object", `{}`, nil},
		{"garbage", `not json at all`, nil},
		{"non round keys ignored", `{"round_0":50,"round_x":70,"gifts":3,"round_2":90}`, map[int]int{2: 90}},
		{"non positive ignored", `{"round_1":0,"round_2":-5,"round_3":45}`, map[int]int{3: 45}},
		{"empty raw", ``, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAuctionRoundDurations([]byte(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("parse(%q) = %v, want %v", tc.raw, got, tc.want)
			}
			for round, duration := range tc.want {
				if got[round] != duration {
					t.Fatalf("parse(%q)[%d] = %d, want %d", tc.raw, round, got[round], duration)
				}
			}
		})
	}
}

func TestAuctionRoundDurationForFallsBackToCommon(t *testing.T) {
	durations := map[int]int{1: 120, 2: 300}
	if got := auctionRoundDurationFor(durations, 1, 3600); got != 120 {
		t.Fatalf("round 1 = %d, want 120", got)
	}
	if got := auctionRoundDurationFor(durations, 2, 3600); got != 300 {
		t.Fatalf("round 2 = %d, want 300", got)
	}
	// Пропущенный раунд — общая длительность.
	if got := auctionRoundDurationFor(durations, 3, 3600); got != 3600 {
		t.Fatalf("round 3 = %d, want 3600", got)
	}
	// NULL-jsonb — всегда общая.
	if got := auctionRoundDurationFor(nil, 1, 3600); got != 3600 {
		t.Fatalf("nil durations round 1 = %d, want 3600", got)
	}
}

func TestAuctionRoundBoundary(t *testing.T) {
	durations := map[int]int{1: 120, 2: 300}
	// Раунд ещё не начался — граница равна start.
	if got := auctionRoundBoundary(1000, durations, 0, 3600); got != 1000 {
		t.Fatalf("boundary(0) = %d, want 1000", got)
	}
	if got := auctionRoundBoundary(1000, durations, 1, 3600); got != 1120 {
		t.Fatalf("boundary(1) = %d, want 1120", got)
	}
	if got := auctionRoundBoundary(1000, durations, 2, 3600); got != 1420 {
		t.Fatalf("boundary(2) = %d, want 1420", got)
	}
	// Раунд 3 без индивидуальной длительности: 1420 + 3600.
	if got := auctionRoundBoundary(1000, durations, 3, 3600); got != 5020 {
		t.Fatalf("boundary(3) = %d, want 5020", got)
	}
	// Без jsonb — эквивалент прежнего пошагового next_round_at += common.
	if got := auctionRoundBoundary(1000, nil, 3, 3600); got != 11800 {
		t.Fatalf("boundary(nil,3) = %d, want 11800", got)
	}
}

// Раунд 20 (исправление): пол следующей ставки аукциона — «топ-1 + 1».
func TestAuctionBidFloor(t *testing.T) {
	cases := []struct {
		name         string
		minBidAmount int64
		topBid       int64
		want         int64
	}{
		// Ставок ещё нет — действует исходный min_bid_amount без надбавки.
		{"no bids keeps base min", 500, 0, 500},
		// Пример из запроса: топ-1 поставил 600 — минимальная ставка 601.
		{"top bid 600 floors at 601", 500, 600, 601},
		// Сохранённый пол выше топ-ставки+1 (переносится между раундами) — не понижается.
		{"ratchet floor never decreases", 700, 600, 700},
		// Старые данные до исправления: min_bid_amount равнялся самой топ-ставке —
		// состояние всё равно показывает топ+1.
		{"legacy row equal to top bid", 600, 600, 601},
		{"top bid 1 floors at 2", 1, 1, 2},
	}
	for _, tc := range cases {
		if got := auctionBidFloor(tc.minBidAmount, tc.topBid); got != tc.want {
			t.Errorf("%s: auctionBidFloor(%d, %d) = %d, want %d", tc.name, tc.minBidAmount, tc.topBid, got, tc.want)
		}
	}
}
