package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// Тесты проекции storage.FileType для форматов, которые сервер теперь
// принимает в интерфейсе: webp/webm/png определяются по магии байт.
func TestStorageFileTypeSniffsFormats(t *testing.T) {
	webp := append([]byte("RIFF\x00\x00\x00\x00"), []byte("WEBPVP8 ")...)
	if _, ok := storageFileType("application/octet-stream", webp).(*tg.StorageFileWebp); !ok {
		t.Fatalf("webp data = %T, want StorageFileWebp", storageFileType("application/octet-stream", webp))
	}
	webm := append([]byte{0x1a, 0x45, 0xdf, 0xa3}, make([]byte, 32)...)
	if _, ok := storageFileType("application/octet-stream", webm).(*tg.StorageFileMov); !ok {
		t.Fatalf("webm data = %T, want StorageFileMov (видео-контейнер)", storageFileType("application/octet-stream", webm))
	}
	mp4 := append([]byte("\x00\x00\x00\x18ftypisom"), make([]byte, 32)...)
	if _, ok := storageFileType("application/octet-stream", mp4).(*tg.StorageFileMov); !ok {
		t.Fatalf("mp4 data = %T, want StorageFileMov", storageFileType("application/octet-stream", mp4))
	}
	pngData := []byte("\x89PNG\r\n\x1a\nrest")
	if _, ok := storageFileType("", pngData).(*tg.StorageFilePng); !ok {
		t.Fatalf("png data = %T, want StorageFilePng", storageFileType("", pngData))
	}
	// mime-фоллбек: webm с корректным mime (без магии в первых байтах не бывает,
	// но проверяем поведение для пустых данных).
	if _, ok := storageFileType("video/webm", nil).(*tg.StorageFileMov); !ok {
		t.Fatalf("webm mime = %T, want StorageFileMov", storageFileType("video/webm", nil))
	}
	if _, ok := storageFileType("image/webp", nil).(*tg.StorageFileWebp); !ok {
		t.Fatalf("webp mime = %T, want StorageFileWebp", storageFileType("image/webp", nil))
	}
}
