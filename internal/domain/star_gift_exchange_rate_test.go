package domain

import "testing"

func TestStarGiftExchangeRateConversions(t *testing.T) {
	official := StarGiftExchangeRate{StarsUnits: 1457, NanotonUnits: 18_000_000_000}
	if !official.Valid() {
		t.Fatal("official rate must be valid")
	}
	// Официальный пример: 1457 звёзд = 18 TON (18000000000 нанотон).
	if got := official.TonForStars(1457); got != 18_000_000_000 {
		t.Fatalf("TonForStars(1457) = %d, want 18000000000", got)
	}
	if got := official.StarsForTon(18_000_000_000); got != 1457 {
		t.Fatalf("StarsForTon(18000000000) = %d, want 1457", got)
	}
	// Округление вверх: неполные единицы не должны занижать цену продавца.
	simple := StarGiftExchangeRate{StarsUnits: 1, NanotonUnits: 3}
	if got := simple.TonForStars(5); got != 15 {
		t.Fatalf("TonForStars(5) = %d, want 15", got)
	}
	if got := simple.StarsForTon(7); got != 3 {
		t.Fatalf("StarsForTon(7) = %d, want 3 (ceil)", got)
	}
	if got := simple.StarsForTon(9); got != 3 {
		t.Fatalf("StarsForTon(9) = %d, want 3", got)
	}
	if got := simple.StarsForTon(10); got != 4 {
		t.Fatalf("StarsForTon(10) = %d, want 4 (ceil)", got)
	}
	// Невалидные входы и курс.
	if got := simple.TonForStars(0); got != 0 {
		t.Fatalf("TonForStars(0) = %d, want 0", got)
	}
	if got := simple.StarsForTon(-1); got != 0 {
		t.Fatalf("StarsForTon(-1) = %d, want 0", got)
	}
	invalid := StarGiftExchangeRate{StarsUnits: 0, NanotonUnits: 3}
	if got := invalid.TonForStars(5); got != 0 {
		t.Fatalf("invalid rate TonForStars = %d, want 0", got)
	}
	// Большие числа не должны переполняться: 1e8 звёзд по курсу 1:15e9.
	big := StarGiftExchangeRate{StarsUnits: 1, NanotonUnits: 15_000_000_000}
	if got, want := big.TonForStars(100_000_000), int64(1_500_000_000_000_000_000); got != want {
		t.Fatalf("TonForStars(1e8) = %d, want %d", got, want)
	}
	// Результат за пределами int64 ограничивается максимумом вместо переполнения.
	if got := big.TonForStars(1_000_000_000_000_000_000); got != 9223372036854775807 {
		t.Fatalf("TonForStars(1e18) = %d, want MaxInt64", got)
	}
}

func TestSetStarGiftExchangeRate(t *testing.T) {
	fallback := CurrentStarGiftExchangeRate()
	defer SetStarGiftExchangeRate(fallback)
	custom := StarGiftExchangeRate{StarsUnits: 1, NanotonUnits: 15_000_000_000}
	SetStarGiftExchangeRate(custom)
	if got := CurrentStarGiftExchangeRate(); got != custom {
		t.Fatalf("rate = %+v, want %+v", got, custom)
	}
	// Невалидный курс игнорируется.
	SetStarGiftExchangeRate(StarGiftExchangeRate{StarsUnits: -1, NanotonUnits: 1})
	if got := CurrentStarGiftExchangeRate(); got != custom {
		t.Fatalf("invalid rate must be ignored, got %+v", got)
	}
}

func TestStarGiftResalePrices(t *testing.T) {
	fallback := CurrentStarGiftExchangeRate()
	defer SetStarGiftExchangeRate(fallback)
	SetStarGiftExchangeRate(StarGiftExchangeRate{StarsUnits: 2, NanotonUnits: 5})
	// Листинг в звёздах: звёздная цена сохраняется, TON считается сервером.
	stars, ton := StarGiftResalePrices(StarGiftAmount{Currency: StarGiftCurrencyStars, Amount: 10})
	if stars != 10 || ton != 25 {
		t.Fatalf("StarGiftResalePrices(stars 10) = %d/%d, want 10/25", stars, ton)
	}
	// Листинг в TON: TON сохраняется, звёзды — эквивалент для отображения.
	stars, ton = StarGiftResalePrices(StarGiftAmount{Currency: StarGiftCurrencyTON, Amount: 11})
	if stars != 5 || ton != 11 {
		t.Fatalf("StarGiftResalePrices(ton 11) = %d/%d, want 5/11 (ceil)", stars, ton)
	}
}
