package tdesktop

import (
        "encoding/binary"
        "testing"

        "github.com/iamxvbaba/td/bin"
        "github.com/iamxvbaba/td/tg"

        "telesrv/internal/domain"
)

// giftThemeFixture — эталонный подарок #743 «Persimmon» с официального сервера.
func giftThemeFixture() domain.UniqueStarGift {
        return domain.UniqueStarGift{
                ID: 5312273168126181377, GiftID: 5415586798250229762,
                Backdrop: domain.StarGiftCollectibleAttribute{
                        Kind: domain.StarGiftCollectibleBackdrop, Name: "Persimmon", BackdropID: 26,
                        CenterColor: 15181658, EdgeColor: 12937055, PatternColor: 11341312, TextColor: 16770263,
                },
                Model:   domain.StarGiftCollectibleAttribute{Kind: domain.StarGiftCollectibleModel, Document: &domain.Document{ID: 5130132332476366849}},
                Pattern: domain.StarGiftCollectibleAttribute{Kind: domain.StarGiftCollectiblePattern, Document: &domain.Document{ID: 5132006132218331137}},
        }
}

// encodeFlagWord кодирует значение и возвращает первое флаговое слово после
// конструктора (для themeSettings) — эталонная проверка формы на проводе.
func encodeFlagWord(t *testing.T, value bin.Encoder) uint32 {
        t.Helper()
        var buf bin.Buffer
        if err := value.Encode(&buf); err != nil {
                t.Fatalf("encode %T: %v", value, err)
        }
        raw := buf.Buf
        if len(raw) < 8 {
                t.Fatalf("encoded %T too short: %d bytes", value, len(raw))
        }
        return binary.LittleEndian.Uint32(raw[4:8])
}

// TestUniqueGiftThemeSettingsOfficialWireShape фиксирует побайтовую форму
// theme_settings по эталонному дампу официального сервера: ровно два набора
// (Classic: только wallpaper; Night: message_colors + wallpaper), вложенный
// wallPaper с pattern/dark флагами и wallPaperSettings 0x79 (четыре цвета +
// intensity), intensity 40 / -35.
func TestUniqueGiftThemeSettingsOfficialWireShape(t *testing.T) {
        settings := UniqueGiftThemeSettings(giftThemeFixture())
        if len(settings) != 2 {
                t.Fatalf("theme settings count = %d, want exactly 2 (classic + night)", len(settings))
        }
        light, dark := settings[0], settings[1]
        if _, ok := light.BaseTheme.(*tg.BaseThemeClassic); !ok {
                t.Fatalf("light base theme = %T, want BaseThemeClassic", light.BaseTheme)
        }
        if _, ok := dark.BaseTheme.(*tg.BaseThemeNight); !ok {
                t.Fatalf("dark base theme = %T, want BaseThemeNight", dark.BaseTheme)
        }
        if len(light.MessageColors) != 0 {
                t.Fatalf("light settings must not carry message_colors (official flags=0x2): %v", light.MessageColors)
        }
        if _, hasOutbox := light.GetOutboxAccentColor(); hasOutbox {
                t.Fatalf("light settings must not carry outbox_accent_color")
        }
        if _, hasOutbox := dark.GetOutboxAccentColor(); hasOutbox {
                t.Fatalf("dark settings must not carry outbox_accent_color")
        }
        if len(dark.MessageColors) != 2 {
                t.Fatalf("dark settings message_colors = %v, want exactly 2", dark.MessageColors)
        }

        // Флаговые слова themeSettings: light=0x2 (wallpaper), dark=0x3
        // (message_colors + wallpaper) — байт-в-байт как в эталонном дампе.
        if flags := encodeFlagWord(t, &light); flags != 0x00000002 {
                t.Fatalf("light themeSettings flags = 0x%08X, want 0x00000002", flags)
        }
        if flags := encodeFlagWord(t, &dark); flags != 0x00000003 {
                t.Fatalf("dark themeSettings flags = 0x%08X, want 0x00000003", flags)
        }

        lightWallpaper, ok := light.GetWallpaper()
        if !ok {
                t.Fatalf("light settings missing wallpaper (Android crashes without it)")
        }
        darkWallpaper, ok := dark.GetWallpaper()
        if !ok {
                t.Fatalf("dark settings missing wallpaper (Android crashes without it)")
        }
        for name, entry := range map[string]struct {
                wp     tg.WallPaperClass
                flags  uint32
                darker bool
        }{
                "light": {lightWallpaper, 0x0000000C, false},
                "dark":  {darkWallpaper, 0x0000001C, true},
        } {
                wallpaper, ok := entry.wp.(*tg.WallPaper)
                if !ok {
                        t.Fatalf("%s wallpaper = %T, want *tg.WallPaper", name, entry.wp)
                }
                if !wallpaper.Pattern {
                        t.Fatalf("%s wallpaper pattern flag missing", name)
                }
                if wallpaper.Dark != entry.darker {
                        t.Fatalf("%s wallpaper dark = %v, want %v", name, wallpaper.Dark, entry.darker)
                }
                // wallPaper: id(long) -> flags; эталон: light 0x0C (pattern+settings),
                // dark 0x1C (pattern+dark+settings).
                var buf bin.Buffer
                if err := wallpaper.Encode(&buf); err != nil {
                        t.Fatalf("encode %s wallpaper: %v", name, err)
                }
                // ctor(4) + id(8) -> flags at offset 12.
                got := binary.LittleEndian.Uint32(buf.Buf[12:16])
                if got != entry.flags {
                        t.Fatalf("%s wallPaper flags = 0x%08X, want 0x%08X", name, got, entry.flags)
                }
                settings, ok := wallpaper.GetSettings()
                if !ok {
                        t.Fatalf("%s wallpaper missing settings", name)
                }
                // wallPaperSettings flags: эталон 0x79 — background_color, intensity,
                // second/third/fourth; без rotation/emoticon/blur/motion.
                if flags := encodeFlagWord(t, &settings); flags != 0x00000079 {
                        t.Fatalf("%s wallPaperSettings flags = 0x%08X, want 0x00000079", name, flags)
                }
                wantIntensity := domain.CollectibleChatThemeIntensityLight
                if entry.darker {
                        wantIntensity = domain.CollectibleChatThemeIntensityDark
                }
                if settings.Intensity != wantIntensity {
                        t.Fatalf("%s wallpaper intensity = %d, want %d", name, settings.Intensity, wantIntensity)
                }
                doc, ok := wallpaper.Document.(*tg.Document)
                if !ok || doc.ID == 0 {
                        t.Fatalf("%s wallpaper document = %T, want seeded tg.Document", name, wallpaper.Document)
                }
                if wallpaper.ID == 0 || wallpaper.Slug == "" {
                        t.Fatalf("%s wallpaper identity incomplete: id=%d slug=%q", name, wallpaper.ID, wallpaper.Slug)
                }
        }
        // Эталон: обе базы ссылаются на один и тот же wallPaper.
        if lightWallpaper.(*tg.WallPaper).ID != darkWallpaper.(*tg.WallPaper).ID {
                t.Fatalf("light/dark wallpapers must share identity (official behavior)")
        }
}

// TestUniqueGiftThemeSettingsDeterministicPerCollection проверяет, что выбор
// узора стабилен для одной коллекции (официально коллекция = один узор).
func TestUniqueGiftThemeSettingsDeterministicPerCollection(t *testing.T) {
        first := UniqueGiftThemeSettings(giftThemeFixture())
        second := UniqueGiftThemeSettings(giftThemeFixture())
        a, _ := first[0].GetWallpaper()
        b, _ := second[0].GetWallpaper()
        if a.(*tg.WallPaper).ID != b.(*tg.WallPaper).ID {
                t.Fatalf("wallpaper selection unstable for the same gift")
        }
}
