package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// chatThemeRoundTripGift — тестовый коллекционный подарок с полным backdrop.
func chatThemeRoundTripGift() domain.UniqueStarGift {
	return domain.UniqueStarGift{
		ID:      9901,
		GiftID:  12,
		Title:   "Star",
		Slug:    "star-22",
		Num:     22,
		Owner:   domain.Peer{Type: domain.PeerTypeUser, ID: 101},
		Model:   domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7101}},
		Pattern: domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7201}},
		Backdrop: domain.StarGiftCollectibleAttribute{
			CenterColor: 0xe4a7ff, EdgeColor: 0x7b1fa2, PatternColor: 0xffffff, TextColor: 0x2b0b3a,
		},
	}
}

// TestChatThemeUniqueGiftCarriesWallpaper — контракт Android-клиента по
// ЭТАЛОННОМУ дампу официального сервера (раунд 11): ровно ДВА themeSettings
// (BaseThemeClassic светлая + BaseThemeNight тёмная), каждый обязан нести
// wallPaper (flag 1) с pattern-документом и settings (flag 2): четыре цвета
// фона + intensity 40 (светлая) / -35 (тёмная). Отсутствие wallpaper → NPE в
// ChatActivity.ThemeDelegate.getBackgroundDrawableFromTheme (краш при открытии
// чата с установленной темой подарка). Проверяем полный цикл Encode→Decode:
// структура обязана корректно развернуться обратно, как это сделает клиент.
func TestChatThemeUniqueGiftCarriesWallpaper(t *testing.T) {
	gift := chatThemeRoundTripGift()
	theme := tgUniqueGiftChatThemeForViewer(gift, 0)

	if len(theme.ThemeSettings) != 2 {
		t.Fatalf("theme settings count = %d, want exactly 2 (classic light + night dark)", len(theme.ThemeSettings))
	}
	light, dark := theme.ThemeSettings[0], theme.ThemeSettings[1]
	if _, ok := light.BaseTheme.(*tg.BaseThemeClassic); !ok {
		t.Fatalf("light base theme = %T, want BaseThemeClassic", light.BaseTheme)
	}
	if _, ok := dark.BaseTheme.(*tg.BaseThemeNight); !ok {
		t.Fatalf("dark base theme = %T, want BaseThemeNight", dark.BaseTheme)
	}
	for i, ts := range theme.ThemeSettings {
		wallpaper, ok := ts.GetWallpaper()
		if !ok {
			t.Fatalf("settings[%d] (%T): wallpaper flag not set", i, ts.BaseTheme)
		}
		fileWallpaper, ok := wallpaper.(*tg.WallPaper)
		if !ok {
			t.Fatalf("settings[%d]: wallpaper = %T, want wallPaper with document", i, wallpaper)
		}
		if !fileWallpaper.Pattern {
			t.Fatalf("settings[%d]: wallpaper pattern flag missing", i)
		}
		settings, ok := fileWallpaper.GetSettings()
		if !ok {
			t.Fatalf("settings[%d]: wallPaper.settings flag not set", i)
		}
		if _, ok := settings.GetBackgroundColor(); !ok {
			t.Fatalf("settings[%d]: background_color missing", i)
		}
		if _, ok := settings.GetSecondBackgroundColor(); !ok {
			t.Fatalf("settings[%d]: second_background_color missing", i)
		}
		if _, ok := settings.GetThirdBackgroundColor(); !ok {
			t.Fatalf("settings[%d]: third_background_color missing", i)
		}
		if _, ok := settings.GetFourthBackgroundColor(); !ok {
			t.Fatalf("settings[%d]: fourth_background_color missing", i)
		}
		wantIntensity := domain.CollectibleChatThemeIntensityLight
		if i == 1 {
			wantIntensity = domain.CollectibleChatThemeIntensityDark
		}
		if intensity, ok := settings.GetIntensity(); !ok || intensity != wantIntensity {
			t.Fatalf("settings[%d]: intensity = %d/%v, want %d", i, intensity, ok, wantIntensity)
		}
	}
	if _, ok := dark.GetMessageColors(); !ok || len(dark.MessageColors) != 2 {
		t.Fatalf("dark settings message_colors = %v, want exactly 2", dark.MessageColors)
	}
	if _, has := light.GetMessageColors(); has {
		t.Fatal("light settings must not carry message_colors (official dump)")
	}

	// Обе базы ссылаются на один wallPaper (эталон: узор один на коллекцию).
	lightWallpaper := light.Wallpaper.(*tg.WallPaper)
	darkWallpaper := dark.Wallpaper.(*tg.WallPaper)
	if lightWallpaper.ID == 0 || lightWallpaper.ID != darkWallpaper.ID || lightWallpaper.Slug == "" {
		t.Fatalf("wallpapers identity: light id=%d dark id=%d slug=%q",
			lightWallpaper.ID, darkWallpaper.ID, lightWallpaper.Slug)
	}
	if lightWallpaper.Document == nil || darkWallpaper.Document == nil {
		t.Fatal("wallpaper document missing")
	}
	if lightWallpaper.Dark || !darkWallpaper.Dark {
		t.Fatalf("dark flags: light=%v dark=%v, want false/true", lightWallpaper.Dark, darkWallpaper.Dark)
	}

	// Полный wire round-trip chatThemeUniqueGift: серверное кодирование должно
	// развернуться без ошибок — это ровно то, что читает Android-клиент.
	var buf bin.Buffer
	if err := theme.Encode(&buf); err != nil {
		t.Fatalf("encode chatThemeUniqueGift: %v", err)
	}
	decoded := &tg.ChatThemeUniqueGift{}
	if err := decoded.Decode(&buf); err != nil {
		t.Fatalf("decode chatThemeUniqueGift: %v", err)
	}
	if len(decoded.ThemeSettings) != len(theme.ThemeSettings) {
		t.Fatalf("decoded settings = %d, want %d", len(decoded.ThemeSettings), len(theme.ThemeSettings))
	}
	for i, ts := range decoded.ThemeSettings {
		if wp, ok := ts.GetWallpaper(); !ok {
			t.Fatalf("decoded settings[%d]: wallpaper lost", i)
		} else if _, ok := wp.(*tg.WallPaper); !ok {
			t.Fatalf("decoded settings[%d]: wallpaper = %T", i, wp)
		}
	}

	// Сервисное сообщение (messageActionSetChatTheme) несёт ту же тему:
	// структура media повторяет appmessages.chatThemeServiceGiftMedia.
	action := &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind:                domain.MessageServiceActionSetChatTheme,
			ChatThemeUniqueGift: &domain.MessageChatThemeUniqueGiftAction{Gift: gift},
		},
	}
	converted := tgMessageServiceAction(domain.Message{Media: action})
	setTheme, ok := converted.(*tg.MessageActionSetChatTheme)
	if !ok {
		t.Fatalf("action = %T, want messageActionSetChatTheme", converted)
	}
	if _, ok := setTheme.Theme.(*tg.ChatThemeUniqueGift); !ok {
		t.Fatalf("action theme = %T, want chatThemeUniqueGift", setTheme.Theme)
	}
}

// TestPeerColorCollectibleThreeColors — официальный сервер отдаёт палитру из
// ровно трёх цветов colors (+ dark_accent_color по флагу.0): палитра из двух
// цветов отвергается клиентом, из-за чего настройка слетает. Конкретные
// значения выводятся из фона подарка по эталонным дампам (см.
// internal/domain/collectible_theme_test.go) — здесь фиксируются форма и wire
// round-trip.
func TestPeerColorCollectibleThreeColors(t *testing.T) {
	gift := chatThemeRoundTripGift()
	palette := domain.PeerColorCollectibleFromGift(gift)
	if !palette.Valid() {
		t.Fatalf("palette invalid: %+v", palette)
	}
	if len(palette.Colors) != 3 {
		t.Fatalf("colors = %v (%d), want 3", palette.Colors, len(palette.Colors))
	}
	for i, color := range palette.Colors {
		if color <= 0 || color > 0xffffff {
			t.Fatalf("colors[%d] = %#x, want plausible RGB24", i, color)
		}
	}
	if palette.DarkAccentColor <= 0 || palette.DarkAccentColor > 0xffffff {
		t.Fatalf("dark_accent_color = %#x, want plausible RGB24", palette.DarkAccentColor)
	}

	// Wire round-trip peerColorCollectible.
	var buf bin.Buffer
	if err := tgPeerColorCollectible(palette).Encode(&buf); err != nil {
		t.Fatalf("encode peerColorCollectible: %v", err)
	}
	decoded := &tg.PeerColorCollectible{}
	if err := decoded.Decode(&buf); err != nil {
		t.Fatalf("decode peerColorCollectible: %v", err)
	}
	if len(decoded.Colors) != 3 {
		t.Fatalf("decoded colors = %v, want 3", decoded.Colors)
	}
	if decoded.AccentColor != palette.AccentColor || decoded.DarkAccentColor != palette.DarkAccentColor {
		t.Fatalf("decoded accent = %#x dark = %#x, want %#x/%#x",
			decoded.AccentColor, decoded.DarkAccentColor, palette.AccentColor, palette.DarkAccentColor)
	}
}

// TestCollectibleEmojiStatusTitleFormat — title статуса форматируется как
// «{имя подарка} #{номер}» (клиент отображает поле как есть).
func TestCollectibleEmojiStatusTitleFormat(t *testing.T) {
	gift := chatThemeRoundTripGift()
	status, ok := domain.CollectibleEmojiStatus(gift)
	if !ok {
		t.Fatal("collectible status must project")
	}
	if status.Title != "Star #22" {
		t.Fatalf("title = %q, want %q", status.Title, "Star #22")
	}

	// Без номера выпуска (атипичные данные) — имя без суффикса.
	gift.Num = 0
	status, ok = domain.CollectibleEmojiStatus(gift)
	if !ok || status.Title != "Star" {
		t.Fatalf("num=0: title = %q ok=%v, want %q", status.Title, ok, "Star")
	}
}
