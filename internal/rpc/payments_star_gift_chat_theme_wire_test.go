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

// TestChatThemeUniqueGiftCarriesWallpaper — контракт Android-клиента: каждый
// themeSettings в chatThemeUniqueGift обязан нести wallpaper (flag 1) с
// wallPaperNoFile, у которого задан settings (flag 2) с градиентом фона и
// intensity. Отсутствие wallpaper → NPE в
// ChatActivity.ThemeDelegate.getBackgroundDrawableFromTheme (краш при открытии
// чата с установленной темой подарка). Проверяем полный цикл Encode→Decode:
// структура обязана корректно развернуться обратно, как это сделает клиент.
func TestChatThemeUniqueGiftCarriesWallpaper(t *testing.T) {
	gift := chatThemeRoundTripGift()
	theme := tgUniqueGiftChatThemeForViewer(gift, 0)

	if len(theme.ThemeSettings) < 4 {
		t.Fatalf("theme settings count = %d, want >= 4 (client picker drops < 4)", len(theme.ThemeSettings))
	}
	seenBases := make(map[tg.BaseThemeClass]int, len(theme.ThemeSettings))
	for i, ts := range theme.ThemeSettings {
		wallpaper, ok := ts.GetWallpaper()
		if !ok {
			t.Fatalf("settings[%d] (%T): wallpaper flag not set", i, ts.BaseTheme)
		}
		noFile, ok := wallpaper.(*tg.WallPaperNoFile)
		if !ok {
			t.Fatalf("settings[%d]: wallpaper = %T, want wallPaperNoFile", i, wallpaper)
		}
		settings, ok := noFile.GetSettings()
		if !ok {
			t.Fatalf("settings[%d]: wallPaperNoFile.settings flag not set", i)
		}
		bg, ok := settings.GetBackgroundColor()
		if !ok {
			t.Fatalf("settings[%d]: background_color missing", i)
		}
		second, ok := settings.GetSecondBackgroundColor()
		if !ok {
			t.Fatalf("settings[%d]: second_background_color missing", i)
		}
		if intensity, ok := settings.GetIntensity(); !ok || intensity <= 0 {
			t.Fatalf("settings[%d]: intensity = %d/%v, want > 0", i, intensity, ok)
		}
		// Проверяем самосогласованность с accent: светлые base — фон=центр,
		// тёмные — фон=край (второй цвет — парный).
		dark := ts.AccentColor == gift.Backdrop.EdgeColor
		if dark {
			if bg != gift.Backdrop.EdgeColor || second != gift.Backdrop.CenterColor {
				t.Fatalf("settings[%d] dark: colors = [%#x,%#x], want [edge,center]", i, bg, second)
			}
		} else if bg != gift.Backdrop.CenterColor || second != gift.Backdrop.EdgeColor {
			t.Fatalf("settings[%d] light: colors = [%#x,%#x], want [center,edge]", i, bg, second)
		}
		seenBases[ts.BaseTheme]++
	}
	if len(seenBases) < 4 {
		t.Fatalf("distinct base themes = %d, want >= 4", len(seenBases))
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
		} else if _, ok := wp.(*tg.WallPaperNoFile); !ok {
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
// трёх цветов: [центр, край, text_color] фона подарка. Палитра из двух цветов
// отвергается клиентом, из-за чего настройка слетает.
func TestPeerColorCollectibleThreeColors(t *testing.T) {
	gift := chatThemeRoundTripGift()
	palette := domain.PeerColorCollectibleFromGift(gift)
	if !palette.Valid() {
		t.Fatalf("palette invalid: %+v", palette)
	}
	if len(palette.Colors) != 3 {
		t.Fatalf("colors = %v (%d), want 3", palette.Colors, len(palette.Colors))
	}
	if palette.Colors[0] != gift.Backdrop.CenterColor ||
		palette.Colors[1] != gift.Backdrop.EdgeColor ||
		palette.Colors[2] != gift.Backdrop.TextColor {
		t.Fatalf("colors = [%#x %#x %#x], want [center edge text_color]",
			palette.Colors[0], palette.Colors[1], palette.Colors[2])
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
	if len(decoded.Colors) != 3 || decoded.Colors[2] != gift.Backdrop.TextColor {
		t.Fatalf("decoded colors = %v, want 3 with text_color last", decoded.Colors)
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
