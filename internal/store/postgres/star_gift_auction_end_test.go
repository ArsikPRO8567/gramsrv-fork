package postgres

import "testing"

// Раунд 21 (исправление): конец аукциона выводится из границы текущего раунда
// плюс длительности оставшихся раундов — продления раундов не должны завершать
// аукцион раньше, чем пройдут все раунды.
func TestDerivedAuctionEnd(t *testing.T) {
	durations := map[int]int{1: 120, 2: 300, 3: 1000}

	// Раунд 2 идёт, его граница (с учётом продлений) 5000: остаются раунды 3 (1000).
	if got := derivedAuctionEnd(durations, 2, 3, 3600, 5000); got != 6000 {
		t.Fatalf("derived end = %d, want 6000", got)
	}
	// Последний раунд: конец = граница текущего раунда.
	if got := derivedAuctionEnd(durations, 3, 3, 3600, 9000); got != 9000 {
		t.Fatalf("derived end at last round = %d, want 9000", got)
	}
	// Завершённый аукцион (currentRound > totalRounds): граница без добавок.
	if got := derivedAuctionEnd(durations, 4, 3, 3600, 12000); got != 12000 {
		t.Fatalf("derived end past total = %d, want 12000", got)
	}
	// Без jsonb-длительностей: общая длительность для оставшихся раундов.
	if got := derivedAuctionEnd(nil, 1, 3, 3600, 3600); got != 3600*3 {
		t.Fatalf("derived end common duration = %d, want %d", got, 3600*3)
	}
}
