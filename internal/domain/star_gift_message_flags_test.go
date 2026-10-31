package domain

import "testing"

// Матрица флагов messageActionStarGift воспроизводит выбор подписи ячейки
// сервисного сообщения в ChatActionCell официального клиента (12.x):
//
//	Gift2ActionOutInfo        «{user} может добавить этот подарок в профиль
//	                           или обменять на N звёзд»       — convert_stars>0
//	Gift2ActionOutInfoUpgrade «…или превратить в уникальный коллекционный»
//	                                                          — can_upgrade, без convert_stars
//	Gift2ActionOutInfoNoConvert «{user} может добавить этот подарок в профиль»
//	                                                          — без обоих
//	Gift2ActionUpgradeOut/Gift2ActionUpgrade («Распаковать»)  — can_upgrade && upgrade_stars>0
func TestStarGiftMessageFlags(t *testing.T) {
	convertible := StarGift{Stars: 50, ConvertStars: 25, UpgradeStars: 0}
	upgradeable := StarGift{Stars: 50, ConvertStars: 25, UpgradeStars: 100,
		UpgradeTotal: 1000, UpgradeIssued: 10}
	upgradeSoldOut := StarGift{Stars: 50, ConvertStars: 25, UpgradeStars: 100,
		UpgradeTotal: 1000, UpgradeIssued: 1000}

	cases := []struct {
		name              string
		gift              StarGift
		savedConvertStars int64
		prepaidStars      int64
		wantCanUpgrade    bool
		wantActionConvert int64
	}{
		{"обычный конвертируемый", convertible, 25, 0, false, 25},
		{"обычный без конвертации", convertible, 0, 0, false, 0},
		{"улучшаемый лимитированный", upgradeable, 25, 0, true, 0},
		{"пул апгрейдов исчерпан", upgradeSoldOut, 25, 0, false, 25},
		{"предоплаченный", upgradeable, 25, 100, true, 0},
		{"предоплаченный без звёзд подарка", upgradeable, 0, 100, true, 0},
		{"улучшаемый без пула (открытый)", StarGift{ConvertStars: 25, UpgradeStars: 100}, 25, 0, true, 0},
	}
	for _, tc := range cases {
		canUpgrade, convertStars := StarGiftMessageFlags(tc.gift, tc.savedConvertStars, tc.prepaidStars)
		if canUpgrade != tc.wantCanUpgrade || convertStars != tc.wantActionConvert {
			t.Errorf("%s: got (can_upgrade=%v, convert_stars=%d), want (%v, %d)",
				tc.name, canUpgrade, convertStars, tc.wantCanUpgrade, tc.wantActionConvert)
		}
	}
}

func TestStarGiftUpgradeAvailable(t *testing.T) {
	g := StarGift{UpgradeStars: 100, UpgradeTotal: 2, UpgradeIssued: 2}
	if StarGiftUpgradeAvailable(g, 0) {
		t.Errorf("пул исчерпан — улучшение недоступно")
	}
	if !StarGiftUpgradeAvailable(g, 100) {
		t.Errorf("предоплата — entitlement переживает sold-out")
	}
	if StarGiftUpgradeAvailable(StarGift{UpgradeStars: 0}, 0) {
		t.Errorf("upgrade_stars=0 — улучшение недоступно")
	}
}
