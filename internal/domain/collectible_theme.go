package domain

import "math"

// Официальная схема палитры коллекционного подарка восстановлена по дампам
// эталонного сервера (messages.setChatTheme + users.getFullUser, слой 228):
//
//   - starGiftUnique.peer_color / users.color — peerColorCollectible с ТРЕМЯ
//     цветами colors и установленным битом dark_accent_color (dark_colors не
//     передаётся: флаг.1 снят, на проводе только dark_accent_color);
//   - chatThemeUniqueGift.theme_settings — ровно ДВА набора themeSettings:
//     BaseThemeClassic (флаги: wallpaper) и BaseThemeNight (флаги:
//     message_colors + wallpaper); у Classic message_colors отсутствует,
//     у Night — два цвета; outbox_accent_color не передаётся ни в одном;
//   - вложенный wallPaper: pattern=true, dark=false/true, settings с четырьмя
//     цветами фона и intensity 40 (светлая) / -35 (тёмная), rotation=0.
//
// Точные значения цветов официальный сервер не документирует; формулы ниже
// подобраны по двум эталонным образцам (Crystal Ball #743 «Persimmon» и #801
// «Midnight Blue»): значения воспроизводят структуру и попадают в те же
// тональности (hue/насыщенность/светлота в пределах нескольких градусов и
// единиц процентов от эталона). Структурная точность (число цветов, биты,
// intensity) соблюдена байт-в-байт.

// CollectibleWallpaperColors — четыре цвета заливки wallPaperSettings.
type CollectibleWallpaperColors struct {
        Background int
        Second     int
        Third      int
        Fourth     int
}

// CollectibleChatThemePalette — полная палитра темы чата коллекционного
// подарка: акценты, цвета исходящих сообщений ночной темы и заливки обоев.
type CollectibleChatThemePalette struct {
        ClassicAccent      int
        NightAccent        int
        NightMessageColors [2]int
        WallpaperLight     CollectibleWallpaperColors
        WallpaperDark      CollectibleWallpaperColors
}

// CollectibleChatThemeIntensity — официальные константы intensity обоев тем
// подарков: 40 на светлой базе и -35 на ночной (у обоих эталонных образцов).
const (
        CollectibleChatThemeIntensityLight = 40
        CollectibleChatThemeIntensityDark  = -35
)

// CollectibleChatThemePaletteFromGift выводит палитру темы чата из фона
// коллекционного подарка (центр, край, текст).
func CollectibleChatThemePaletteFromGift(g UniqueStarGift) CollectibleChatThemePalette {
        centerRaw := giftBackdropColor(g.Backdrop.CenterColor, 0x8a94a6)
        edgeRaw := giftBackdropColor(g.Backdrop.EdgeColor, 0x5a6270)
        center := hslFromRGB(centerRaw)
        edge := hslFromRGB(edgeRaw)
        text := hslFromRGB(giftBackdropColor(g.Backdrop.TextColor, 0xfff6ea))

        // Заливка обоев: базовый тон — центр фона, третий — край, второй —
        // производная текстового (светлого) тона.
        light := CollectibleWallpaperColors{
                Background: hslToRGB(center.h, center.s, clampF(center.l+0.08, 0.35, 0.74)),
                Second:     hslToRGB(center.h, center.s, clampF(text.l-0.10, 0.55, 0.85)),
                Third:      hslToRGB(edge.h, edge.s, clampF(edge.l+0.07, 0.30, 0.66)),
                Fourth:     hslToRGB(center.h, center.s, clampF((center.l+text.l)/2, 0.45, 0.85)),
        }
        dark := light
        dark.Second = hslToRGB(center.h, clampF(text.s, 0.20, 1.0), clampF(text.l+0.04, 0.80, 0.96))

        accentClassic := mixRGB(centerRaw, edgeRaw)
        accentNight := hslToRGB(
                mixHue(center.h, text.h, 0.5),
                clampF(math.Max(text.s, center.s*1.35), 0.55, 1.0),
                clampF(text.l-0.15, 0.72, 0.80),
        )
        return CollectibleChatThemePalette{
                ClassicAccent: accentClassic,
                NightAccent:   accentNight,
                NightMessageColors: [2]int{
                        hslToRGB(center.h, clampF(center.s*0.9, 0.05, 0.85), clampF(center.l-0.08, 0.25, 0.55)),
                        hslToRGB(edge.h, clampF(edge.s, 0.20, 0.60), clampF(edge.l-0.04, 0.22, 0.55)),
                },
                WallpaperLight: light,
                WallpaperDark:  dark,
        }
}

// PeerColorCollectibleFromGift выводит официальную палитру сообщений
// (peerColorCollectible) из атрибутов коллекционного подарка: три цвета
// colors (тёмный, средний, светлый — светлый из текстового тона фона),
// accent из края фона и dark_accent_color; gift_emoji_id — emoji модели,
// background_emoji_id — документ узора.
func PeerColorCollectibleFromGift(g UniqueStarGift) PeerColorCollectible {
        center := hslFromRGB(giftBackdropColor(g.Backdrop.CenterColor, 0x8a94a6))
        edge := hslFromRGB(giftBackdropColor(g.Backdrop.EdgeColor, 0x5a6270))
        text := hslFromRGB(giftBackdropColor(g.Backdrop.TextColor, 0xfff6ea))
        // nil-safety: у недоопубликованной коллекции может не быть документов
        // модели/узора — тогда палитра получается невалидной (0 <= id), но не
        // паникует; клиенты такие палитры отбрасывают.
        var giftEmojiID, patternEmojiID int64
        if g.Model.Document != nil {
                giftEmojiID = g.Model.Document.ID
        }
        if g.Pattern.Document != nil {
                patternEmojiID = g.Pattern.Document.ID
        }
        return PeerColorCollectible{
                CollectibleID:     g.ID,
                GiftEmojiID:       giftEmojiID,
                BackgroundEmojiID: patternEmojiID,
                AccentColor: hslToRGB(
                        edge.h,
                        clampF(edge.s, 0.40, 0.55),
                        clampF(edge.l, 0.45, 0.55),
                ),
                Colors: []int{
                        // Тёмный тон центра — полоса ответов/ссылок в тёмных обводках.
                        hslToRGB(center.h, clampF(center.s*0.30, 0.10, 0.35), clampF(center.l-0.26, 0.15, 0.45)),
                        // Средний тон края.
                        hslToRGB(mixHue(edge.h, center.h, 0.5), clampF(edge.s*0.70, 0.15, 0.45), clampF(edge.l-0.06, 0.30, 0.60)),
                        // Светлый тон текста фона (третий цвет — как на эталоне).
                        hslToRGB(mixHue(text.h, center.h, 0.5), clampF(text.s*0.76, 0.30, 0.85), clampF(text.l+0.01, 0.80, 0.94)),
                },
                DarkAccentColor: hslToRGB(edge.h, 1.0, clampF(edge.l+0.15, 0.55, 0.78)),
        }
}

// giftBackdropColor подставляет запасной тон для некорректных (нулевых)
// значений фона, чтобы палитра оставалась валидной на любом datum.
func giftBackdropColor(value, fallback int) int {
        if value <= 0 || value > 0xffffff {
                return fallback
        }
        return value
}

type hsl struct {
        h, s, l float64
}

func hslFromRGB(rgb int) hsl {
        r := float64((rgb>>16)&0xff) / 255
        g := float64((rgb>>8)&0xff) / 255
        b := float64(rgb&0xff) / 255
        max := math.Max(r, math.Max(g, b))
        min := math.Min(r, math.Min(g, b))
        l := (max + min) / 2
        if max == min {
                return hsl{h: 0, s: 0, l: l}
        }
        delta := max - min
        var s float64
        if l > 0.5 {
                s = delta / (2 - max - min)
        } else {
                s = delta / (max + min)
        }
        var h float64
        switch max {
        case r:
                h = (g - b) / delta
                if g < b {
                        h += 6
                }
        case g:
                h = (b-r)/delta + 2
        default:
                h = (r-g)/delta + 4
        }
        return hsl{h: h * 60, s: s, l: l}
}

func hslToRGB(h, s, l float64) int {
        h = math.Mod(math.Mod(h, 360)+360, 360)
        s = clampF(s, 0, 1)
        l = clampF(l, 0, 1)
        if s == 0 {
                v := int(math.Round(l * 255))
                return v<<16 | v<<8 | v
        }
        c := (1 - math.Abs(2*l-1)) * s
        x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
        m := l - c/2
        var r, g, b float64
        switch {
        case h < 60:
                r, g, b = c, x, 0
        case h < 120:
                r, g, b = x, c, 0
        case h < 180:
                r, g, b = 0, c, x
        case h < 240:
                r, g, b = 0, x, c
        case h < 300:
                r, g, b = x, 0, c
        default:
                r, g, b = c, 0, x
        }
        toByte := func(v float64) int { return int(math.Round((v + m) * 255)) }
        return clampByte(toByte(r))<<16 | clampByte(toByte(g))<<8 | clampByte(toByte(b))
}

// mixHue смешивает два тона по кратчайшей дуге цветового круга.
func mixHue(a, b, t float64) float64 {
        a = math.Mod(math.Mod(a, 360)+360, 360)
        b = math.Mod(math.Mod(b, 360)+360, 360)
        diff := b - a
        if diff > 180 {
                diff -= 360
        } else if diff < -180 {
                diff += 360
        }
        return a + diff*t
}

// mixRGB — покомпонентное среднее двух упакованных цветов (эталонный образец
// «Midnight Blue» воспроизводится им практически точно).
func mixRGB(a, b int) int {
        ar, ag, ab := (a>>16)&0xff, (a>>8)&0xff, a&0xff
        br, bg, bb := (b>>16)&0xff, (b>>8)&0xff, b&0xff
        return clampByte((ar+br+1)/2)<<16 | clampByte((ag+bg+1)/2)<<8 | clampByte((ab+bb+1)/2)
}

func clampF(v, lo, hi float64) float64 {
        if v < lo {
                return lo
        }
        if v > hi {
                return hi
        }
        return v
}

func clampByte(v int) int {
        if v < 0 {
                return 0
        }
        if v > 255 {
                return 255
        }
        return v
}
