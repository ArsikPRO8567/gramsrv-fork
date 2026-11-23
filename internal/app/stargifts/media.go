package stargifts

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"

	"telesrv/internal/domain"
)

// Раунд 37: медиа-форматы анимаций подарков (.webp/.webm/.png). Официальные
// клиенты рендерят такие документы как статичные/анимированные стикеры и
// видео-стикеры, поэтому сервер обязан хранить исходные байты и отдавать
// документ с корректным mime и атрибутами. Размеры (и длительность для webm)
// парсятся из заголовков: без них клиент рисует карточку с нулевой геометрией.

// sniffMediaAnimation определяет медиа-формат по магическим байтам, а при их
// несовпадении — по расширению. Возвращает false, если файл не является
// webp/webm/png (то есть обрабатывается Lottie-конвейером).
func sniffMediaAnimation(fileName string, data []byte) (domain.StarGiftAnimationFormat, bool) {
	if isPNG(data) {
		return domain.StarGiftAnimationPNG, true
	}
	if isWEBP(data) {
		return domain.StarGiftAnimationWEBP, true
	}
	if isWEBM(data) {
		return domain.StarGiftAnimationWEBM, true
	}
	switch animationFormatByExtension(fileName) {
	case domain.StarGiftAnimationPNG, domain.StarGiftAnimationWEBP, domain.StarGiftAnimationWEBM:
		return animationFormatByExtension(fileName), true
	default:
		return "", false
	}
}

// animationFormatByExtension сопоставляет расширение файла формату импорта.
func animationFormatByExtension(fileName string) domain.StarGiftAnimationFormat {
	name := strings.TrimSpace(fileName)
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return ""
	}
	switch strings.ToLower(name[dot:]) {
	case ".tgs":
		return domain.StarGiftAnimationTGS
	case ".json", ".lottie":
		return domain.StarGiftAnimationLottie
	case ".webp":
		return domain.StarGiftAnimationWEBP
	case ".webm":
		return domain.StarGiftAnimationWEBM
	case ".png":
		return domain.StarGiftAnimationPNG
	default:
		return ""
	}
}

// prepareMediaAnimation валидирует медиа-файл и заполняет StarGiftAnimation.
// Исходные байты сохраняются в TGS-поле структуры (единственный клиентский
// payload), а JSON несёт дескриптор (format/width/height/source) — столбец
// animation_json в схеме каталога jsonb NOT NULL с проверкой «объект»;
// админ-превью отличает дескриптор по ключу format. Требование стикеров
// Telegram и shape-check каталога: холст ровно 512×512.
func prepareMediaAnimation(fileName string, data []byte, format domain.StarGiftAnimationFormat) (domain.StarGiftAnimation, error) {
	if int64(len(data)) == 0 || int64(len(data)) > domain.MaxStarGiftMediaBytes {
		return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
	}
	var width, height int
	var duration float64
	switch format {
	case domain.StarGiftAnimationPNG:
		if !isPNG(data) {
			return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
		}
		var ok bool
		width, height, ok = pngDimensions(data)
		if !ok {
			return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
		}
	case domain.StarGiftAnimationWEBP:
		if !isWEBP(data) {
			return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
		}
		var ok bool
		width, height, ok = webpDimensions(data)
		if !ok {
			return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
		}
	case domain.StarGiftAnimationWEBM:
		if !isWEBM(data) {
			return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
		}
		var ok bool
		width, height, duration, ok = webmMetadata(data)
		if !ok || width <= 0 || height <= 0 {
			return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
		}
		if duration > domain.MaxStarGiftAnimationSeconds {
			return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
		}
	default:
		return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
	}
	// Официальные клиенты масштабируют блок подарка под документ, поэтому
	// медиа-анимация может быть любого размера в пределах лимита стикера
	// 512×512 (не обязательно квадратной и не обязательно ровно 512×512 —
	// раунд 42: админ загружает исходник как есть). Больше 512 не допускаем
	// по лимитам стикеров Telegram; миграция 0198 ослабила shape-check БД.
	if width <= 0 || height <= 0 || width > 512 || height > 512 {
		return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
	}
	descriptor, err := json.Marshal(map[string]any{
		"format": string(format), "width": width, "height": height,
		"source": fileName, "duration": duration,
	})
	if err != nil {
		return domain.StarGiftAnimation{}, domain.ErrStarGiftFileInvalid
	}
	sum := sha256.Sum256(data)
	return domain.StarGiftAnimation{
		SourceName:   fileName,
		SourceFormat: format,
		JSON:         descriptor,
		TGS:          append([]byte(nil), data...),
		SHA256:       sum[:],
		Width:        width,
		Height:       height,
		OutPoint:     duration,
	}, nil
}

func isPNG(data []byte) bool {
	return len(data) >= 24 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 &&
		data[4] == 0x0D && data[5] == 0x0A && data[6] == 0x1A && data[7] == 0x0A
}

// pngDimensions читает IHDR первого чанка: подпись (8) + длина (4) + "IHDR" (4),
// затем big-endian width/height.
func pngDimensions(data []byte) (int, int, bool) {
	if len(data) < 24 || string(data[12:16]) != "IHDR" {
		return 0, 0, false
	}
	width := int(binary.BigEndian.Uint32(data[16:20]))
	height := int(binary.BigEndian.Uint32(data[20:24]))
	return width, height, width > 0 && height > 0
}

func isWEBP(data []byte) bool {
	return len(data) >= 16 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// webpDimensions поддерживает все три внутренних формата контейнера:
// VP8X (extended), VP8 (lossy) и VP8L (lossless).
func webpDimensions(data []byte) (int, int, bool) {
	if !isWEBP(data) {
		return 0, 0, false
	}
	switch string(data[12:16]) {
	case "VP8X":
		if len(data) < 30 {
			return 0, 0, false
		}
		width := 1 + (int(data[24]) | int(data[25])<<8 | int(data[26])<<16)
		height := 1 + (int(data[27]) | int(data[28])<<8 | int(data[29])<<16)
		return width, height, width > 0 && height > 0
	case "VP8 ":
		// Кадр lossy: 3 байта frame tag (20..22), start code 0x9D 0x01 0x2A
		// (23..25), затем 14-битные width/height little-endian.
		if len(data) < 30 || data[23] != 0x9D || data[24] != 0x01 || data[25] != 0x2A {
			return 0, 0, false
		}
		width := int(binary.LittleEndian.Uint16(data[26:28])) & 0x3FFF
		height := int(binary.LittleEndian.Uint16(data[28:30])) & 0x3FFF
		return width, height, width > 0 && height > 0
	case "VP8L":
		// Lossless: сигнатура 0x2F, затем 32-битное слово: 14 бит width-1,
		// 14 бит height-1.
		if len(data) < 25 || data[20] != 0x2F {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		width := int(bits&0x3FFF) + 1
		height := int((bits>>14)&0x3FFF) + 1
		return width, height, width > 0 && height > 0
	default:
		return 0, 0, false
	}
}

func isWEBM(data []byte) bool {
	// EBML magic: 0x1A45DFA3
	return len(data) >= 4 && data[0] == 0x1A && data[1] == 0x45 && data[2] == 0xDF && data[3] == 0xA3
}

// EBML идентификаторы, достаточные для чтения геометрии и длительности.
const (
	ebmlIDSegment    = 0x18538067
	ebmlIDInfo       = 0x1549A966
	ebmlIDTracks     = 0x1654AE6B
	ebmlIDTrackEntry = 0xAE
	ebmlIDVideo      = 0xE0
	ebmlIDDuration   = 0x4489
	ebmlIDPixelWidth = 0xB0
	ebmlIDPixelHeigh = 0xBA
)

// ebmlMasters — множество master-элементов, в которые разрешено спускаться.
func ebmlMasters(id int64) bool {
	switch id {
	case ebmlIDSegment, ebmlIDInfo, ebmlIDTracks, ebmlIDTrackEntry, ebmlIDVideo:
		return true
	default:
		return false
	}
}

// ebmlVintLength — длина EBML-переменной (идентификатор или размер) по
// позиции ведущего единичного бита первого байта.
func ebmlVintLength(first byte) (int, bool) {
	if first == 0 {
		return 0, false
	}
	length, mask := 1, byte(0x80)
	for first&mask == 0 {
		length++
		if length > 8 {
			return 0, false
		}
		mask >>= 1
	}
	return length, true
}

// ebmlReadID читает идентификатор элемента (с маркерными битами).
func ebmlReadID(data []byte, pos int) (int64, int, bool) {
	if pos >= len(data) {
		return 0, pos, false
	}
	length, ok := ebmlVintLength(data[pos])
	if !ok || length > 4 || pos+length > len(data) {
		return 0, pos, false
	}
	id := int64(data[pos])
	for i := 1; i < length; i++ {
		id = id<<8 | int64(data[pos+i])
	}
	return id, pos + length, true
}

// ebmlReadSize читает размер элемента. Неизвестный размер (все единицы)
// возвращается как -1: границей payload становится конец родителя.
func ebmlReadSize(data []byte, pos int) (int64, int, bool) {
	if pos >= len(data) {
		return 0, pos, false
	}
	length, ok := ebmlVintLength(data[pos])
	if !ok || pos+length > len(data) {
		return 0, pos, false
	}
	marker := byte(1) << uint(8-length)
	value := int64(data[pos] & (marker - 1))
	for i := 1; i < length; i++ {
		value = value<<8 | int64(data[pos+i])
	}
	if value == (int64(1)<<uint(7*length))-1 {
		return -1, pos + length, true
	}
	return value, pos + length, true
}

// ebmlWalk обходит [start,end), спускаясь только в master-элементы, и
// собирает PixelWidth/PixelHeight/Duration.
func ebmlWalk(data []byte, start, end int, depth int, out *webmMetadataResult) {
	if depth > 8 {
		return
	}
	pos := start
	for pos < end {
		id, next, ok := ebmlReadID(data, pos)
		if !ok {
			return
		}
		size, afterSize, ok := ebmlReadSize(data, next)
		if !ok {
			return
		}
		payloadStart := afterSize
		payloadEnd := end
		if size >= 0 {
			payloadEnd = payloadStart + int(size)
			if payloadEnd > end {
				return
			}
		}
		switch {
		case ebmlMasters(id):
			ebmlWalk(data, payloadStart, payloadEnd, depth+1, out)
		case id == ebmlIDPixelWidth && size > 0 && size <= 4 && payloadEnd <= len(data):
			out.width = ebmlUint(data[payloadStart:payloadEnd])
		case id == ebmlIDPixelHeigh && size > 0 && size <= 4 && payloadEnd <= len(data):
			out.height = ebmlUint(data[payloadStart:payloadEnd])
		case id == ebmlIDDuration && size == 4 && payloadEnd <= len(data):
			out.duration = float64(math.Float32frombits(binary.BigEndian.Uint32(data[payloadStart:payloadEnd])))
		case id == ebmlIDDuration && size == 8 && payloadEnd <= len(data):
			out.duration = math.Float64frombits(binary.BigEndian.Uint64(data[payloadStart:payloadEnd]))
		}
		if size < 0 {
			// Неизвестный размер: элемент занимает остаток родителя —
			// продолжать обход на этом уровне больше нечего.
			return
		}
		pos = payloadEnd
	}
}

func ebmlUint(raw []byte) int {
	value := 0
	for _, b := range raw {
		value = value<<8 | int(b)
	}
	return value
}

type webmMetadataResult struct {
	width    int
	height   int
	duration float64
}

// webmMetadata парсит PixelWidth/PixelHeight из Tracks/Video и Duration (мс)
// из Info. Длительность возвращается в секундах.
func webmMetadata(data []byte) (int, int, float64, bool) {
	out := &webmMetadataResult{}
	ebmlWalk(data, 0, len(data), 0, out)
	if out.width <= 0 || out.height <= 0 {
		return 0, 0, 0, false
	}
	return out.width, out.height, out.duration / 1000, true
}
