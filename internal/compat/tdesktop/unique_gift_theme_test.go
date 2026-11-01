package tdesktop

import (
        "testing"

        "github.com/iamxvbaba/td/tg"

        "telesrv/internal/domain"
)

// TestUniqueGiftThemeSettingsWireShape фиксирует эталонную форму
// chatThemeUniqueGift.theme_settings, восстановленную по дампам официального
// сервера: ровно ДВА набора (Classic + Night), у обоих — wallPaper с
// заполненными settings (Android-клиент читает wallPaper.settings без
// проверки на nil и падает NPE, если обоев нет), pattern=true, у ночной базы
// dark=true и два message_colors; у светлой message_colors нет.
func TestUniqueGiftThemeSettingsWireShape(t *testing.T) {
        gift := domain.UniqueStarGift{
                ID:     5386809095732854791,
                GiftID: 5415586798250229762,
                Title:  "Crystal Ball",
                Slug:   "CrystalBall-801",
                Model: domain.StarGiftCollectibleAttribute{
                        Name: "Fortune",
                        Document: &domain.Document{
                                ID: 5130132332476366849,
                        },
                },
                Pattern: domain.StarGiftCollectibleAttribute{
                        Name: "Cow",
                        Document: &domain.Document{
                                ID: 5131971562026565633,
                        },
                },
                Backdrop: domain.StarGiftCollectibleAttribute{
                        Name:         "Midnight Blue",
                        CenterColor:  6056325,
                        EdgeColor:    3489879,
                        PatternColor: 199192,
                        TextColor:    12569824,
                },
        }

        settings := UniqueGiftThemeSettings(gift)
        if len(settings) != 2 {
                t.Fatalf("theme settings count = %d, want 2 (Classic + Night)", len(settings))
        }
        if _, ok := settings[0].BaseTheme.(*tg.BaseThemeClassic); !ok {
                t.Fatalf("settings[0] base theme = %T, want BaseThemeClassic", settings[0].BaseTheme)
        }
        if _, ok := settings[1].BaseTheme.(*tg.BaseThemeNight); !ok {
                t.Fatalf("settings[1] base theme = %T, want BaseThemeNight", settings[1].BaseTheme)
        }

        light := settings[0]
        dark := settings[1]

        // Краш-страж: wallPaper обязан быть и обязан нести settings.
        lightWp, ok := light.Wallpaper.(*tg.WallPaper)
        if !ok || lightWp == nil {
                t.Fatalf("light theme wallpaper missing (got %#v) — Android падает NPE на wallPaper.settings", light.Wallpaper)
        }
        if lightWp.Settings.Zero() {
                t.Fatalf("light wallpaper settings пустые — Android падает NPE на wallPaper.settings")
        }
        if !lightWp.Pattern || lightWp.Dark {
                t.Fatalf("light wallpaper flags: pattern=%v dark=%v, want pattern=true dark=false", lightWp.Pattern, lightWp.Dark)
        }
        if lightWp.Settings.Intensity != 40 {
                t.Fatalf("light wallpaper intensity = %d, want 40", lightWp.Settings.Intensity)
        }
        if lightWp.Settings.BackgroundColor == 0 || lightWp.Settings.SecondBackgroundColor == 0 ||
                lightWp.Settings.ThirdBackgroundColor == 0 || lightWp.Settings.FourthBackgroundColor == 0 {
                t.Fatalf("light wallpaper colors incomplete: %+v", lightWp.Settings)
        }
        if light.MessageColors != nil {
                t.Fatalf("light theme must not carry message_colors, got %v", light.MessageColors)
        }

        darkWp, ok := dark.Wallpaper.(*tg.WallPaper)
        if !ok || darkWp == nil {
                t.Fatalf("dark theme wallpaper missing (got %#v) — Android падает NPE на wallPaper.settings", dark.Wallpaper)
        }
        if darkWp.Settings.Zero() {
                t.Fatalf("dark wallpaper settings пустые — Android падает NPE на wallPaper.settings")
        }
        if !darkWp.Pattern || !darkWp.Dark {
                t.Fatalf("dark wallpaper flags: pattern=%v dark=%v, want pattern=true dark=true", darkWp.Pattern, darkWp.Dark)
        }
        if darkWp.Settings.Intensity != -35 {
                t.Fatalf("dark wallpaper intensity = %d, want -35", darkWp.Settings.Intensity)
        }
        if len(dark.MessageColors) != 2 {
                t.Fatalf("dark theme message_colors = %v, want 2 colors", dark.MessageColors)
        }
        if dark.OutboxAccentColor != 0 {
                t.Fatalf("dark theme must not carry outbox_accent_color (эталон его не передаёт)")
        }

        // Оба оттенка ссылаются на один wallPaper identity (как на эталоне).
        if lightWp.ID != darkWp.ID || lightWp.AccessHash != darkWp.AccessHash || lightWp.Slug != darkWp.Slug {
                t.Fatalf("light/dark wallpapers diverge: %+v vs %+v", lightWp, darkWp)
        }
        lightDoc, ok := lightWp.Document.(*tg.Document)
        if !ok || lightDoc == nil || lightDoc.ID == 0 {
                t.Fatalf("wallpaper document missing — клиент не сможет скачать узор")
        }
}

// TestPeerColorCollectibleFromGiftShape фиксирует официальную форму
// peerColorCollectible: три цвета colors + dark_accent_color, привязка к
// emoji модели и узору; недоопубликованная коллекция не паникует.
func TestPeerColorCollectibleFromGiftShape(t *testing.T) {
        gift := domain.UniqueStarGift{
                ID:     5312273168126181377,
                GiftID: 5415586798250229762,
                Model: domain.StarGiftCollectibleAttribute{
                        Document: &domain.Document{ID: 5130132332476366849},
                },
                Pattern: domain.StarGiftCollectibleAttribute{
                        Document: &domain.Document{ID: 5132006132218331137},
                },
                Backdrop: domain.StarGiftCollectibleAttribute{
                        Name:        "Persimmon",
                        CenterColor: 15181658,
                        EdgeColor:   12937055,
                        TextColor:   16770263,
                },
        }

        palette := domain.PeerColorCollectibleFromGift(gift)
        if !palette.Valid() {
                t.Fatalf("palette invalid: %+v", palette)
        }
        if len(palette.Colors) != 3 {
                t.Fatalf("colors = %v, want exactly 3 (эталонная форма)", palette.Colors)
        }
        if palette.DarkAccentColor == 0 {
                t.Fatalf("dark_accent_color = 0, эталон передаёт его всегда")
        }
        if palette.GiftEmojiID != 5130132332476366849 || palette.BackgroundEmojiID != 5132006132218331137 {
                t.Fatalf("emoji binding wrong: %+v", palette)
        }
        for i, v := range palette.Colors {
                if v <= 0 || v > 0xffffff {
                        t.Fatalf("colors[%d] out of range: %d", i, v)
                }
        }

        // Недоопубликованная коллекция (без документов) — не паникует и не валидна.
        bare := domain.UniqueStarGift{ID: 42, GiftID: 7}
        _ = domain.PeerColorCollectibleFromGift(bare)
}
