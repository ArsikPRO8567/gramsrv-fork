package files

import (
        "context"
        "crypto/sha256"
        "fmt"
        "path/filepath"
        "strings"
        "time"

        "telesrv/internal/domain"
)

// ValidateStickerMaterialUpload is a pure check used by the admin console
// before a raw file is materialized into a loose Document.
func (s *Service) ValidateStickerMaterialUpload(fileName string, data []byte) (string, bool) {
        if len(data) == 0 || int64(len(data)) > domain.MaxStickerMaterialDocumentSize {
                return "", false
        }
        return detectStickerMaterialUploadMime(fileName, data)
}

// AdminUploadStickerMaterial turns a raw TGS, Lottie JSON, WebP, WebM (video
// sticker), or PNG upload into a loose Document that can be attached by
// AdminCreateStickerSet or AdminAddStickerToSet. Bytes go through the
// configured blob backend directly. WebM uploads are probed with ffprobe and
// get a synthesized documentAttributeVideo because the admin path has no
// client-supplied attributes; without it video stickers cannot pass the
// sticker-set material gates.
func (s *Service) AdminUploadStickerMaterial(ctx context.Context, fileName string, data []byte) (domain.Document, error) {
        if len(data) == 0 || int64(len(data)) > domain.MaxStickerMaterialDocumentSize {
                return domain.Document{}, domain.ErrStickerSetFileInvalid
        }
        mimeType, ok := detectStickerMaterialUploadMime(fileName, data)
        if !ok {
                return domain.Document{}, domain.ErrStickerSetFileInvalid
        }
        objectKey, err := s.blobs.Put(ctx, data)
        if err != nil {
                return domain.Document{}, err
        }
        sum := sha256.Sum256(data)
        docID := randomID()
        if err := s.media.PutFileBlob(ctx, domain.FileBlob{
                LocationKey: fmt.Sprintf("doc:%d", docID),
                Backend:     domain.MediaBackend(s.blobs.Name()),
                ObjectKey:   objectKey,
                Size:        int64(len(data)),
                SHA256:      append([]byte(nil), sum[:]...),
                MimeType:    mimeType,
        }); err != nil {
                return domain.Document{}, err
        }
        doc := domain.Document{
                ID:            docID,
                AccessHash:    randomID(),
                FileReference: randomFileReference(),
                Date:          int(time.Now().Unix()),
                MimeType:      mimeType,
                Size:          int64(len(data)),
                DCID:          s.dc,
                Attributes: []domain.DocumentAttribute{
                        {Kind: domain.DocAttrFilename, FileName: strings.TrimSpace(fileName)},
                },
        }
        if mimeType == stickerMaterialMimeWebM {
                doc, err = s.probeVideoStickerData(ctx, doc)
                if err != nil {
                        return domain.Document{}, err
                }
        }
        if err := s.media.PutDocument(ctx, doc); err != nil {
                return domain.Document{}, err
        }
        return doc, nil
}

// probeVideoStickerData проверяет webm-контейнер ffprobe'ом и синтезирует
// documentAttributeVideo (ширина/высота/длительность, как у видеостикеров,
// загруженных клиентами с атрибутом). Без этого атрибута домен-гейт
// IsStickerSetMaterial не пропустит видео в набор стикеров.
func (s *Service) probeVideoStickerData(ctx context.Context, doc domain.Document) (domain.Document, error) {
        if s.videos == nil {
                return domain.Document{}, domain.ErrStickerSetFileInvalid
        }
        data, ok := s.readStickerMaterialBlob(ctx, doc)
        if !ok {
                return domain.Document{}, domain.ErrStickerSetFileInvalid
        }
        meta, err := s.videos.Probe(ctx, data, doc.MimeType)
        if err != nil || meta.Width <= 0 || meta.Height <= 0 || meta.Duration <= 0 {
                return domain.Document{}, domain.ErrStickerSetFileInvalid
        }
        doc.Attributes = append(doc.Attributes, domain.DocumentAttribute{
                Kind:     domain.DocAttrVideo,
                W:        meta.Width,
                H:        meta.Height,
                Duration: meta.Duration,
                NoSound:  true,
        })
        return doc, nil
}

func detectStickerMaterialUploadMime(fileName string, data []byte) (string, bool) {
        ext := strings.ToLower(filepath.Ext(strings.TrimSpace(fileName)))
        webp := isWebPData(data)
        switch {
        case ext == ".tgs" || (len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b):
                return stickerMaterialMimeTGS, validTGSStickerData(data)
        case ext == ".webp" || webp:
                return stickerMaterialMimeWebP, webp
        case ext == ".webm" || isEBMLData(data):
                // Видеостикеры: контейнер EBML/matroska; валидность проверяет ffprobe
                // при материализации (см. probeVideoStickerData).
                return stickerMaterialMimeWebM, isEBMLData(data)
        case ext == ".png" || isPNGData(data):
                return stickerMaterialMimePNG, isPNGData(data)
        case ext == ".json":
                _, _, ok := lottieStickerDimensions(normalizeLottieStickerJSON(data))
                return stickerMaterialMimeJSON, ok
        default:
                return "", false
        }
}

func isWebPData(data []byte) bool {
        return len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// isEBMLData проверяет магию EBML/Matroska (1A 45 DF A3) — контейнер webm.
func isEBMLData(data []byte) bool {
        return len(data) >= 4 && data[0] == 0x1a && data[1] == 0x45 && data[2] == 0xdf && data[3] == 0xa3
}

// isPNGData проверяет сигнатуру PNG (89 50 4E 47 0D 0A 1A 0A).
func isPNGData(data []byte) bool {
        return len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' &&
                data[4] == 0x0d && data[5] == 0x0a && data[6] == 0x1a && data[7] == 0x0a
}

// AdminAddStickerToSet appends an already materialized document to any pack,
// with no ownership check.
func (s *Service) AdminAddStickerToSet(ctx context.Context, setID int64, item domain.StickerSetItemInput) (domain.StickerSet, []domain.Document, error) {
        set, docs, found, err := s.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: setID})
        if err != nil {
                return domain.StickerSet{}, nil, err
        }
        if !found || set.ID == 0 || set.Deleted {
                return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
        }
        if len(set.DocumentIDs) >= domain.MaxStickerSetItems {
                return domain.StickerSet{}, nil, domain.ErrStickerSetTooMuch
        }
        doc, err := s.loadStickerMaterialDocument(ctx, item.DocumentID, item.DocumentAccessHash)
        if err != nil {
                return domain.StickerSet{}, nil, err
        }
        doc, err = s.materialDocumentForStickerSet(ctx, doc, set.ID)
        if err != nil {
                return domain.StickerSet{}, nil, err
        }
        if containsInt64(set.DocumentIDs, doc.ID) {
                return set, docs, nil
        }
        emoji := strings.TrimSpace(item.Emoji)
        if err := validateStickerEmoji(emoji); err != nil {
                return domain.StickerSet{}, nil, err
        }
        doc, err = s.prepareStickerSetDocument(ctx, doc, set, emoji)
        if err != nil {
                return domain.StickerSet{}, nil, err
        }
        set.DocumentIDs = append(set.DocumentIDs, doc.ID)
        set.Count = len(set.DocumentIDs)
        set.Packs = addDocumentToStickerPacks(set.Packs, emoji, doc.ID)
        set.Keywords = upsertStickerKeywords(set.Keywords, parseStickerKeywords(doc.ID, item.Keywords))
        if set.ThumbDocumentID == 0 {
                setStickerSetThumbFromDocument(&set, doc)
        }
        set.Hash = stickerSetHash(set)
        docs = append(docs, doc)
        return s.persistStickerSetMutation(ctx, set, docs, []domain.Document{doc})
}

// AdminRemoveStickerFromSet detaches one document from any pack, with no
// ownership check.
func (s *Service) AdminRemoveStickerFromSet(ctx context.Context, setID int64, documentID int64) (domain.StickerSet, []domain.Document, error) {
        set, docs, found, err := s.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: setID})
        if err != nil {
                return domain.StickerSet{}, nil, err
        }
        if !found || set.ID == 0 || set.Deleted {
                return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
        }
        if len(set.DocumentIDs) <= 1 {
                return domain.StickerSet{}, nil, domain.ErrStickerSetEmpty
        }
        idx := indexInt64(set.DocumentIDs, documentID)
        if idx < 0 {
                return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
        }
        var removedDoc domain.Document
        removedFound := false
        for _, d := range docs {
                if d.ID == documentID {
                        removedDoc = d
                        removedFound = true
                        break
                }
        }
        if !removedFound {
                return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
        }
        set.DocumentIDs = removeInt64At(set.DocumentIDs, idx)
        set.Count = len(set.DocumentIDs)
        set.Packs = removeDocumentFromStickerPacks(set.Packs, documentID)
        set.Keywords = removeStickerKeywords(set.Keywords, documentID)
        removedDoc = detachStickerSetFromDocument(removedDoc)
        docs = removeDocumentByID(docs, documentID)
        if set.ThumbDocumentID == documentID {
                clearStickerSetThumb(&set)
                if len(docs) > 0 {
                        setStickerSetThumbFromDocument(&set, docs[0])
                }
        }
        set.Hash = stickerSetHash(set)
        return s.persistStickerSetMutation(ctx, set, docs, []domain.Document{removedDoc})
}
