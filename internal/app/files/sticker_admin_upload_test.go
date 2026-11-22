package files

import (
        "bytes"
        "context"
        "image"
        "image/color"
        "image/png"
        "os"
        "os/exec"
        "testing"

        "telesrv/internal/domain"
)

// adminUploadTestService поднимает files-сервис на real localfs + fake media store.
func adminUploadTestService(t *testing.T) *Service {
        t.Helper()
        media := newFakeMediaStore()
        blobs, err := NewLocalFS(t.TempDir())
        if err != nil {
                t.Fatalf("NewLocalFS: %v", err)
        }
        return NewService(media, blobs, 2, WithVideoThumbnailer(nil))
}

func encodeTestPNG(t *testing.T, w, h int) []byte {
        t.Helper()
        img := image.NewRGBA(image.Rect(0, 0, w, h))
        for y := 0; y < h; y++ {
                for x := 0; x < w; x++ {
                        img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 4), B: 128, A: 255})
                }
        }
        var buf bytes.Buffer
        if err := png.Encode(&buf, img); err != nil {
                t.Fatalf("png encode: %v", err)
        }
        return buf.Bytes()
}

// generateTestWebM рендит крошечный webm через ffmpeg (как gif-тесты — skip без ffmpeg).
func generateTestWebM(t *testing.T) []byte {
        t.Helper()
        ffmpeg, err := exec.LookPath("ffmpeg")
        if err != nil {
                t.Skipf("ffmpeg unavailable: %v", err)
        }
        out := t.TempDir() + "/probe.webm"
        cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
                "-f", "lavfi", "-i", "testsrc=duration=0.5:size=128x128:rate=10",
                "-c:v", "libvpx", out)
        if msg, err := cmd.CombinedOutput(); err != nil {
                t.Skipf("ffmpeg render webm: %v: %s", err, msg)
        }
        data, err := os.ReadFile(out)
        if err != nil || len(data) == 0 {
                t.Fatalf("read rendered webm: %v", err)
        }
        return data
}

func TestAdminUploadStickerMaterialAcceptsPNG(t *testing.T) {
        ctx := context.Background()
        svc := adminUploadTestService(t)
        data := encodeTestPNG(t, 64, 48)
        doc, err := svc.AdminUploadStickerMaterial(ctx, "sticker.png", data)
        if err != nil {
                t.Fatalf("png upload: %v", err)
        }
        if doc.MimeType != stickerMaterialMimePNG {
                t.Fatalf("png mime = %q, want image/png", doc.MimeType)
        }
        shaped, err := svc.ensureStickerMaterialShape(ctx, doc)
        if err != nil {
                t.Fatalf("png shape: %v", err)
        }
        if !documentHasAttribute(shaped, domain.DocAttrImageSize) || shaped.Attributes[len(shaped.Attributes)-1].W != 64 {
                t.Fatalf("png shaped attrs = %+v, want ImageSize 64x48", shaped.Attributes)
        }
        if !shaped.IsStickerSetMaterial() {
                t.Fatalf("png shaped document rejected by IsStickerSetMaterial")
        }
}

func TestAdminUploadStickerMaterialRejectsGarbagePNG(t *testing.T) {
        ctx := context.Background()
        svc := adminUploadTestService(t)
        if _, err := svc.AdminUploadStickerMaterial(ctx, "sticker.png", []byte("not-a-png")); err == nil {
                t.Fatalf("garbage png upload accepted, want ErrStickerSetFileInvalid")
        }
}

func TestAdminUploadStickerMaterialAcceptsWebMWithVideoAttribute(t *testing.T) {
        ctx := context.Background()
        svc := adminUploadTestService(t)
        data := generateTestWebM(t)
        doc, err := svc.AdminUploadStickerMaterial(ctx, "sticker.webm", data)
        if err != nil {
                t.Fatalf("webm upload: %v", err)
        }
        if doc.MimeType != stickerMaterialMimeWebM {
                t.Fatalf("webm mime = %q, want video/webm", doc.MimeType)
        }
        var video *domain.DocumentAttribute
        for i := range doc.Attributes {
                if doc.Attributes[i].Kind == domain.DocAttrVideo {
                        video = &doc.Attributes[i]
                }
        }
        if video == nil {
                t.Fatalf("webm upload has no synthesized video attribute: %+v", doc.Attributes)
        }
        if video.W != 128 || video.H != 128 || video.Duration <= 0 {
                t.Fatalf("video attribute = %+v, want 128x128 with positive duration", video)
        }
        if !doc.IsStickerSetMaterial() {
                t.Fatalf("webm document rejected by IsStickerSetMaterial")
        }
        if _, err := svc.ensureStickerMaterialShape(ctx, doc); err != nil {
                t.Fatalf("webm shape: %v", err)
        }
}

func TestAdminUploadStickerMaterialRejectsBrokenWebM(t *testing.T) {
        if _, err := exec.LookPath("ffprobe"); err != nil {
                t.Skipf("ffprobe unavailable: %v", err)
        }
        ctx := context.Background()
        svc := adminUploadTestService(t)
        // EBML-магия без валидного контента — ffprobe обязан отклонить.
        broken := append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x00}, 64)...)
        if _, err := svc.AdminUploadStickerMaterial(ctx, "sticker.webm", broken); err == nil {
                t.Fatalf("broken webm upload accepted, want ErrStickerSetFileInvalid")
        }
}

func TestImageMimeTypeRecognizesWebPAndGIF(t *testing.T) {
        webp := append([]byte("RIFF\x00\x00\x00\x00"), []byte("WEBPVP8 ")...)
        if got := imageMimeType(webp); got != "image/webp" {
                t.Fatalf("webp mime = %q, want image/webp", got)
        }
        if got := imageMimeType([]byte("GIF89a-more")); got != "image/gif" {
                t.Fatalf("gif mime = %q, want image/gif", got)
        }
        if got := imageMimeType([]byte("\x89PNG\r\n\x1a\nrest")); got != "image/png" {
                t.Fatalf("png mime = %q, want image/png", got)
        }
        if got := imageMimeType([]byte{0xff, 0xd8, 0xff, 0xe0}); got != "image/jpeg" {
                t.Fatalf("jpeg mime = %q, want image/jpeg", got)
        }
        if got := imageMimeType([]byte("plain")); got != "application/octet-stream" {
                t.Fatalf("unknown mime = %q, want application/octet-stream", got)
        }
}

func TestSynthesizeDocumentMediaAttributes(t *testing.T) {
        ctx := context.Background()
        svc := adminUploadTestService(t)

        // PNG-документ без атрибутов получает documentAttributeImageSize.
        data := encodeTestPNG(t, 96, 64)
        key, err := svc.blobs.Put(ctx, data)
        if err != nil {
                t.Fatalf("put png blob: %v", err)
        }
        doc := svc.synthesizeDocumentMediaAttributes(ctx, domain.Document{
                ID: 5001, MimeType: "image/png", Size: int64(len(data)),
        }, assembledUploadBlob{ObjectKey: key, Size: int64(len(data))})
        if len(doc.Attributes) != 1 || doc.Attributes[0].Kind != domain.DocAttrImageSize || doc.Attributes[0].W != 96 || doc.Attributes[0].H != 64 {
                t.Fatalf("png synthesized attrs = %+v, want single ImageSize 96x64", doc.Attributes)
        }

        // WebM-документ без атрибутов получает documentAttributeVideo (ffprobe).
        webm := generateTestWebM(t)
        key, err = svc.blobs.Put(ctx, webm)
        if err != nil {
                t.Fatalf("put webm blob: %v", err)
        }
        doc = svc.synthesizeDocumentMediaAttributes(ctx, domain.Document{
                ID: 5002, MimeType: "video/webm", Size: int64(len(webm)),
        }, assembledUploadBlob{ObjectKey: key, Size: int64(len(webm))})
        if len(doc.Attributes) != 1 || doc.Attributes[0].Kind != domain.DocAttrVideo || doc.Attributes[0].W != 128 {
                t.Fatalf("webm synthesized attrs = %+v, want single Video 128x128", doc.Attributes)
        }

        // Документ с video-атрибутом клиента не трогаем.
        doc = svc.synthesizeDocumentMediaAttributes(ctx, domain.Document{
                ID: 5003, MimeType: "video/webm", Size: int64(len(webm)),
                Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrVideo, W: 640, H: 360}},
        }, assembledUploadBlob{ObjectKey: key, Size: int64(len(webm))})
        if len(doc.Attributes) != 1 || doc.Attributes[0].W != 640 {
                t.Fatalf("client video attribute must be preserved: %+v", doc.Attributes)
        }

        // Стикеры пропускаются (их форму ведёт ensureStickerMaterialShape).
        doc = svc.synthesizeDocumentMediaAttributes(ctx, domain.Document{
                ID: 5004, MimeType: "image/png", Size: int64(len(data)),
                Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}},
        }, assembledUploadBlob{ObjectKey: key, Size: int64(len(data))})
        if len(doc.Attributes) != 1 {
                t.Fatalf("sticker document must be untouched: %+v", doc.Attributes)
        }
}
