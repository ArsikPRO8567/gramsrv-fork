package domain

import "testing"

// Эталонные образцы официального сервера (дампы слоя 228):
// Crystal Ball #743 «Persimmon» и #801 «Midnight Blue».
var (
	referencePersimmon = UniqueStarGift{
		ID: 5312273168126181377, GiftID: 5415586798250229762,
		Backdrop: StarGiftCollectibleAttribute{
			Kind: StarGiftCollectibleBackdrop, Name: "Persimmon", BackdropID: 26,
			CenterColor: 15181658, EdgeColor: 12937055, PatternColor: 11341312, TextColor: 16770263,
		},
		Model:   StarGiftCollectibleAttribute{Kind: StarGiftCollectibleModel, Document: &Document{ID: 5130132332476366849}},
		Pattern: StarGiftCollectibleAttribute{Kind: StarGiftCollectiblePattern, Document: &Document{ID: 5132006132218331137}},
	}
	referenceMidnight = UniqueStarGift{
		ID: 5386809095732854791, GiftID: 5415586798250229762,
		Backdrop: StarGiftCollectibleAttribute{
			Kind: StarGiftCollectibleBackdrop, Name: "Midnight Blue", BackdropID: 48,
			CenterColor: 6056325, EdgeColor: 3489879, PatternColor: 199192, TextColor: 12569824,
		},
		Model:   StarGiftCollectibleAttribute{Kind: StarGiftCollectibleModel, Document: &Document{ID: 5130132332476366849}},
		Pattern: StarGiftCollectibleAttribute{Kind: StarGiftCollectiblePattern, Document: &Document{ID: 5131971562026565633}},
	}
)

// channelDiff возвращает максимальную поканальную разницу двух упакованных
// цветов (0..255).
func channelDiff(a, b int) int {
	dr := (a>>16)&0xff - (b>>16)&0xff
	dg := (a>>8)&0xff - (b>>8)&0xff
	db := a&0xff - b&0xff
	if dr < 0 {
		dr = -dr
	}
	if dg < 0 {
		dg = -dg
	}
	if db < 0 {
		db = -db
	}
	return maxInt(dr, maxInt(dg, db))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestPeerColorCollectibleFromGiftOfficialReference сверяет выведенную палитру
// сообщений с эталоном #743: структура официальная (3 цвета + dark_accent),
// значения попадают в те же тональности (поканальная разница до 40/255).
func TestPeerColorCollectibleFromGiftOfficialReference(t *testing.T) {
	palette := PeerColorCollectibleFromGift(referencePersimmon)
	if !palette.Valid() {
		t.Fatalf("palette invalid: %+v", palette)
	}
	if palette.CollectibleID != referencePersimmon.ID ||
		palette.GiftEmojiID != referencePersimmon.Model.Document.ID ||
		palette.BackgroundEmojiID != referencePersimmon.Pattern.Document.ID {
		t.Fatalf("identity mismatch: %+v", palette)
	}
	official := PeerColorCollectible{
		AccentColor:     12671559,  // 0xC15A47
		Colors:          []int{7494986, 11229810, 16512738}, // 0x725D4A, 0xAB5A72, 0xFBF6E2
		DarkAccentColor: 16746097,  // 0xFF8671
	}
	if diff := channelDiff(palette.AccentColor, official.AccentColor); diff > 40 {
		t.Fatalf("accent color = #%06X, official #C15A47, diff %d", palette.AccentColor, diff)
	}
	for i := range official.Colors {
		if diff := channelDiff(palette.Colors[i], official.Colors[i]); diff > 40 {
			t.Fatalf("colors[%d] = #%06X, official #%06X, diff %d", i, palette.Colors[i], official.Colors[i], diff)
		}
	}
	if diff := channelDiff(palette.DarkAccentColor, official.DarkAccentColor); diff > 40 {
		t.Fatalf("dark accent = #%06X, official #FF8671, diff %d", palette.DarkAccentColor, diff)
	}
	// Светлый (третий) цвет обязан быть заметно светлее тёмного — эталонный
	// признак, по которому клиент строит градиент полосы ответов.
	if palette.Colors[2] <= palette.Colors[0] {
		t.Fatalf("light color #%06X must exceed dark #%06X", palette.Colors[2], palette.Colors[0])
	}
}

// TestCollectibleChatThemePaletteOfficialReference сверяет палитру темы чата
// с эталонами #743 и #801 (обоев и акцентов).
func TestCollectibleChatThemePaletteOfficialReference(t *testing.T) {
	palette := CollectibleChatThemePaletteFromGift(referencePersimmon)
	// Обои #743: bg #EDB678, second #F7D0AB, third #CF7D6E, fourth #F1C3A9.
	wp := []struct {
		name     string
		got      int
		official int
	}{
		{"bg", palette.WallpaperLight.Background, 15578744},
		{"second", palette.WallpaperLight.Second, 16240811},
		{"third", palette.WallpaperLight.Third, 13598062},
		{"fourth", palette.WallpaperLight.Fourth, 15844265},
		{"dark second", palette.WallpaperDark.Second, 16772579},
	}
	for _, c := range wp {
		if diff := channelDiff(c.got, c.official); diff > 40 {
			t.Fatalf("wallpaper %s = #%06X, official #%06X, diff %d", c.name, c.got, c.official, diff)
		}
	}
	if diff := channelDiff(palette.NightAccent, 16754798); diff > 48 { // 0xFFA86E
		t.Fatalf("night accent = #%06X, official #FFA86E, diff %d", palette.NightAccent, diff)
	}
	if diff := channelDiff(palette.NightMessageColors[0], 13995317); diff > 40 { // 0xD58D35
		t.Fatalf("night message color 0 = #%06X, official #D58D35, diff %d", palette.NightMessageColors[0], diff)
	}
	if diff := channelDiff(palette.NightMessageColors[1], 11225655); diff > 40 { // 0xAB4A37
		t.Fatalf("night message color 1 = #%06X, official #AB4A37, diff %d", palette.NightMessageColors[1], diff)
	}

	// #801 Midnight Blue: classic accent на эталоне — почти точное среднее
	// центра и края (#4A556E).
	palette801 := CollectibleChatThemePaletteFromGift(referenceMidnight)
	if diff := channelDiff(palette801.ClassicAccent, 4871534); diff > 16 {
		t.Fatalf("classic accent #801 = #%06X, official #4A556E, diff %d", palette801.ClassicAccent, diff)
	}
	// Заливки обоев #801: bg #74809A, second #ABB3C4, third #4B556C.
	wp801 := []struct {
		name     string
		got      int
		official int
	}{
		{"bg", palette801.WallpaperLight.Background, 7635098},
		{"second", palette801.WallpaperLight.Second, 11252676},
		{"third", palette801.WallpaperLight.Third, 4937068},
		{"fourth", palette801.WallpaperLight.Fourth, 10727105},
		{"dark second", palette801.WallpaperDark.Second, 14081768},
	}
	for _, c := range wp801 {
		if diff := channelDiff(c.got, c.official); diff > 40 {
			t.Fatalf("wallpaper #801 %s = #%06X, official #%06X, diff %d", c.name, c.got, c.official, diff)
		}
	}
	// Светлая и тёмная базы делят bg/third/fourth (эталонное правило).
	if palette801.WallpaperDark.Background != palette801.WallpaperLight.Background ||
		palette801.WallpaperDark.Third != palette801.WallpaperLight.Third ||
		palette801.WallpaperDark.Fourth != palette801.WallpaperLight.Fourth {
		t.Fatalf("dark wallpaper must share bg/third/fourth with light: %+v vs %+v",
			palette801.WallpaperDark, palette801.WallpaperLight)
	}
}

// TestPeerColorCollectibleFromGiftDegenerateBackdrop проверяет устойчивость
// к нулевым значениям фона: палитра обязана оставаться валидной.
func TestPeerColorCollectibleFromGiftDegenerateBackdrop(t *testing.T) {
	gift := referencePersimmon
	gift.Backdrop = StarGiftCollectibleAttribute{Kind: StarGiftCollectibleBackdrop}
	palette := PeerColorCollectibleFromGift(gift)
	if !palette.Valid() {
		t.Fatalf("palette on zero backdrop invalid: %+v", palette)
	}
	theme := CollectibleChatThemePaletteFromGift(gift)
	for _, c := range []int{
		theme.ClassicAccent, theme.NightAccent,
		theme.NightMessageColors[0], theme.NightMessageColors[1],
		theme.WallpaperLight.Background, theme.WallpaperLight.Second,
		theme.WallpaperLight.Third, theme.WallpaperLight.Fourth,
		theme.WallpaperDark.Second,
	} {
		if c <= 0 || c > 0xffffff {
			t.Fatalf("theme color out of range: #%06X (palette %+v)", c, theme)
		}
	}
}
