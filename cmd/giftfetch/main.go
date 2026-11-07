// Command giftfetch snapshots the official Telegram star-gift catalog, the
// complete current upgrade-attribute pools, and all document resources
// referenced by either response. Catalog entries whose sticker document is
// unusable and unique-gift entries are skipped and reported in the manifest
// instead of failing the whole snapshot, and documents that cannot be
// downloaded after retries are recorded the same way so a re-run picks them
// up. It is a read-only fetcher: it never imports data into telesrv and
// never copies the authorization session out of the session storage; for
// documents stored on a foreign DC it transfers the existing authorization
// in memory (auth.exportAuthorization/auth.importAuthorization) to download
// them directly from the DC that stores them.
//
// Usage:
//
//	SESSION=/path/to/session giftfetch -out <directory>
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/log/logzap"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/telegram/downloader"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"

	"telesrv/internal/app/stargifts"
)

const (
	apiID              = 17349
	apiHash            = "344583e45741c457fe1862106095a5eb"
	defaultMaxDocBytes = int64(16 << 20)
	defaultDC          = 2
	defaultWorkers     = 8
	maxCatalogGifts    = 5000
	maxUpgradeAttrs    = 10000
	maxDocuments       = 50000
	maxWorkers         = 128
	floodWaitMargin    = 5 * time.Second
	// Metadata RPCs (catalog, upgrade attributes) are small single calls;
	// without a deadline a wedged server would stall the whole dump.
	metadataTimeout = 60 * time.Second
	// mediaConnectTimeout bounds the secondary file-DC connection setup:
	// key exchange plus one auth export/import round-trip.
	mediaConnectTimeout = 2 * time.Minute
	// downloadBaseTimeout bounds one download attempt for the first chunk;
	// larger resources get one extra second per 64 KiB part.
	downloadBaseTimeout = 90 * time.Second
	downloadAttempts    = 3
	heartbeatInterval   = 30 * time.Second
)

type catalogManifest struct {
	Schema                   int                           `json:"schema"`
	Hash                     int                           `json:"hash"`
	RawCatalog               fileArtifact                  `json:"raw_catalog"`
	GiftCount                int                           `json:"gift_count"`
	SkippedCount             int                           `json:"skipped_count"`
	Skipped                  []skippedItemManifest         `json:"skipped,omitempty"`
	ChatCount                int                           `json:"chat_count"`
	UserCount                int                           `json:"user_count"`
	UpgradeableGiftCount     int                           `json:"upgradeable_gift_count"`
	UpgradeAttributeSetCount int                           `json:"upgrade_attribute_set_count"`
	UpgradeAttributeCount    int                           `json:"upgrade_attribute_count"`
	UpgradeModelCount        int                           `json:"upgrade_model_count"`
	UpgradePatternCount      int                           `json:"upgrade_pattern_count"`
	UpgradeBackdropCount     int                           `json:"upgrade_backdrop_count"`
	MissingThumbCount        int                           `json:"missing_thumb_count"`
	Gifts                    []giftManifest                `json:"gifts"`
	UpgradeAttributeSets     []upgradeAttributeSetManifest `json:"upgrade_attribute_sets"`
	Documents                []documentManifest            `json:"documents"`
	TotalBytes               int64                         `json:"total_document_bytes"`
	BoundaryNote             string                        `json:"boundary_note"`
}

type giftManifest struct {
	Index               int                 `json:"index"`
	Kind                string              `json:"kind"`
	ID                  int64               `json:"id"`
	GiftID              int64               `json:"gift_id,omitempty"`
	Title               string              `json:"title,omitempty"`
	Slug                string              `json:"slug,omitempty"`
	Number              int                 `json:"number,omitempty"`
	Stars               int64               `json:"stars,omitempty"`
	ConvertStars        int64               `json:"convert_stars,omitempty"`
	UpgradeStars        int64               `json:"upgrade_stars,omitempty"`
	ResellMinStars      int64               `json:"resell_min_stars,omitempty"`
	Limited             bool                `json:"limited,omitempty"`
	SoldOut             bool                `json:"sold_out,omitempty"`
	Birthday            bool                `json:"birthday,omitempty"`
	RequirePremium      bool                `json:"require_premium,omitempty"`
	LimitedPerUser      bool                `json:"limited_per_user,omitempty"`
	PeerColorAvailable  bool                `json:"peer_color_available,omitempty"`
	Auction             bool                `json:"auction,omitempty"`
	AvailabilityRemains int                 `json:"availability_remains,omitempty"`
	AvailabilityTotal   int                 `json:"availability_total,omitempty"`
	AvailabilityResale  int64               `json:"availability_resale,omitempty"`
	AvailabilityIssued  int                 `json:"availability_issued,omitempty"`
	PerUserTotal        int                 `json:"per_user_total,omitempty"`
	PerUserRemains      int                 `json:"per_user_remains,omitempty"`
	FirstSaleDate       int                 `json:"first_sale_date,omitempty"`
	LastSaleDate        int                 `json:"last_sale_date,omitempty"`
	LockedUntilDate     int                 `json:"locked_until_date,omitempty"`
	AuctionSlug         string              `json:"auction_slug,omitempty"`
	GiftsPerRound       int                 `json:"gifts_per_round,omitempty"`
	AuctionStartDate    int                 `json:"auction_start_date,omitempty"`
	UpgradeVariants     int                 `json:"upgrade_variants,omitempty"`
	DocumentIDs         []int64             `json:"document_ids,omitempty"`
	Background          *backgroundManifest `json:"background,omitempty"`
}

type backgroundManifest struct {
	CenterColor int `json:"center_color"`
	EdgeColor   int `json:"edge_color"`
	TextColor   int `json:"text_color"`
}

type upgradeAttributeSetManifest struct {
	GiftID         int64                     `json:"gift_id"`
	RawAttributes  fileArtifact              `json:"raw_attributes"`
	AttributeCount int                       `json:"attribute_count"`
	Models         []upgradeModelManifest    `json:"models"`
	Patterns       []upgradePatternManifest  `json:"patterns"`
	Backdrops      []upgradeBackdropManifest `json:"backdrops"`
	DocumentIDs    []int64                   `json:"document_ids"`
}

type upgradeModelManifest struct {
	Name       string         `json:"name"`
	DocumentID int64          `json:"document_id"`
	Crafted    bool           `json:"crafted"`
	Rarity     rarityManifest `json:"rarity"`
}

type upgradePatternManifest struct {
	Name       string         `json:"name"`
	DocumentID int64          `json:"document_id"`
	Rarity     rarityManifest `json:"rarity"`
}

type upgradeBackdropManifest struct {
	Name         string         `json:"name"`
	BackdropID   int            `json:"backdrop_id"`
	CenterColor  int            `json:"center_color"`
	EdgeColor    int            `json:"edge_color"`
	PatternColor int            `json:"pattern_color"`
	TextColor    int            `json:"text_color"`
	Rarity       rarityManifest `json:"rarity"`
}

type rarityManifest struct {
	Kind          string `json:"kind"`
	ConstructorID string `json:"constructor_id"`
	Permille      *int   `json:"permille,omitempty"`
}

type documentManifest struct {
	ID                 int64          `json:"id"`
	Date               int            `json:"date"`
	DCID               int            `json:"dc_id"`
	MimeType           string         `json:"mime_type"`
	ExpectedSize       int64          `json:"expected_size"`
	FileName           string         `json:"file_name,omitempty"`
	StickerAlt         string         `json:"sticker_alt,omitempty"`
	Purposes           []string       `json:"purposes"`
	File               fileArtifact   `json:"file"`
	AnimationValidated bool           `json:"animation_validated,omitempty"`
	ValidationError    string         `json:"validation_error,omitempty"`
	Thumbs             []fileArtifact `json:"thumbs,omitempty"`
	MissingThumbs      []missingThumb `json:"missing_thumbs,omitempty"`
}

type missingThumb struct {
	Kind         string `json:"kind"`
	Type         string `json:"type"`
	ExpectedSize int64  `json:"expected_size"`
	Error        string `json:"error"`
}

type fileArtifact struct {
	Kind   string `json:"kind,omitempty"`
	Type   string `json:"type,omitempty"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type documentSource struct {
	document *tg.Document
	purposes map[string]struct{}
}

type addDocumentFunc func(tg.DocumentClass, string) (*tg.Document, error)

// Documents that the official server ships without a renderable sticker
// attribute, or without a decodable document body at all, cannot be rendered
// or imported by telesrv. giftfetch records the referencing gift or attribute
// as skipped instead of failing the whole snapshot.
var (
	errInvalidStickerDocument = errors.New("invalid document")
	errNoRenderableSticker    = errors.New("has neither sticker nor custom-emoji attribute")
)

func isSkippableDocumentError(err error) bool {
	return errors.Is(err, errInvalidStickerDocument) || errors.Is(err, errNoRenderableSticker)
}

type skippedItemManifest struct {
	CatalogIndex int    `json:"catalog_index,omitempty"`
	Kind         string `json:"kind"`
	GiftID       int64  `json:"gift_id,omitempty"`
	DocumentID   int64  `json:"document_id,omitempty"`
	Name         string `json:"name,omitempty"`
	Reason       string `json:"reason"`
}

type skipLog struct {
	mu    sync.Mutex
	items []skippedItemManifest
}

func (l *skipLog) add(item skippedItemManifest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, item)
}

func (l *skipLog) snapshot() []skippedItemManifest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]skippedItemManifest(nil), l.items...)
}

func (l *skipLog) countKind(kind string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, item := range l.items {
		if item.Kind == kind {
			count++
		}
	}
	return count
}

func giftClassIdentity(class tg.StarGiftClass) (string, int64) {
	switch gift := class.(type) {
	case *tg.StarGift:
		return "regular-gift", gift.ID
	case *tg.StarGiftUnique:
		return "unique-gift", gift.ID
	default:
		return fmt.Sprintf("%T", class), 0
	}
}

type documentRegistry struct {
	sources map[int64]*documentSource
}

func newDocumentRegistry() *documentRegistry {
	return &documentRegistry{sources: make(map[int64]*documentSource)}
}

func (r *documentRegistry) add(class tg.DocumentClass, purpose string) (*tg.Document, error) {
	doc, ok := class.(*tg.Document)
	if !ok || doc.ID == 0 {
		return nil, fmt.Errorf("%s references %w %T", purpose, errInvalidStickerDocument, class)
	}
	if !hasRenderableStickerAttribute(doc) {
		return doc, fmt.Errorf("%s document %d %w", purpose, doc.ID, errNoRenderableSticker)
	}
	existing := r.sources[doc.ID]
	if existing == nil {
		existing = &documentSource{document: doc, purposes: make(map[string]struct{})}
		r.sources[doc.ID] = existing
	} else if existing.document.Size != doc.Size || existing.document.MimeType != doc.MimeType {
		return nil, fmt.Errorf("document %d has conflicting metadata", doc.ID)
	}
	existing.purposes[purpose] = struct{}{}
	return doc, nil
}

func skippedDocumentID(doc *tg.Document) int64 {
	if doc == nil {
		return 0
	}
	return doc.ID
}

func main() {
	outDir := flag.String("out", "", "output directory")
	maxDocBytes := flag.Int64("max-document-bytes", defaultMaxDocBytes, "maximum bytes accepted for one document or thumbnail")
	skipThumbs := flag.Bool("skip-thumbs", false, "download main documents only")
	workers := flag.Int("workers", defaultWorkers, "concurrent document downloads")
	testServer := flag.Bool("test", false, "connect to the official Telegram test DC list (test DC 2 = 149.154.167.40:443)")
	dcID := flag.Int("dc", defaultDC, "DC id to connect to")
	addrOverride := flag.String("addr", "", "explicit host:port replacing the chosen DC address")
	reuseMetadata := flag.Bool("reuse-metadata", false, "reuse and strictly decode catalog.tl plus upgrade-attributes/*.tl instead of refetching metadata")
	allowedMissingThumbsRaw := flag.String("allow-missing-thumb", "", "comma-separated document_id:photo|video:type entries that may be recorded as explicitly missing after a failed download")
	debug := flag.Bool("debug", false, "log raw td protocol events (connections, retries, RPC errors)")
	flag.Parse()
	if strings.TrimSpace(*outDir) == "" {
		fmt.Fprintln(os.Stderr, "usage: SESSION=/path/to/session giftfetch -out <directory>")
		os.Exit(2)
	}
	session := strings.TrimSpace(os.Getenv("SESSION"))
	if session == "" {
		fmt.Fprintln(os.Stderr, "ERROR: SESSION is required")
		os.Exit(2)
	}
	if *maxDocBytes <= 0 || *maxDocBytes > 256<<20 {
		fmt.Fprintln(os.Stderr, "ERROR: max-document-bytes must be in (0, 256 MiB]")
		os.Exit(2)
	}
	if *workers <= 0 || *workers > maxWorkers {
		fmt.Fprintf(os.Stderr, "ERROR: workers must be in [1, %d]\n", maxWorkers)
		os.Exit(2)
	}
	allowedMissingThumbs, err := parseAllowedMissingThumbs(*allowedMissingThumbsRaw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	dcList, err := buildDCList(*testServer, *dcID, strings.TrimSpace(*addrOverride))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(2)
	}
	fmt.Printf("[target] mode=%s dc=%d addr=%s\n", modeName(*testServer), *dcID, describeConnectAddr(dcList, *dcID))
	// Media documents can live on any DC, and upload.getFile answers
	// FILE_MIGRATE_<dc> for them: gotd then dials the migration target using
	// the static DC list. Hardcoded fallback lists go stale (the shipped test
	// DC 1 address no longer matches the live server and every migration
	// attempt dies in a "wrong DC" reconnect loop), so the list is refreshed
	// from the server's own help.getConfig response before dumping.
	dcList, err = refreshDCList(ctx, dcList, *dcID, strings.TrimSpace(*addrOverride))
	if err != nil {
		fmt.Printf("[dc list] server config unavailable (%v); continuing with the built-in list\n", err)
	}
	for _, dc := range dcIDs(dcList) {
		fmt.Printf("[dc list] dc=%d addresses=%s\n", dc, describeDCAddresses(dcList, dc))
	}
	resolvedSession, err := resolveSessionPath(session)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(2)
	}
	options := telegram.Options{
		DC:             *dcID,
		DCList:         dcList,
		SessionStorage: &telegram.FileSessionStorage{Path: resolvedSession},
	}
	if *debug {
		options.Logger = logzap.New(verboseZapLogger())
	}
	client := telegram.NewClient(apiID, apiHash, options)
	if err := client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return errors.New("SESSION is not authorized")
		}
		fmt.Println("[session authorized]")
		return fetchCatalog(ctx, client.API(), dcList, *dcID, *outDir, *maxDocBytes, *skipThumbs, *workers, *reuseMetadata, allowedMissingThumbs)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func verboseZapLogger() *zap.Logger {
	encoderConfig := zap.NewDevelopmentEncoderConfig()
	encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	return zap.New(zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		zapcore.Lock(os.Stderr),
		zapcore.DebugLevel,
	))
}

func fetchCatalog(ctx context.Context, api *tg.Client, dcList dcs.List, mainDC int, outDir string, maxDocBytes int64, skipThumbs bool, workers int, reuseMetadata bool, allowedMissingThumbs map[string]struct{}) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	// Downloads are routed per-document: catalog resources can live on a
	// different DC than the session connection (the test server keeps gift
	// documents on DC 1 while the userbot session lives on DC 2).
	router := &mediaRouter{
		main:    api,
		mainDC:  mainDC,
		remotes: newRemoteDCs(ctx, dcList, api),
	}

	var catalog *tg.PaymentsStarGifts
	var rawArtifact fileArtifact
	if reuseMetadata {
		catalog = &tg.PaymentsStarGifts{}
		var err error
		rawArtifact, err = readTLArtifact(outDir, "catalog.tl", catalog)
		if err != nil {
			return fmt.Errorf("reuse catalog metadata: %w", err)
		}
		fmt.Printf("[metadata reused] path=%s\n", rawArtifact.Path)
	} else {
		rpcCtx, cancel := context.WithTimeout(ctx, metadataTimeout)
		result, err := api.PaymentsGetStarGifts(rpcCtx, 0)
		cancel()
		if err != nil {
			return fmt.Errorf("payments.getStarGifts: %w", err)
		}
		var ok bool
		catalog, ok = result.(*tg.PaymentsStarGifts)
		if !ok {
			return fmt.Errorf("payments.getStarGifts(hash=0) returned %T", result)
		}
		var raw bin.Buffer
		if err := catalog.Encode(&raw); err != nil {
			return fmt.Errorf("encode raw catalog: %w", err)
		}
		rawArtifact, err = writeArtifact(outDir, "catalog.tl", "tl", "", raw.Buf)
		if err != nil {
			return err
		}
	}
	if len(catalog.Gifts) > maxCatalogGifts {
		return fmt.Errorf("gift catalog has %d entries, limit is %d", len(catalog.Gifts), maxCatalogGifts)
	}
	manifest := catalogManifest{
		Schema:       2,
		Hash:         catalog.Hash,
		RawCatalog:   rawArtifact,
		ChatCount:    len(catalog.Chats),
		UserCount:    len(catalog.Users),
		BoundaryNote: "payments.getStarGifts(hash=0) plus payments.getStarGiftUpgradeAttributes for every currently upgradeable base gift: complete current official attribute definitions and referenced documents, not deleted historical definitions or precomputed model-pattern-backdrop combinations; gifts with unusable sticker documents and unique-gift entries are skipped and listed under skipped, the raw catalog.tl snapshot keeps every server entry; documents that could not be downloaded after retries are recorded under skipped with kind=document and are picked up by a re-run, which reuses every already downloaded artifact",
	}

	registry := newDocumentRegistry()
	addDocument := registry.add
	skips := &skipLog{}
	skippedGiftIDs := make(map[int64]struct{})

	for index, class := range catalog.Gifts {
		if unique, ok := class.(*tg.StarGiftUnique); ok {
			// telesrv imports only regular catalog gifts; unique entries stay
			// available in the raw catalog.tl snapshot.
			reason := "unique gift entries are not importable as official gifts; the raw catalog.tl snapshot keeps the full entry"
			skippedGiftIDs[unique.ID] = struct{}{}
			skips.add(skippedItemManifest{CatalogIndex: index, Kind: "unique-gift", GiftID: unique.ID, Reason: reason})
			fmt.Printf("[skipped gift] index=%d id=%d kind=unique-gift reason=%s\n", index, unique.ID, reason)
			continue
		}
		gm, err := collectGift(index, class, addDocument)
		if err != nil {
			if !isSkippableDocumentError(err) {
				return err
			}
			kind, giftID := giftClassIdentity(class)
			skippedGiftIDs[giftID] = struct{}{}
			skips.add(skippedItemManifest{CatalogIndex: index, Kind: kind, GiftID: giftID, Reason: err.Error()})
			fmt.Printf("[skipped gift] index=%d id=%d kind=%s reason=%v\n", index, giftID, kind, err)
			continue
		}
		manifest.Gifts = append(manifest.Gifts, gm)
	}
	manifest.GiftCount = len(manifest.Gifts)

	upgradeableGiftIDs := collectUpgradeableGiftIDs(catalog.Gifts)
	if len(skippedGiftIDs) > 0 {
		filtered := make([]int64, 0, len(upgradeableGiftIDs))
		for _, giftID := range upgradeableGiftIDs {
			if _, skipped := skippedGiftIDs[giftID]; skipped {
				continue
			}
			filtered = append(filtered, giftID)
		}
		upgradeableGiftIDs = filtered
	}
	manifest.UpgradeableGiftCount = len(upgradeableGiftIDs)
	for _, giftID := range upgradeableGiftIDs {
		attributeSet, err := fetchUpgradeAttributeSet(ctx, api, outDir, giftID, addDocument, reuseMetadata, skips)
		if err != nil {
			return err
		}
		manifest.UpgradeAttributeSets = append(manifest.UpgradeAttributeSets, attributeSet)
		manifest.UpgradeAttributeSetCount++
		manifest.UpgradeAttributeCount += attributeSet.AttributeCount
		manifest.UpgradeModelCount += len(attributeSet.Models)
		manifest.UpgradePatternCount += len(attributeSet.Patterns)
		manifest.UpgradeBackdropCount += len(attributeSet.Backdrops)
		fmt.Printf("[upgrade attributes] gift_id=%d models=%d patterns=%d backdrops=%d raw=%s\n", giftID, len(attributeSet.Models), len(attributeSet.Patterns), len(attributeSet.Backdrops), attributeSet.RawAttributes.Path)
	}
	if manifest.UpgradeAttributeSetCount != manifest.UpgradeableGiftCount {
		return fmt.Errorf("upgrade attribute set count mismatch: got %d want %d", manifest.UpgradeAttributeSetCount, manifest.UpgradeableGiftCount)
	}
	if len(registry.sources) > maxDocuments {
		return fmt.Errorf("catalog and upgrade attributes reference %d documents, limit is %d", len(registry.sources), maxDocuments)
	}

	ids := make([]int64, 0, len(registry.sources))
	for id := range registry.sources {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	fmt.Printf("[documents discovered] count=%d workers=%d\n", len(ids), workers)
	documentResults := make([]documentManifest, len(ids))
	group, downloadCtx := errgroup.WithContext(ctx)
	group.SetLimit(workers)
	var completed, failedDocuments atomic.Int64
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ticker.C:
				fmt.Printf("[progress] completed=%d failed=%d total=%d\n", completed.Load(), failedDocuments.Load(), len(ids))
			}
		}
	}()
	for index, id := range ids {
		index, id := index, id
		group.Go(func() error {
			dl := downloader.NewDownloader().WithRetryHandler(func(event downloader.RetryEvent) {
				fmt.Printf("[download retry] document=%d operation=%s attempt=%d error=%v\n", id, event.Operation, event.Attempt, event.Err)
				if strings.Contains(event.Err.Error(), "FLOOD_WAIT") {
					timer := time.NewTimer(floodWaitMargin)
					defer timer.Stop()
					select {
					case <-downloadCtx.Done():
					case <-timer.C:
					}
				}
			})
			dm, err := fetchDocument(downloadCtx, router, dl, outDir, registry.sources[id], maxDocBytes, skipThumbs, allowedMissingThumbs)
			if err != nil {
				if downloadCtx.Err() != nil {
					// The run itself was canceled or hit its global deadline;
					// keep the whole-dump error semantics in that case.
					return err
				}
				// One unreachable or broken document must not abort the
				// whole snapshot: record it and keep going. A re-run reuses
				// every already downloaded artifact and retries only these.
				failedDocuments.Add(1)
				skips.add(skippedItemManifest{Kind: "document", DocumentID: id, Reason: err.Error()})
				fmt.Printf("[failed document] id=%d error=%v\n", id, err)
				return nil
			}
			documentResults[index] = dm
			done := completed.Add(1)
			fmt.Printf("[document done] document=%d size=%d thumbs=%d (%d/%d)\n", dm.ID, dm.File.Size, len(dm.Thumbs), done, len(ids))
			if done == int64(len(ids)) || done%100 == 0 {
				fmt.Printf("[documents downloaded] completed=%d total=%d\n", done, len(ids))
			}
			return nil
		})
	}
	err := group.Wait()
	close(heartbeatDone)
	if err != nil {
		return err
	}
	for _, dm := range documentResults {
		manifest.TotalBytes += dm.File.Size
		for _, thumb := range dm.Thumbs {
			manifest.TotalBytes += thumb.Size
		}
		manifest.MissingThumbCount += len(dm.MissingThumbs)
		manifest.Documents = append(manifest.Documents, dm)
	}

	manifest.SkippedCount = len(skips.snapshot())
	manifest.Skipped = skips.snapshot()

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(outDir, "manifest.json"), append(encoded, '\n')); err != nil {
		return err
	}
	fmt.Printf("[complete] gifts=%d attribute_sets=%d attributes=%d documents=%d failed_documents=%d missing_thumbs=%d skipped=%d bytes=%d manifest=%s\n", len(manifest.Gifts), len(manifest.UpgradeAttributeSets), manifest.UpgradeAttributeCount, len(manifest.Documents), skips.countKind("document"), manifest.MissingThumbCount, manifest.SkippedCount, manifest.TotalBytes, filepath.Join(outDir, "manifest.json"))
	return nil
}

func collectUpgradeableGiftIDs(classes []tg.StarGiftClass) []int64 {
	ids := make([]int64, 0, len(classes))
	for _, class := range classes {
		gift, ok := class.(*tg.StarGift)
		if !ok || gift.ID == 0 || gift.UpgradeStars <= 0 && gift.UpgradeVariants <= 0 {
			continue
		}
		ids = append(ids, gift.ID)
	}
	return ids
}

func fetchUpgradeAttributeSet(ctx context.Context, api *tg.Client, root string, giftID int64, addDocument addDocumentFunc, reuseMetadata bool, skips *skipLog) (upgradeAttributeSetManifest, error) {
	var result *tg.PaymentsStarGiftUpgradeAttributes
	var rawArtifact fileArtifact
	if reuseMetadata {
		result = &tg.PaymentsStarGiftUpgradeAttributes{}
		var err error
		rawArtifact, err = readTLArtifact(root, filepath.Join("upgrade-attributes", fmt.Sprintf("%d.tl", giftID)), result)
		if err != nil {
			return upgradeAttributeSetManifest{}, fmt.Errorf("reuse upgrade attributes for gift %d: %w", giftID, err)
		}
	} else {
		var err error
		rpcCtx, cancel := context.WithTimeout(ctx, metadataTimeout)
		result, err = api.PaymentsGetStarGiftUpgradeAttributes(rpcCtx, giftID)
		cancel()
		if err != nil {
			return upgradeAttributeSetManifest{}, fmt.Errorf("payments.getStarGiftUpgradeAttributes(gift_id=%d): %w", giftID, err)
		}
		var raw bin.Buffer
		if err := result.Encode(&raw); err != nil {
			return upgradeAttributeSetManifest{}, fmt.Errorf("encode upgrade attributes for gift %d: %w", giftID, err)
		}
		rawArtifact, err = writeArtifact(root, filepath.Join("upgrade-attributes", fmt.Sprintf("%d.tl", giftID)), "tl", "", raw.Buf)
		if err != nil {
			return upgradeAttributeSetManifest{}, err
		}
	}
	if len(result.Attributes) == 0 {
		return upgradeAttributeSetManifest{}, fmt.Errorf("payments.getStarGiftUpgradeAttributes(gift_id=%d) returned no attributes", giftID)
	}
	if len(result.Attributes) > maxUpgradeAttrs {
		return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d has %d upgrade attributes, limit is %d", giftID, len(result.Attributes), maxUpgradeAttrs)
	}
	return collectUpgradeAttributes(giftID, result, rawArtifact, addDocument, skips)
}

func collectUpgradeAttributes(giftID int64, result *tg.PaymentsStarGiftUpgradeAttributes, rawArtifact fileArtifact, addDocument addDocumentFunc, skips *skipLog) (upgradeAttributeSetManifest, error) {
	if result == nil {
		return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d has nil upgrade-attribute result", giftID)
	}
	set := upgradeAttributeSetManifest{
		GiftID:        giftID,
		RawAttributes: rawArtifact,
	}
	documentIDs := make(map[int64]struct{})
	skippedAttributes := 0
	for index, attribute := range result.Attributes {
		switch value := attribute.(type) {
		case *tg.StarGiftAttributeModel:
			rarity, err := collectRarity(value.Rarity)
			if err != nil {
				return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d model %q rarity: %w", giftID, value.Name, err)
			}
			doc, err := addDocument(value.Document, fmt.Sprintf("gift:%d:upgrade-model:%s", giftID, value.Name))
			if err != nil {
				if !isSkippableDocumentError(err) {
					return upgradeAttributeSetManifest{}, err
				}
				skippedAttributes++
				skips.add(skippedItemManifest{Kind: "upgrade-model", GiftID: giftID, DocumentID: skippedDocumentID(doc), Name: value.Name, Reason: err.Error()})
				fmt.Printf("[skipped attribute] gift_id=%d kind=model name=%q reason=%v\n", giftID, value.Name, err)
				continue
			}
			set.Models = append(set.Models, upgradeModelManifest{Name: value.Name, DocumentID: doc.ID, Crafted: value.GetCrafted(), Rarity: rarity})
			documentIDs[doc.ID] = struct{}{}
		case *tg.StarGiftAttributePattern:
			rarity, err := collectRarity(value.Rarity)
			if err != nil {
				return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d pattern %q rarity: %w", giftID, value.Name, err)
			}
			doc, err := addDocument(value.Document, fmt.Sprintf("gift:%d:upgrade-pattern:%s", giftID, value.Name))
			if err != nil {
				if !isSkippableDocumentError(err) {
					return upgradeAttributeSetManifest{}, err
				}
				skippedAttributes++
				skips.add(skippedItemManifest{Kind: "upgrade-pattern", GiftID: giftID, DocumentID: skippedDocumentID(doc), Name: value.Name, Reason: err.Error()})
				fmt.Printf("[skipped attribute] gift_id=%d kind=pattern name=%q reason=%v\n", giftID, value.Name, err)
				continue
			}
			set.Patterns = append(set.Patterns, upgradePatternManifest{Name: value.Name, DocumentID: doc.ID, Rarity: rarity})
			documentIDs[doc.ID] = struct{}{}
		case *tg.StarGiftAttributeBackdrop:
			rarity, err := collectRarity(value.Rarity)
			if err != nil {
				return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d backdrop %q rarity: %w", giftID, value.Name, err)
			}
			set.Backdrops = append(set.Backdrops, upgradeBackdropManifest{
				Name: value.Name, BackdropID: value.BackdropID, CenterColor: value.CenterColor,
				EdgeColor: value.EdgeColor, PatternColor: value.PatternColor, TextColor: value.TextColor,
				Rarity: rarity,
			})
		default:
			return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d upgrade attribute at index %d has unsupported constructor %T", giftID, index, attribute)
		}
	}
	if len(set.Models) == 0 || len(set.Patterns) == 0 || len(set.Backdrops) == 0 {
		return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d incomplete upgrade attributes: models=%d patterns=%d backdrops=%d", giftID, len(set.Models), len(set.Patterns), len(set.Backdrops))
	}
	set.AttributeCount = len(set.Models) + len(set.Patterns) + len(set.Backdrops)
	if set.AttributeCount+skippedAttributes != len(result.Attributes) {
		return upgradeAttributeSetManifest{}, fmt.Errorf("gift %d parsed %d of %d upgrade attributes", giftID, set.AttributeCount, len(result.Attributes))
	}
	for id := range documentIDs {
		set.DocumentIDs = append(set.DocumentIDs, id)
	}
	sort.Slice(set.DocumentIDs, func(i, j int) bool { return set.DocumentIDs[i] < set.DocumentIDs[j] })
	return set, nil
}

func collectRarity(class tg.StarGiftAttributeRarityClass) (rarityManifest, error) {
	switch value := class.(type) {
	case *tg.StarGiftAttributeRarity:
		permille := value.Permille
		return rarityManifest{Kind: "permille", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID()), Permille: &permille}, nil
	case *tg.StarGiftAttributeRarityUncommon:
		return rarityManifest{Kind: "uncommon", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID())}, nil
	case *tg.StarGiftAttributeRarityRare:
		return rarityManifest{Kind: "rare", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID())}, nil
	case *tg.StarGiftAttributeRarityEpic:
		return rarityManifest{Kind: "epic", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID())}, nil
	case *tg.StarGiftAttributeRarityLegendary:
		return rarityManifest{Kind: "legendary", ConstructorID: fmt.Sprintf("0x%08x", value.TypeID())}, nil
	default:
		return rarityManifest{}, fmt.Errorf("unsupported constructor %T", class)
	}
}

func collectGift(index int, class tg.StarGiftClass, addDocument addDocumentFunc) (giftManifest, error) {
	switch gift := class.(type) {
	case *tg.StarGift:
		purpose := fmt.Sprintf("gift:%d:sticker", gift.ID)
		doc, err := addDocument(gift.Sticker, purpose)
		if err != nil {
			return giftManifest{}, err
		}
		gm := giftManifest{
			Index:               index,
			Kind:                "regular",
			ID:                  gift.ID,
			Stars:               gift.Stars,
			ConvertStars:        gift.ConvertStars,
			UpgradeStars:        gift.UpgradeStars,
			ResellMinStars:      gift.ResellMinStars,
			Limited:             gift.Limited,
			SoldOut:             gift.SoldOut,
			Birthday:            gift.Birthday,
			RequirePremium:      gift.RequirePremium,
			LimitedPerUser:      gift.LimitedPerUser,
			PeerColorAvailable:  gift.PeerColorAvailable,
			Auction:             gift.Auction,
			AvailabilityRemains: gift.AvailabilityRemains,
			AvailabilityTotal:   gift.AvailabilityTotal,
			AvailabilityResale:  gift.AvailabilityResale,
			PerUserTotal:        gift.PerUserTotal,
			PerUserRemains:      gift.PerUserRemains,
			FirstSaleDate:       gift.FirstSaleDate,
			LastSaleDate:        gift.LastSaleDate,
			LockedUntilDate:     gift.LockedUntilDate,
			AuctionSlug:         gift.AuctionSlug,
			GiftsPerRound:       gift.GiftsPerRound,
			AuctionStartDate:    gift.AuctionStartDate,
			UpgradeVariants:     gift.UpgradeVariants,
			Title:               gift.Title,
			DocumentIDs:         []int64{doc.ID},
		}
		if background, ok := gift.GetBackground(); ok {
			gm.Background = &backgroundManifest{CenterColor: background.CenterColor, EdgeColor: background.EdgeColor, TextColor: background.TextColor}
		}
		return gm, nil
	case *tg.StarGiftUnique:
		gm := giftManifest{
			Index:              index,
			Kind:               "unique",
			ID:                 gift.ID,
			GiftID:             gift.GiftID,
			Title:              gift.Title,
			Slug:               gift.Slug,
			Number:             gift.Num,
			RequirePremium:     gift.RequirePremium,
			AvailabilityIssued: gift.AvailabilityIssued,
			AvailabilityTotal:  gift.AvailabilityTotal,
		}
		for _, attribute := range gift.Attributes {
			var class tg.DocumentClass
			var purpose string
			switch value := attribute.(type) {
			case *tg.StarGiftAttributeModel:
				class = value.Document
				purpose = fmt.Sprintf("unique:%d:model:%s", gift.ID, value.Name)
			case *tg.StarGiftAttributePattern:
				class = value.Document
				purpose = fmt.Sprintf("unique:%d:pattern:%s", gift.ID, value.Name)
			default:
				continue
			}
			doc, err := addDocument(class, purpose)
			if err != nil {
				return giftManifest{}, err
			}
			gm.DocumentIDs = append(gm.DocumentIDs, doc.ID)
		}
		return gm, nil
	default:
		return giftManifest{}, fmt.Errorf("unsupported gift constructor %T at index %d", class, index)
	}
}

func fetchDocument(ctx context.Context, router *mediaRouter, dl *downloader.Downloader, root string, source *documentSource, maxBytes int64, skipThumbs bool, allowedMissingThumbs map[string]struct{}) (documentManifest, error) {
	doc := source.document
	if doc.Size <= 0 || doc.Size > maxBytes {
		return documentManifest{}, fmt.Errorf("document %d size %d is outside (0, %d]", doc.ID, doc.Size, maxBytes)
	}
	fileName, stickerAlt := documentNames(doc)
	ext := documentExtension(fileName, doc.MimeType)
	rel := filepath.Join("documents", fmt.Sprintf("%d%s", doc.ID, ext))
	data, reused, err := existingArtifact(root, rel, doc.Size, maxBytes)
	if err != nil {
		return documentManifest{}, err
	}
	if !reused {
		fmt.Printf("[network fetch] document=%d resource=document expected_size=%d part_size=%d\n", doc.ID, doc.Size, downloadPartSize(doc.Size))
		data, err = downloadWithRetry(ctx, router, dl, doc, "", doc.Size, maxBytes)
		if err != nil {
			return documentManifest{}, fmt.Errorf("download document %d: %w", doc.ID, err)
		}
	}
	if int64(len(data)) != doc.Size {
		return documentManifest{}, fmt.Errorf("document %d size mismatch: got %d want %d", doc.ID, len(data), doc.Size)
	}
	artifact, err := writeArtifact(root, rel, "document", "", data)
	if err != nil {
		return documentManifest{}, err
	}
	purposes := make([]string, 0, len(source.purposes))
	for purpose := range source.purposes {
		purposes = append(purposes, purpose)
	}
	sort.Strings(purposes)
	dm := documentManifest{
		ID:           doc.ID,
		Date:         doc.Date,
		DCID:         doc.DCID,
		MimeType:     doc.MimeType,
		ExpectedSize: doc.Size,
		FileName:     fileName,
		StickerAlt:   stickerAlt,
		Purposes:     purposes,
		File:         artifact,
	}
	if ext == ".tgs" || strings.EqualFold(doc.MimeType, "application/x-tgsticker") {
		validator := &stargifts.Service{}
		if _, err := validator.PrepareAnimation(fmt.Sprintf("%d.tgs", doc.ID), data); err != nil {
			dm.ValidationError = err.Error()
		} else {
			dm.AnimationValidated = true
		}
	}
	if skipThumbs {
		return dm, nil
	}
	thumbs, missingThumbs, err := fetchThumbs(ctx, router, dl, root, doc, maxBytes, allowedMissingThumbs)
	if err != nil {
		return documentManifest{}, err
	}
	dm.Thumbs = thumbs
	dm.MissingThumbs = missingThumbs
	return dm, nil
}

func fetchThumbs(ctx context.Context, router *mediaRouter, dl *downloader.Downloader, root string, doc *tg.Document, maxBytes int64, allowedMissingThumbs map[string]struct{}) ([]fileArtifact, []missingThumb, error) {
	seen := make(map[string]struct{})
	artifacts := make([]fileArtifact, 0, len(doc.Thumbs)+len(doc.VideoThumbs))
	missing := make([]missingThumb, 0)
	for _, class := range doc.Thumbs {
		thumbType := class.GetType()
		if thumbType == "" {
			continue
		}
		key := "photo:" + thumbType
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		var data []byte
		var err error
		var expectedSize int64
		downloadAttempted := false
		switch value := class.(type) {
		case *tg.PhotoCachedSize:
			data = append([]byte(nil), value.Bytes...)
		case *tg.PhotoStrippedSize:
			data = append([]byte(nil), value.Bytes...)
		case *tg.PhotoPathSize:
			data = append([]byte(nil), value.Bytes...)
		case *tg.PhotoSize:
			expectedSize = int64(value.Size)
			if expectedSize <= 0 || expectedSize > maxBytes {
				return nil, nil, fmt.Errorf("document %d photo thumb %q size %d is outside (0, %d]", doc.ID, thumbType, expectedSize, maxBytes)
			}
			rel := filepath.Join("thumbs", fmt.Sprintf("%d-photo-%s.bin", doc.ID, safePart(thumbType)))
			var reused bool
			data, reused, err = existingArtifact(root, rel, expectedSize, maxBytes)
			if err == nil && !reused {
				downloadAttempted = true
				fmt.Printf("[network fetch] document=%d resource=photo-thumb type=%s expected_size=%d part_size=%d\n", doc.ID, thumbType, expectedSize, downloadPartSize(expectedSize))
				data, err = downloadWithRetry(ctx, router, dl, doc, thumbType, expectedSize, maxBytes)
			}
		case *tg.PhotoSizeProgressive:
			if len(value.Sizes) == 0 {
				return nil, nil, fmt.Errorf("document %d progressive photo thumb %q has no sizes", doc.ID, thumbType)
			}
			expectedSize = int64(value.Sizes[len(value.Sizes)-1])
			if expectedSize <= 0 || expectedSize > maxBytes {
				return nil, nil, fmt.Errorf("document %d progressive photo thumb %q size %d is outside (0, %d]", doc.ID, thumbType, expectedSize, maxBytes)
			}
			rel := filepath.Join("thumbs", fmt.Sprintf("%d-photo-%s.bin", doc.ID, safePart(thumbType)))
			var reused bool
			data, reused, err = existingArtifact(root, rel, expectedSize, maxBytes)
			if err == nil && !reused {
				downloadAttempted = true
				fmt.Printf("[network fetch] document=%d resource=photo-thumb-progressive type=%s expected_size=%d part_size=%d\n", doc.ID, thumbType, expectedSize, downloadPartSize(expectedSize))
				data, err = downloadWithRetry(ctx, router, dl, doc, thumbType, expectedSize, maxBytes)
			}
		default:
			continue
		}
		if err != nil {
			if downloadAttempted && missingThumbAllowed(allowedMissingThumbs, doc.ID, "photo", thumbType) {
				missing = append(missing, missingThumb{Kind: "photo", Type: thumbType, ExpectedSize: expectedSize, Error: err.Error()})
				fmt.Printf("[missing thumb] document=%d kind=photo type=%s expected_size=%d error=%v\n", doc.ID, thumbType, expectedSize, err)
				continue
			}
			return nil, nil, fmt.Errorf("download document %d photo thumb %q: %w", doc.ID, thumbType, err)
		}
		if len(data) == 0 {
			continue
		}
		if expectedSize > 0 && int64(len(data)) != expectedSize {
			return nil, nil, fmt.Errorf("document %d photo thumb %q size mismatch: got %d want %d", doc.ID, thumbType, len(data), expectedSize)
		}
		rel := filepath.Join("thumbs", fmt.Sprintf("%d-photo-%s.bin", doc.ID, safePart(thumbType)))
		artifact, err := writeArtifact(root, rel, "photo", thumbType, data)
		if err != nil {
			return nil, nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	for _, class := range doc.VideoThumbs {
		value, ok := class.(*tg.VideoSize)
		if !ok || value.Type == "" {
			continue
		}
		key := "video:" + value.Type
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		rel := filepath.Join("thumbs", fmt.Sprintf("%d-video-%s.bin", doc.ID, safePart(value.Type)))
		data, reused, err := existingArtifact(root, rel, int64(value.Size), maxBytes)
		downloadAttempted := false
		if err == nil && !reused {
			downloadAttempted = true
			fmt.Printf("[network fetch] document=%d resource=video-thumb type=%s expected_size=%d part_size=%d\n", doc.ID, value.Type, value.Size, downloadPartSize(int64(value.Size)))
			data, err = downloadWithRetry(ctx, router, dl, doc, value.Type, int64(value.Size), maxBytes)
		}
		if err != nil {
			if downloadAttempted && missingThumbAllowed(allowedMissingThumbs, doc.ID, "video", value.Type) {
				missing = append(missing, missingThumb{Kind: "video", Type: value.Type, ExpectedSize: int64(value.Size), Error: err.Error()})
				fmt.Printf("[missing thumb] document=%d kind=video type=%s expected_size=%d error=%v\n", doc.ID, value.Type, value.Size, err)
				continue
			}
			return nil, nil, fmt.Errorf("download document %d video thumb %q: %w", doc.ID, value.Type, err)
		}
		artifact, err := writeArtifact(root, rel, "video", value.Type, data)
		if err != nil {
			return nil, nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].Kind != artifacts[j].Kind {
			return artifacts[i].Kind < artifacts[j].Kind
		}
		return artifacts[i].Type < artifacts[j].Type
	})
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Kind != missing[j].Kind {
			return missing[i].Kind < missing[j].Kind
		}
		return missing[i].Type < missing[j].Type
	})
	return artifacts, missing, nil
}

// mediaRouter hands every resource download to the tg.Client whose
// connection actually stores the document. Documents carry the DC that
// stores them (tg.Document.DCID); on the Telegram test server the gift
// catalog lives on DC 2 while all documents live on DC 1, so blind downloads
// through the session connection trigger upload.getFile's FILE_MIGRATE
// handling. The automatic gotd migration cannot be used here: its
// sub-connection creator does not apply the test-server DC id shift
// (dc+10000), the handshake answers "wrong DC" (protocol error 444) forever,
// and the download stalls. Explicit per-DC clients with one auth
// export/import round-trip avoid that path entirely.
type mediaRouter struct {
	main    *tg.Client
	mainDC  int
	remotes remoteInvoker
}

type remoteInvoker interface {
	acquire(ctx context.Context, dc int) (*tg.Client, error)
}

func (r *mediaRouter) forDocument(ctx context.Context, doc *tg.Document, forcedDC int) (*tg.Client, error) {
	target := forcedDC
	if target == 0 {
		target = doc.DCID
	}
	if target <= 0 || target == r.mainDC {
		return r.main, nil
	}
	api, err := r.remotes.acquire(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("route document %d to DC %d: %w", doc.ID, target, err)
	}
	return api, nil
}

// migrationTargetDC extracts the DC id from gotd's automatic migration
// failure ("create connection to DC %d: ..."), which surfaces when
// upload.getFile answered FILE_MIGRATE_<dc> on the session connection. It is
// a fallback for documents that do not carry a usable DCID.
func migrationTargetDC(err error) (int, bool) {
	var re = regexp.MustCompile(`create connection to DC (\d+)`)
	match := re.FindStringSubmatch(err.Error())
	if match == nil {
		return 0, false
	}
	dc, parseErr := strconv.Atoi(match[1])
	if parseErr != nil || dc <= 0 {
		return 0, false
	}
	return dc, true
}

// remoteDCs owns one lazily started telegram.Client per file DC. Every client
// generates its own auth key (the primary connection path applies the test
// DC id shift correctly) and receives the session authorization through a
// single auth.exportAuthorization/auth.importAuthorization pair.
type remoteDCs struct {
	parent  context.Context
	dcList  dcs.List
	mainAPI *tg.Client

	mu      sync.Mutex
	clients map[int]*remoteClient
}

type remoteClient struct {
	ready chan struct{}
	mu    sync.Mutex
	api   *tg.Client
	err   error
}

func newRemoteDCs(parent context.Context, dcList dcs.List, mainAPI *tg.Client) *remoteDCs {
	return &remoteDCs{
		parent:  parent,
		dcList:  dcList,
		mainAPI: mainAPI,
		clients: make(map[int]*remoteClient),
	}
}

func (r *remoteDCs) acquire(ctx context.Context, dc int) (*tg.Client, error) {
	r.mu.Lock()
	client, ok := r.clients[dc]
	if !ok {
		client = &remoteClient{ready: make(chan struct{})}
		r.clients[dc] = client
		go r.start(dc, client)
	}
	r.mu.Unlock()
	select {
	case <-client.ready:
		return client.api, client.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *remoteDCs) start(dc int, client *remoteClient) {
	runCtx, cancel := context.WithCancel(r.parent)
	defer cancel()
	second := telegram.NewClient(apiID, apiHash, telegram.Options{DC: dc, DCList: r.dcList})
	var connected *tg.Client
	err := second.Run(runCtx, func(ctx context.Context) error {
		connectCtx, cancel := context.WithTimeout(ctx, mediaConnectTimeout)
		defer cancel()
		exported, err := r.mainAPI.AuthExportAuthorization(connectCtx, dc)
		if err != nil {
			return fmt.Errorf("export auth to DC %d: %w", dc, err)
		}
		if _, err := tg.NewClient(second).AuthImportAuthorization(connectCtx, &tg.AuthImportAuthorizationRequest{
			ID:    exported.ID,
			Bytes: exported.Bytes,
		}); err != nil {
			return fmt.Errorf("import auth to DC %d: %w", dc, err)
		}
		connected = second.API()
		client.mu.Lock()
		client.api = connected
		client.mu.Unlock()
		close(client.ready)
		// Keep the client alive for the rest of the dump; returning would
		// stop Run and tear the connection down.
		<-ctx.Done()
		return ctx.Err()
	})
	cancel()
	if connected == nil {
		client.mu.Lock()
		if err == nil {
			err = errors.New("file DC connection closed before becoming ready")
		}
		client.err = fmt.Errorf("connect to DC %d: %w", dc, err)
		client.mu.Unlock()
		close(client.ready)
		// Drop the failed instance so a retry starts a fresh client.
		r.mu.Lock()
		delete(r.clients, dc)
		r.mu.Unlock()
	}
}

// downloadWithRetry bounds every attempt with a per-resource deadline and
// retries transient failures (the attempt's own timeout, FLOOD_WAIT) a few
// times before the caller records the resource as failed. Permanent errors
// are returned immediately: retrying cannot help there.
func downloadWithRetry(ctx context.Context, router *mediaRouter, dl *downloader.Downloader, doc *tg.Document, thumbType string, expectedSize, maxBytes int64) ([]byte, error) {
	resource := "document"
	if thumbType != "" {
		resource = "thumb:" + thumbType
	}
	var lastErr error
	forcedDC := 0
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		api, routeErr := router.forDocument(ctx, doc, forcedDC)
		if routeErr != nil {
			return nil, routeErr
		}
		attemptCtx, cancel := context.WithTimeout(ctx, downloadTimeout(expectedSize))
		data, err := download(attemptCtx, api, dl, doc, thumbType, expectedSize, maxBytes)
		cancel()
		if err == nil {
			return data, nil
		}
		if ctx.Err() != nil {
			// The whole run is winding down; report immediately.
			return nil, err
		}
		lastErr = err
		if forcedDC == 0 {
			if dc, ok := migrationTargetDC(err); ok {
				forcedDC = dc
			}
		}
		var wait time.Duration
		if floodWait, isFlood := tgerr.AsFloodWait(err); isFlood {
			wait = floodWait + floodWaitMargin
		} else if !errors.Is(err, context.DeadlineExceeded) {
			// Permanent failure (invalid location, size limits, ...):
			// retrying cannot help, let the caller record it.
			return nil, err
		} else {
			wait = time.Duration(attempt) * 2 * time.Second
		}
		fmt.Printf("[download retry] document=%d resource=%s attempt=%d wait=%s error=%v\n", doc.ID, resource, attempt, wait, err)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

// downloadTimeout bounds one download attempt: enough for the initial chunk
// plus one second per additional 64 KiB part.
func downloadTimeout(expectedSize int64) time.Duration {
	timeout := downloadBaseTimeout
	if expectedSize > 64<<10 {
		timeout += time.Duration(expectedSize/(64<<10)) * time.Second
	}
	return timeout
}

func download(ctx context.Context, api *tg.Client, dl *downloader.Downloader, doc *tg.Document, thumbType string, expectedSize, maxBytes int64) ([]byte, error) {
	location := &tg.InputDocumentFileLocation{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
		ThumbSize:     thumbType,
	}
	partSize := downloadPartSize(expectedSize)
	if expectedSize > 0 && expectedSize < int64(partSize) {
		// TDesktop and DrKLO issue ordinary non-precise upload.getFile requests
		// for regular file chunks. A known-size resource that fits in one valid
		// chunk needs neither gotd's precise mode nor an EOF probe.
		result, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   0,
			Limit:    partSize,
		})
		if err != nil {
			return nil, err
		}
		file, ok := result.(*tg.UploadFile)
		if !ok {
			return nil, fmt.Errorf("single-chunk upload.getFile returned %T", result)
		}
		if int64(len(file.Bytes)) > maxBytes {
			return nil, fmt.Errorf("download exceeds %d bytes", maxBytes)
		}
		return append([]byte(nil), file.Bytes...), nil
	}
	buffer := &boundedBuffer{max: maxBytes}
	if _, err := dl.WithPartSize(partSize).Download(api, location).Stream(ctx, buffer); err != nil {
		return nil, err
	}
	return append([]byte(nil), buffer.Bytes()...), nil
}

func downloadPartSize(expectedSize int64) int {
	const (
		unit = int64(4 << 10)
		max  = int64(512 << 10)
	)
	if expectedSize <= 0 || expectedSize >= max {
		return int(max)
	}
	// Non-precise upload.getFile limits must use the client-compatible chunk
	// ladder (4, 8, ..., 512 KiB), whose values also divide a 1 MiB window.
	// Merely rounding to an arbitrary 4 KiB multiple (for example 48 KiB)
	// is rejected with LIMIT_INVALID by some official file DCs.
	partSize := unit
	for partSize <= expectedSize && partSize < max {
		partSize *= 2
	}
	return int(partSize)
}

func existingArtifact(root, relative string, expectedSize, maxBytes int64) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, relative))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read existing artifact %q: %w", relative, err)
	}
	size := int64(len(data))
	if size <= 0 || size > maxBytes || expectedSize >= 0 && size != expectedSize {
		return nil, false, nil
	}
	return data, true, nil
}

type boundedBuffer struct {
	bytes.Buffer
	max int64
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.max - int64(b.Len())
	if remaining <= 0 {
		return 0, fmt.Errorf("download exceeds %d bytes", b.max)
	}
	if int64(len(p)) > remaining {
		written, _ := b.Buffer.Write(p[:remaining])
		return written, fmt.Errorf("download exceeds %d bytes", b.max)
	}
	return b.Buffer.Write(p)
}

func hasRenderableStickerAttribute(doc *tg.Document) bool {
	for _, attribute := range doc.Attributes {
		switch attribute.(type) {
		case *tg.DocumentAttributeSticker, *tg.DocumentAttributeCustomEmoji:
			return true
		}
	}
	return false
}

func documentNames(doc *tg.Document) (fileName, alt string) {
	for _, attribute := range doc.Attributes {
		switch value := attribute.(type) {
		case *tg.DocumentAttributeFilename:
			fileName = filepath.Base(value.FileName)
		case *tg.DocumentAttributeSticker:
			alt = value.Alt
		case *tg.DocumentAttributeCustomEmoji:
			alt = value.Alt
		}
	}
	return fileName, alt
}

func documentExtension(fileName, mimeType string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".tgs", ".webm", ".mp4", ".webp", ".png", ".jpg", ".jpeg":
		return ext
	}
	switch strings.ToLower(mimeType) {
	case "application/x-tgsticker", "application/gzip":
		return ".tgs"
	case "video/webm":
		return ".webm"
	case "video/mp4":
		return ".mp4"
	case "image/webp":
		return ".webp"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	default:
		return ".bin"
	}
}

func safePart(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

// buildDCList assembles the DC list giftfetch connects to. The production
// and test DC lists come from the td runtime; -addr replaces the address of
// the chosen DC so that private test deployments stay reachable.
func buildDCList(test bool, dc int, addr string) (dcs.List, error) {
	list := dcs.Prod()
	if test {
		list = dcs.Test()
	}
	if addr == "" {
		if !dcListHas(list, dc) {
			return dcs.List{}, fmt.Errorf("DC %d is missing from the %s DC list; pass -addr host:port to specify the server", dc, modeName(test))
		}
		return list, nil
	}
	return applyAddrOverride(list, dc, addr)
}

// applyAddrOverride replaces every option of the chosen DC with a single
// explicit host:port entry so private deployments stay reachable.
func applyAddrOverride(list dcs.List, dc int, addr string) (dcs.List, error) {
	host, portRaw, err := net.SplitHostPort(addr)
	if err != nil {
		return dcs.List{}, fmt.Errorf("invalid -addr %q: %v", addr, err)
	}
	if host == "" {
		return dcs.List{}, fmt.Errorf("invalid -addr %q: empty host", addr)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return dcs.List{}, fmt.Errorf("invalid -addr %q: port must be in [1, 65535]", addr)
	}
	options := make([]tg.DCOption, 0, len(list.Options)+1)
	for _, option := range list.Options {
		if option.ID == dc {
			continue
		}
		options = append(options, option)
	}
	options = append(options, tg.DCOption{ID: dc, IPAddress: host, Port: port, Ipv6: strings.Contains(host, ":")})
	list.Options = options
	return list, nil
}

// refreshDCList asks the connected server for its authoritative DC addresses
// (help.getConfig) and rebuilds the DC list from that response. The static
// lists compiled into the td runtime go stale: as of November 2026 the test
// DC 1 address they ship no longer matches the live server, and every
// FILE_MIGRATE to DC 1 died in an endless "wrong DC" (protocol error 444)
// reconnect loop. The server-provided config cannot go stale.
func refreshDCList(ctx context.Context, base dcs.List, dc int, addrOverride string) (dcs.List, error) {
	bootstrapCtx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	bootstrap := telegram.NewClient(apiID, apiHash, telegram.Options{DC: dc, DCList: base})
	var configOptions []tg.DCOption
	if err := bootstrap.Run(bootstrapCtx, func(ctx context.Context) error {
		cfg, err := bootstrap.API().HelpGetConfig(ctx)
		if err != nil {
			return fmt.Errorf("help.getConfig: %w", err)
		}
		configOptions = cfg.DCOptions
		return nil
	}); err != nil {
		return dcs.List{}, err
	}
	resolved, err := buildResolvedDCList(base, configOptions)
	if err != nil {
		return dcs.List{}, err
	}
	if addrOverride != "" {
		resolved, err = applyAddrOverride(resolved, dc, addrOverride)
		if err != nil {
			return dcs.List{}, err
		}
	}
	return resolved, nil
}

// buildResolvedDCList converts help.getConfig DC options into the dcs.List
// giftfetch hands to the td runtime, deduplicating entries and preserving
// the built-in list only for DCs the server did not describe.
func buildResolvedDCList(base dcs.List, configOptions []tg.DCOption) (dcs.List, error) {
	if len(configOptions) == 0 {
		return dcs.List{}, fmt.Errorf("server config contains no DC options")
	}
	type optionKey struct {
		id   int
		addr string
		port int
		ipv6 bool
	}
	seen := make(map[optionKey]struct{}, len(configOptions))
	options := make([]tg.DCOption, 0, len(configOptions))
	for _, option := range configOptions {
		if option.ID <= 0 || option.IPAddress == "" || option.Port <= 0 {
			continue
		}
		key := optionKey{id: option.ID, addr: option.IPAddress, port: option.Port, ipv6: option.Ipv6}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		options = append(options, option)
	}
	covered := make(map[int]struct{}, len(options))
	for _, option := range options {
		covered[option.ID] = struct{}{}
	}
	for _, option := range base.Options {
		if _, ok := covered[option.ID]; ok {
			continue
		}
		options = append(options, option)
		covered[option.ID] = struct{}{}
	}
	if len(options) == 0 {
		return dcs.List{}, fmt.Errorf("resolved DC list is empty")
	}
	return dcs.List{Options: options, Test: base.Test}, nil
}

func dcIDs(list dcs.List) []int {
	seen := make(map[int]struct{}, len(list.Options))
	ids := make([]int, 0, len(list.Options))
	for _, option := range list.Options {
		if _, ok := seen[option.ID]; ok {
			continue
		}
		seen[option.ID] = struct{}{}
		ids = append(ids, option.ID)
	}
	sort.Ints(ids)
	return ids
}

func describeDCAddresses(list dcs.List, dc int) string {
	addresses := make([]string, 0, 4)
	for _, option := range list.Options {
		if option.ID != dc || option.IPAddress == "" {
			continue
		}
		addresses = append(addresses, fmt.Sprintf("%s:%d", option.IPAddress, option.Port))
	}
	if len(addresses) == 0 {
		return "discovered at runtime"
	}
	return strings.Join(addresses, ",")
}

func dcListOption(list dcs.List, dc int) (tg.DCOption, bool) {
	for _, option := range list.Options {
		if option.ID == dc {
			return option, true
		}
	}
	return tg.DCOption{}, false
}

func dcListHas(list dcs.List, dc int) bool {
	_, ok := dcListOption(list, dc)
	return ok
}

func describeConnectAddr(list dcs.List, dc int) string {
	if option, ok := dcListOption(list, dc); ok && option.IPAddress != "" {
		return fmt.Sprintf("%s:%d", option.IPAddress, option.Port)
	}
	return "discovered at runtime"
}

func modeName(test bool) string {
	if test {
		return "test"
	}
	return "production"
}

func parseAllowedMissingThumbs(raw string) (map[string]struct{}, error) {
	allowed := make(map[string]struct{})
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid allow-missing-thumb %q: want document_id:photo|video:type", entry)
		}
		documentID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || documentID <= 0 {
			return nil, fmt.Errorf("invalid allow-missing-thumb document ID %q", parts[0])
		}
		kind := parts[1]
		if kind != "photo" && kind != "video" {
			return nil, fmt.Errorf("invalid allow-missing-thumb kind %q", kind)
		}
		thumbType := parts[2]
		if thumbType == "" || safePart(thumbType) != thumbType {
			return nil, fmt.Errorf("invalid allow-missing-thumb type %q", thumbType)
		}
		allowed[missingThumbKey(documentID, kind, thumbType)] = struct{}{}
	}
	return allowed, nil
}

func missingThumbAllowed(allowed map[string]struct{}, documentID int64, kind, thumbType string) bool {
	_, ok := allowed[missingThumbKey(documentID, kind, thumbType)]
	return ok
}

func missingThumbKey(documentID int64, kind, thumbType string) string {
	return fmt.Sprintf("%d:%s:%s", documentID, kind, thumbType)
}

func writeArtifact(root, relative, kind, artifactType string, data []byte) (fileArtifact, error) {
	path := filepath.Join(root, relative)
	if err := writeFileAtomic(path, data); err != nil {
		return fileArtifact{}, err
	}
	sum := sha256.Sum256(data)
	return fileArtifact{
		Kind:   kind,
		Type:   artifactType,
		Path:   filepath.ToSlash(relative),
		Size:   int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func readTLArtifact(root, relative string, target interface{ Decode(*bin.Buffer) error }) (fileArtifact, error) {
	data, err := os.ReadFile(filepath.Join(root, relative))
	if err != nil {
		return fileArtifact{}, err
	}
	if len(data) == 0 {
		return fileArtifact{}, fmt.Errorf("TL artifact %q is empty", relative)
	}
	buffer := &bin.Buffer{Buf: data}
	if err := target.Decode(buffer); err != nil {
		return fileArtifact{}, fmt.Errorf("decode TL artifact %q: %w", relative, err)
	}
	if len(buffer.Buf) != 0 {
		return fileArtifact{}, fmt.Errorf("TL artifact %q has %d trailing bytes", relative, len(buffer.Buf))
	}
	sum := sha256.Sum256(data)
	return fileArtifact{
		Kind:   "tl",
		Path:   filepath.ToSlash(relative),
		Size:   int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".giftfetch-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("replace %q: %w (remove existing: %v)", path, err, removeErr)
		}
		if err := os.Rename(tmpName, path); err != nil {
			return err
		}
	}
	return nil
}
