package domain

import "testing"

// Матрица флагов messageActionStarGift калибрована по официальным дампам
// messageActionStarGift (raw_messages.txt, реальные сообщения официального
// сервера), а не по частной логике ячейки клиента:
//
//	сообщение 81530: limited+sold_out подарок, can_upgrade=true,
//	                 action.convert_stars=9, action.upgrade_stars отсутствует;
//	сообщение 57253: подарок с附言, can_upgrade=true, action.convert_stars=21;
//	сообщение 100802: name_hidden, can_upgrade=false, action.convert_stars=43;
//	сообщение 183490: prepaid_upgrade=true, upgrade_separate=true,
//	                 action.convert_stars отсутствует, action.upgrade_stars=25.
//
// Вывод: convert_stars живёт на action всегда, когда конвертация разрешена —
// в том числе одновременно с can_upgrade; исчезает только у предоплаченных
// подарков (конвертация для них запрещена сервером).
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
		// Дамп 100802: can_upgrade=false, convert_stars=43.
		{"обычный конвертируемый", convertible, 25, 0, false, 25},
		{"обычный без конвертации", convertible, 0, 0, false, 0},
		// Дампы 81530 и 57253: can_upgrade=true И convert_stars>0 одновременно.
		{"улучшаемый лимитированный", upgradeable, 25, 0, true, 25},
		{"пул апгрейдов исчерпан", upgradeSoldOut, 25, 0, false, 25},
		// Дамп 183490: prepaid → convert_stars отсутствует.
		{"предоплаченный", upgradeable, 25, 100, true, 0},
		{"предоплаченный без звёзд подарка", upgradeable, 0, 100, true, 0},
		// Открытый пул (UpgradeTotal=0) — улучшение доступно, конвертация тоже.
		{"улучшаемый без пула (открытый)", StarGift{ConvertStars: 25, UpgradeStars: 100}, 25, 0, true, 25},
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
