package stargifts

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"telesrv/internal/domain"
)

// buildPNG собирает минимальный валидный PNG: подпись + IHDR с размерами.
func buildPNG(width, height uint32) []byte {
	data := make([]byte, 24)
	copy(data, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	binary.BigEndian.PutUint32(data[8:], 13)
	copy(data[12:16], "IHDR")
	binary.BigEndian.PutUint32(data[16:], width)
	binary.BigEndian.PutUint32(data[20:], height)
	return data
}

// buildWEBPVP8X собирает минимальный RIFF/WEBP контейнер с VP8X чанком.
func buildWEBPVP8X(width, height int) []byte {
	data := make([]byte, 30)
	copy(data[0:4], "RIFF")
	copy(data[8:12], "WEBP")
	copy(data[12:16], "VP8X")
	data[24] = byte((width - 1) & 0xFF)
	data[25] = byte(((width - 1) >> 8) & 0xFF)
	data[26] = byte(((width - 1) >> 16) & 0xFF)
	data[27] = byte((height - 1) & 0xFF)
	data[28] = byte(((height - 1) >> 8) & 0xFF)
	data[29] = byte(((height - 1) >> 16) & 0xFF)
	return data
}

// buildWEBMLossy собирает RIFF/WEBP с lossy VP8 кадром.
func buildWEBPLossy(width, height uint16) []byte {
	data := make([]byte, 30)
	copy(data[0:4], "RIFF")
	copy(data[8:12], "WEBP")
	copy(data[12:16], "VP8 ")
	data[23], data[24], data[25] = 0x9D, 0x01, 0x2A
	binary.LittleEndian.PutUint16(data[26:], width)
	binary.LittleEndian.PutUint16(data[28:], height)
	return data
}

// buildWEBPIncomplete без internal-чанка — парсер размеров обязан отказать.
var buildWEBPIncomplete = func() []byte {
	data := make([]byte, 16)
	copy(data[0:4], "RIFF")
	copy(data[8:12], "WEBP")
	copy(data[12:16], "XXXX")
	return data
}()

// ebmlVint кодирует EBML переменную длины: маркер в первом байте + значение.
// Для идентификаторов значение уже содержит маркерные биты.
func ebmlVint(value uint64, length int) []byte {
	out := make([]byte, length)
	for i := length - 1; i >= 1; i-- {
		out[i] = byte(value)
		value >>= 8
	}
	out[0] = byte(1)<<uint(8-length) | byte(value)
	return out
}

// ebmlUintPayload кодирует целое как payload EBML листа (big-endian, минимум байт).
func ebmlUintPayload(value int) []byte {
	length := 1
	for v := value; v > 255; v >>= 8 {
		length++
	}
	out := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		out[i] = byte(value)
		value >>= 8
	}
	return out
}

// ebmlElement собирает элемент: идентификатор нужной длины + размер + payload.
func ebmlElement(id int64, payload []byte) []byte {
	idLen := 1
	for v := id; v > 0xFF; v >>= 8 {
		idLen++
	}
	sizeLen := 1
	for size := len(payload); size >= 127; size >>= 7 {
		sizeLen++
	}
	out := ebmlVint(uint64(id), idLen)
	out = append(out, ebmlVint(uint64(len(payload)), sizeLen)...)
	return append(out, payload...)
}

// buildWEBM собирает минимальный EBML документ: EBML-заголовок, Segment,
// Info с Duration и Tracks с Video{PixelWidth, PixelHeight}.
func buildWEBM(width, height int, durationMS float64) []byte {
	durationBits := make([]byte, 4)
	binary.BigEndian.PutUint32(durationBits, math.Float32bits(float32(durationMS)))
	duration := append(ebmlVint(ebmlIDDuration, 2), byte(0x84))
	duration = append(duration, durationBits...)
	video := ebmlElement(ebmlIDPixelWidth, ebmlUintPayload(width))
	video = append(video, ebmlElement(ebmlIDPixelHeigh, ebmlUintPayload(height))...)
	track := ebmlElement(ebmlIDTrackEntry, ebmlElement(ebmlIDVideo, video))
	segment := append(ebmlElement(ebmlIDInfo, duration), ebmlElement(ebmlIDTracks, track)...)
	header := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x82, 0x42, 0x86}
	return append(header, ebmlElement(ebmlIDSegment, segment)...)
}

func TestPrepareMediaAnimationFormats(t *testing.T) {
	png := buildPNG(512, 512)
	prepared, err := prepareAnimation("gift.png", png)
	if err != nil {
		t.Fatalf("prepare png: %v", err)
	}
	if prepared.SourceFormat != domain.StarGiftAnimationPNG || prepared.Width != 512 || prepared.Height != 512 ||
		string(prepared.TGS) != string(png) {
		t.Fatalf("prepared png = %+v", prepared)
	}
	// animation_json — дескриптор-объект (shape-check каталога требует объект).
	var descriptor map[string]any
	if json.Unmarshal(prepared.JSON, &descriptor) != nil || descriptor["format"] != string(domain.StarGiftAnimationPNG) {
		t.Fatalf("png descriptor = %s", string(prepared.JSON))
	}

	webp := buildWEBPVP8X(512, 512)
	prepared, err = prepareAnimation("gift.webp", webp)
	if err != nil {
		t.Fatalf("prepare webp vp8x: %v", err)
	}
	if prepared.SourceFormat != domain.StarGiftAnimationWEBP || prepared.Width != 512 || prepared.Height != 512 {
		t.Fatalf("prepared webp vp8x = %+v", prepared)
	}

	lossy := buildWEBPLossy(512, 512)
	prepared, err = prepareAnimation("gift.webp", lossy)
	if err != nil {
		t.Fatalf("prepare webp lossy: %v", err)
	}
	if prepared.Width != 512 || prepared.Height != 512 {
		t.Fatalf("prepared webp lossy = %+v", prepared)
	}

	webm := buildWEBM(512, 512, 2500)
	prepared, err = prepareAnimation("gift.webm", webm)
	if err != nil {
		t.Fatalf("prepare webm: %v", err)
	}
	if prepared.SourceFormat != domain.StarGiftAnimationWEBM || prepared.Width != 512 || prepared.Height != 512 ||
		prepared.OutPoint < 2.499 || prepared.OutPoint > 2.501 {
		t.Fatalf("prepared webm = %+v", prepared)
	}

	// Расширение без магии — отказ по сигнатуре.
	if _, err := prepareAnimation("gift.png", []byte("not a png")); !errors.Is(err, domain.ErrStarGiftFileInvalid) {
		t.Fatalf("mislabeled png err=%v", err)
	}
	// WebP без internal-чанка — отказ по размерам.
	if _, err := prepareAnimation("gift.webp", buildWEBPIncomplete); !errors.Is(err, domain.ErrStarGiftFileInvalid) {
		t.Fatalf("webp without chunk err=%v", err)
	}
	// Раунд 42: не-512 и не-квадратные размеры в пределах стикер-лимита
	// принимаются (официальные клиенты масштабируют блок подарка под
	// документ; миграция 0198 ослабила shape-check БД).
	nonSquare, err := prepareAnimation("gift.png", buildPNG(256, 384))
	if err != nil {
		t.Fatalf("non-square png err=%v", err)
	}
	if nonSquare.Width != 256 || nonSquare.Height != 384 || nonSquare.SourceFormat != domain.StarGiftAnimationPNG {
		t.Fatalf("non-square png = %+v", nonSquare)
	}
	// Больше 512×512 — всё ещё отказ (лимит стикеров Telegram).
	if _, err := prepareAnimation("gift.png", buildPNG(513, 513)); !errors.Is(err, domain.ErrStarGiftFileInvalid) {
		t.Fatalf("oversized png err=%v", err)
	}
	// Слишком длинный webm — отказ по длительности.
	if _, err := prepareAnimation("gift.webm", buildWEBM(512, 512, 31000)); !errors.Is(err, domain.ErrStarGiftFileInvalid) {
		t.Fatalf("long webm err=%v", err)
	}
}

func TestCollectibleUpgradeVariants(t *testing.T) {
	if got := domain.CollectibleUpgradeVariants(10, 5, 10); got != 500 {
		t.Fatalf("variants(10,5,10) = %d, want 500", got)
	}
	if got := domain.CollectibleUpgradeVariants(0, 5, 10); got != 0 {
		t.Fatalf("variants(0,5,10) = %d, want 0", got)
	}
}
