package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func nullablePermille(attribute domain.StarGiftCollectibleAttribute) any {
	if attribute.RarityKind != domain.StarGiftRarityPermille {
		return nil
	}
	return attribute.RarityPermille
}

func nullableSHA256(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableOfficialGiftJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func (s *StarGiftStore) PublishCollectibleRevision(ctx context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error) {
	write.SlugPrefix = strings.ToLower(strings.TrimSpace(write.SlugPrefix))
	write.Actor = strings.TrimSpace(write.Actor)
	write.CommandID = strings.TrimSpace(write.CommandID)
	if err := domain.ValidateStarGiftCollectibleWrite(write); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	var result domain.StarGiftCollectibleRevision
	err := withTx(ctx, s.db, "publish collectible star gift revision", func(tx pgx.Tx) error {
		var ignored int64
		if err := tx.QueryRow(ctx, `SELECT gift_id FROM star_gift_catalog WHERE gift_id=$1 FOR UPDATE`, write.GiftID).Scan(&ignored); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftNotFound
			}
			return fmt.Errorf("lock collectible catalog gift: %w", err)
		}
		var revision int
		if err := tx.QueryRow(ctx, `
SELECT COALESCE(MAX(revision), 0) + 1 FROM star_gift_collectible_revisions WHERE gift_id=$1`, write.GiftID).Scan(&revision); err != nil {
			return fmt.Errorf("allocate collectible revision: %w", err)
		}
		var revisionID int64
		if err := tx.QueryRow(ctx, `
INSERT INTO star_gift_collectible_revisions
    (gift_id, revision, upgrade_stars, supply_total, slug_prefix, status, created_by, command_id,
     official_gift_id, source_manifest_sha256)
VALUES ($1,$2,$3,$4,$5,'draft',$6,$7,NULLIF($8::bigint,0),$9)
RETURNING id`, write.GiftID, revision, write.UpgradeStars, write.SupplyTotal, write.SlugPrefix, write.Actor, write.CommandID,
			write.OfficialGiftID, nullableSHA256(write.SourceManifestSHA256)).Scan(&revisionID); err != nil {
			return fmt.Errorf("insert collectible revision: %w", err)
		}
		media := NewMediaStore(tx)
		insertAnimated := func(table string, attributes []domain.StarGiftCollectibleAttribute, models bool) error {
			for _, attribute := range attributes {
				// Раунд 33: скопированные атрибуты (ReuseDocument) ссылаются на
				// УЖЕ сохранённые документ и blob — перезапись дедуплицированных
				// строк не нужна, достаточно вставить строку атрибута с чужим
				// document_id (FK documents проверит существование).
				if !attribute.ReuseDocument {
					if err := media.PutDocument(ctx, *attribute.Document); err != nil {
						return fmt.Errorf("put collectible %s document: %w", attribute.Kind, err)
					}
					if err := media.PutFileBlob(ctx, *attribute.Blob); err != nil {
						return fmt.Errorf("put collectible %s blob: %w", attribute.Kind, err)
					}
				}
				animation := attribute.Animation
				var query string
				if models {
					query = fmt.Sprintf(`
INSERT INTO %s
    (collectible_revision_id, name, document_id, animation_json, animation_sha256,
     source_name, source_format, width, height, frame_rate, in_point, out_point,
     rarity_kind, rarity_permille, crafted, official_document_id, sort_order)
VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, table)
					if _, err := tx.Exec(ctx, query, revisionID, strings.TrimSpace(attribute.Name), attribute.Document.ID,
						string(animation.JSON), animation.SHA256, animation.SourceName, string(animation.SourceFormat),
						animation.Width, animation.Height, animation.FrameRate, animation.InPoint, animation.OutPoint,
						string(attribute.RarityKind), nullablePermille(attribute), attribute.Crafted,
						nullablePositiveInt64(attribute.OfficialDocumentID), attribute.SortOrder); err != nil {
						return fmt.Errorf("insert collectible %s attribute: %w", attribute.Kind, err)
					}
				} else {
					query = fmt.Sprintf(`
INSERT INTO %s
    (collectible_revision_id, name, document_id, animation_json, animation_sha256,
     source_name, source_format, width, height, frame_rate, in_point, out_point,
     rarity_kind, rarity_permille, official_document_id, sort_order)
VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, table)
					if _, err := tx.Exec(ctx, query, revisionID, strings.TrimSpace(attribute.Name), attribute.Document.ID,
						string(animation.JSON), animation.SHA256, animation.SourceName, string(animation.SourceFormat),
						animation.Width, animation.Height, animation.FrameRate, animation.InPoint, animation.OutPoint,
						string(attribute.RarityKind), nullablePermille(attribute), nullablePositiveInt64(attribute.OfficialDocumentID),
						attribute.SortOrder); err != nil {
						return fmt.Errorf("insert collectible %s attribute: %w", attribute.Kind, err)
					}
				}
			}
			return nil
		}
		if err := insertAnimated("star_gift_collectible_models", write.Models, true); err != nil {
			return err
		}
		if err := insertAnimated("star_gift_collectible_patterns", write.Patterns, false); err != nil {
			return err
		}
		for _, attribute := range write.Backdrops {
			if _, err := tx.Exec(ctx, `
INSERT INTO star_gift_collectible_backdrops
    (collectible_revision_id, name, backdrop_id, center_color, edge_color, pattern_color,
     text_color, rarity_kind, rarity_permille, sort_order)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, revisionID, strings.TrimSpace(attribute.Name), attribute.BackdropID,
				attribute.CenterColor, attribute.EdgeColor, attribute.PatternColor, attribute.TextColor,
				string(attribute.RarityKind), nullablePermille(attribute), attribute.SortOrder); err != nil {
				return fmt.Errorf("insert collectible backdrop: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
UPDATE star_gift_collectible_revisions SET status='published', published_at=now() WHERE id=$1`, revisionID); err != nil {
			return fmt.Errorf("publish collectible revision: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE star_gift_catalog SET collectible_revision_id=$2, updated_at=now() WHERE gift_id=$1`, write.GiftID, revisionID); err != nil {
			return fmt.Errorf("activate collectible revision: %w", err)
		}
		var err error
		result, err = collectibleRevisionByID(ctx, tx, revisionID)
		return err
	})
	return result, err
}

func (s *StarGiftStore) ActiveCollectibleRevision(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, bool, error) {
	revisionID, ok, err := activeCollectibleRevisionID(ctx, s.db, giftID)
	if err != nil || !ok {
		return domain.StarGiftCollectibleRevision{}, ok, err
	}
	revision, err := collectibleRevisionByID(ctx, s.db, revisionID)
	if err != nil {
		return domain.StarGiftCollectibleRevision{}, false, err
	}
	return revision, true, nil
}

// CollectibleRevisionFull (раунд 32) читает ЛЮБУЮ ревизию коллекции по id
// независимо от статуса с полными метаданными анимаций (JSON) — источник для
// копирования моделей/узоров/фонов в новую ревизию из админки.
func (s *StarGiftStore) CollectibleRevisionFull(ctx context.Context, revisionID int64) (domain.StarGiftCollectibleRevision, error) {
	return collectibleRevisionByID(ctx, s.db, revisionID)
}

// SetCollectibleSupplyTotal (раунд 34) правит лимит возможных улучшенных
// подарков ОПУБЛИКОВАННОЙ ревизии коллекции подарка. Ревизия неизменяема по
// атрибутам, но supply_total — административный лимит: когда supply каталога
// увеличен, лимит улучшений должен вырасти, иначе апгрейд блокируется при
// issued >= supply_total. Лимит можно только повысить (или оставить равным):
// значение ниже issued отклоняется, чтобы выпущенные подарки не «потерялись».
// Активация не требуется — апгрейд-путь читает строку published-ревизии
// напрямую; issued/slug не трогаются.
func (s *StarGiftStore) SetCollectibleSupplyTotal(ctx context.Context, write domain.StarGiftCollectibleSupplyWrite) (domain.StarGiftCollectibleRevision, error) {
	if write.GiftID <= 0 || write.SupplyTotal <= 0 {
		return domain.StarGiftCollectibleRevision{}, domain.ErrStarGiftCollectibleInvalid
	}
	var revision domain.StarGiftCollectibleRevision
	err := withTx(ctx, s.db, "set collectible supply total", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT active_revision_id FROM star_gift_catalog WHERE gift_id=$1 FOR UPDATE`, write.GiftID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftNotFound
			}
			return fmt.Errorf("lock collectible catalog gift: %w", err)
		}
		var revisionID int64
		var issued, supplyTotal int
		var status string
		err := tx.QueryRow(ctx, `
SELECT r.id, r.issued, r.supply_total, r.status FROM star_gift_catalog c
JOIN star_gift_collectible_revisions r ON r.id = c.collectible_revision_id
WHERE c.gift_id=$1 FOR UPDATE OF r`, write.GiftID).Scan(&revisionID, &issued, &supplyTotal, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrStarGiftCollectibleInvalid
		}
		if err != nil {
			return fmt.Errorf("lock collectible revision: %w", err)
		}
		if status != "published" {
			return domain.ErrStarGiftCollectibleInvalid
		}
		// Лимит только повышается (или не меняется): понижение ниже выпущенного
		// количества и любой сброс запрещены — и здесь (чистая ошибка), и
		// guard-триггером 0189 (последняя линия защиты).
		if write.SupplyTotal < issued || write.SupplyTotal < supplyTotal {
			return domain.ErrStarGiftCollectibleInvalid
		}
		if _, err := tx.Exec(ctx, `
UPDATE star_gift_collectible_revisions SET supply_total=$2 WHERE id=$1`,
			revisionID, write.SupplyTotal); err != nil {
			return fmt.Errorf("update collectible supply total: %w", err)
		}
		revision, err = collectibleRevisionByID(ctx, tx, revisionID)
		return err
	})
	if err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	return revision, nil
}

// CollectibleRevisionSummaries (раунд 34) перечисляет ревизии коллекции
// (новые первыми) в лёгкой проекции — для выбора источника копирования
// атрибутов в админке без загрузки полных метаданных всех ревизий.
// Раунд 35: непустой slug ищет ревизии по slug_prefix коллекции (одна
// коллекция может публиковаться разными подарками). Раунд 36 (обратная
// связь «ревизий в выборке просто нет»): запрос объединяет ОБА источника —
// slug И gift_id (slug мог смениться между публикациями, а у нового подарка
// своих ревизий ещё нет), и если оба пусты — возвращаются последние ревизии
// всех коллекций (scope "all"), чтобы выбор источника оставался доступным.
func (s *StarGiftStore) CollectibleRevisionSummaries(ctx context.Context, giftID int64, slug string) ([]domain.StarGiftCollectibleRevisionSummary, string, error) {
	const listLimit = 100
	trimmed := strings.ToLower(strings.TrimSpace(slug))
	base := `
SELECT r.id, r.gift_id, r.revision, r.slug_prefix, r.status, r.upgrade_stars, r.supply_total, r.issued,
       (SELECT COUNT(*) FROM star_gift_collectible_models m WHERE m.collectible_revision_id = r.id),
       (SELECT COUNT(*) FROM star_gift_collectible_patterns p WHERE p.collectible_revision_id = r.id),
       (SELECT COUNT(*) FROM star_gift_collectible_backdrops b WHERE b.collectible_revision_id = r.id),
       r.created_by, r.created_at
FROM star_gift_collectible_revisions r
`
	scan := func(rows pgx.Rows) ([]domain.StarGiftCollectibleRevisionSummary, error) {
		defer rows.Close()
		out := make([]domain.StarGiftCollectibleRevisionSummary, 0, 8)
		for rows.Next() {
			var summary domain.StarGiftCollectibleRevisionSummary
			if err := rows.Scan(&summary.RevisionID, &summary.GiftID, &summary.Revision, &summary.SlugPrefix, &summary.Status,
				&summary.UpgradeStars, &summary.SupplyTotal, &summary.Issued,
				&summary.ModelCount, &summary.PatternCount, &summary.BackdropCount,
				&summary.CreatedBy, &summary.CreatedAt); err != nil {
				return nil, fmt.Errorf("scan collectible revision summary: %w", err)
			}
			out = append(out, summary)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate collectible revision summaries: %w", err)
		}
		return out, nil
	}
	if trimmed != "" {
		rows, err := s.db.Query(ctx, base+`WHERE r.slug_prefix=$1 OR r.gift_id=$2
ORDER BY r.revision DESC, r.id DESC LIMIT $3`, trimmed, giftID, listLimit)
		if err != nil {
			return nil, "", fmt.Errorf("list collectible revision summaries: %w", err)
		}
		out, err := scan(rows)
		if err != nil || len(out) > 0 {
			return out, "slug", err
		}
	} else {
		rows, err := s.db.Query(ctx, base+`WHERE r.gift_id=$1
ORDER BY r.revision DESC, r.id DESC LIMIT $2`, giftID, listLimit)
		if err != nil {
			return nil, "", fmt.Errorf("list collectible revision summaries: %w", err)
		}
		out, err := scan(rows)
		if err != nil || len(out) > 0 {
			return out, "gift", err
		}
	}
	// Fallback (раунд 36): ни slug, ни gift_id не дали ревизий — показываем
	// последние ревизии всех коллекций, чтобы источник копирования всегда
	// был доступен (админ видит slug/gift в подписи и выбирает нужную).
	rows, err := s.db.Query(ctx, base+`ORDER BY r.revision DESC, r.id DESC LIMIT $1`, 50)
	if err != nil {
		return nil, "", fmt.Errorf("list all collectible revision summaries: %w", err)
	}
	out, err := scan(rows)
	return out, "all", err
}

func (s *StarGiftStore) ActiveCollectibleProjection(ctx context.Context, giftID int64, samplePerKind int) (domain.StarGiftCollectibleRevision, bool, error) {
	revisionID, ok, err := activeCollectibleRevisionID(ctx, s.db, giftID)
	if err != nil || !ok {
		return domain.StarGiftCollectibleRevision{}, ok, err
	}
	revision, err := collectibleRevisionProjectionByID(ctx, s.db, revisionID, samplePerKind)
	if err != nil {
		return domain.StarGiftCollectibleRevision{}, false, err
	}
	return revision, true, nil
}

func activeCollectibleRevisionID(ctx context.Context, db sqlcgen.DBTX, giftID int64) (int64, bool, error) {
	var revisionID int64
	err := db.QueryRow(ctx, `
SELECT collectible_revision_id FROM star_gift_catalog
WHERE gift_id=$1 AND collectible_revision_id IS NOT NULL`, giftID).Scan(&revisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("get active collectible revision: %w", err)
	}
	return revisionID, true, nil
}

func (s *StarGiftStore) CollectibleAvailability(ctx context.Context, giftIDs []int64) (map[int64]domain.StarGiftCollectibleAvailability, error) {
	out := make(map[int64]domain.StarGiftCollectibleAvailability, len(giftIDs))
	if len(giftIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT c.gift_id, r.upgrade_stars, r.supply_total, r.issued
FROM star_gift_catalog c
JOIN star_gift_collectible_revisions r ON r.id=c.collectible_revision_id
WHERE c.gift_id=ANY($1) AND r.status='published'`, giftIDs)
	if err != nil {
		return nil, fmt.Errorf("list collectible availability: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var giftID int64
		var availability domain.StarGiftCollectibleAvailability
		if err := rows.Scan(&giftID, &availability.UpgradeStars, &availability.SupplyTotal, &availability.Issued); err != nil {
			return nil, fmt.Errorf("scan collectible availability: %w", err)
		}
		out[giftID] = availability
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list collectible availability rows: %w", err)
	}
	return out, nil
}

func collectibleRevisionByID(ctx context.Context, db sqlcgen.DBTX, revisionID int64) (domain.StarGiftCollectibleRevision, error) {
	return readCollectibleRevisionByID(ctx, db, revisionID, collectibleRevisionReadOptions{includeAnimationJSON: true})
}

func collectibleRevisionProjectionByID(ctx context.Context, db sqlcgen.DBTX, revisionID int64, samplePerKind int) (domain.StarGiftCollectibleRevision, error) {
	return readCollectibleRevisionByID(ctx, db, revisionID, collectibleRevisionReadOptions{samplePerKind: samplePerKind})
}

type collectibleRevisionReadOptions struct {
	includeAnimationJSON bool
	samplePerKind        int
}

func readCollectibleRevisionByID(ctx context.Context, db sqlcgen.DBTX, revisionID int64, options collectibleRevisionReadOptions) (domain.StarGiftCollectibleRevision, error) {
	var revision domain.StarGiftCollectibleRevision
	var status string
	var publishedAt pgtype.Timestamptz
	if err := db.QueryRow(ctx, `
SELECT id, gift_id, revision, upgrade_stars, supply_total, issued, slug_prefix, status,
       created_by, created_at, published_at, COALESCE(official_gift_id,0), source_manifest_sha256
FROM star_gift_collectible_revisions WHERE id=$1`, revisionID).Scan(
		&revision.ID, &revision.GiftID, &revision.Revision, &revision.UpgradeStars, &revision.SupplyTotal,
		&revision.Issued, &revision.SlugPrefix, &status, &revision.CreatedBy, &revision.CreatedAt, &publishedAt,
		&revision.OfficialGiftID, &revision.SourceManifestSHA256,
	); err != nil {
		return domain.StarGiftCollectibleRevision{}, fmt.Errorf("get collectible revision: %w", err)
	}
	revision.Published = status == "published"
	if publishedAt.Valid {
		revision.PublishedAt = publishedAt.Time
	}
	var err error
	if revision.Models, err = listAnimatedCollectibleAttributes(ctx, db, revisionID, domain.StarGiftCollectibleModel, options); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	if revision.Patterns, err = listAnimatedCollectibleAttributes(ctx, db, revisionID, domain.StarGiftCollectiblePattern, options); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	if revision.Backdrops, err = listCollectibleBackdrops(ctx, db, revisionID, options); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	return revision, nil
}

func listAnimatedCollectibleAttributes(ctx context.Context, db sqlcgen.DBTX, revisionID int64, kind domain.StarGiftCollectibleAttributeKind, options collectibleRevisionReadOptions) ([]domain.StarGiftCollectibleAttribute, error) {
	table := "star_gift_collectible_models"
	if kind == domain.StarGiftCollectiblePattern {
		table = "star_gift_collectible_patterns"
	} else if kind != domain.StarGiftCollectibleModel {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	craftedExpression := "false"
	if kind == domain.StarGiftCollectibleModel {
		craftedExpression = "a.crafted"
	}
	animationJSONExpression := "''::text"
	if options.includeAnimationJSON {
		animationJSONExpression = "a.animation_json::text"
	}
	prefix := ""
	from := fmt.Sprintf("%s a JOIN documents d ON d.id=a.document_id", table)
	args := []any{revisionID}
	if options.samplePerKind > 0 {
		extra := ""
		if kind == domain.StarGiftCollectibleModel {
			extra = " AND NOT crafted"
		}
		prefix = fmt.Sprintf(`WITH picked AS MATERIALIZED (
    SELECT id FROM %s
    WHERE collectible_revision_id=$1 AND rarity_kind='permille' AND rarity_permille > 0%s
    ORDER BY random()
    LIMIT $2
)`, table, extra)
		from = fmt.Sprintf("picked p JOIN %s a ON a.id=p.id JOIN documents d ON d.id=a.document_id", table)
		args = append(args, options.samplePerKind)
	}
	rows, err := db.Query(ctx, fmt.Sprintf(`
%s
SELECT a.id, a.collectible_revision_id, a.name, a.rarity_kind, COALESCE(a.rarity_permille,0),
       %s, COALESCE(a.official_document_id,0), a.sort_order,
       %s, a.animation_sha256, a.source_name, a.source_format,
       a.width, a.height, a.frame_rate, a.in_point, a.out_point,
       d.id, d.access_hash, d.file_reference, d.date, d.mime_type, d.size, d.dc_id,
       d.attributes::text, d.thumbs::text
FROM %s
%s
ORDER BY a.sort_order, a.id`, prefix, craftedExpression, animationJSONExpression, from,
		func() string {
			if options.samplePerKind > 0 {
				return ""
			}
			return "WHERE a.collectible_revision_id=$1"
		}()), args...)
	if err != nil {
		return nil, fmt.Errorf("list collectible %s attributes: %w", kind, err)
	}
	defer rows.Close()
	out := make([]domain.StarGiftCollectibleAttribute, 0)
	for rows.Next() {
		attribute := domain.StarGiftCollectibleAttribute{Kind: kind, Document: &domain.Document{}, Animation: &domain.StarGiftAnimation{}}
		var attrsJSON, thumbsJSON, sourceFormat string
		if err := rows.Scan(&attribute.ID, &attribute.CollectibleRevisionID, &attribute.Name, &attribute.RarityKind,
			&attribute.RarityPermille, &attribute.Crafted, &attribute.OfficialDocumentID, &attribute.SortOrder,
			&attribute.Animation.JSON, &attribute.Animation.SHA256, &attribute.Animation.SourceName, &sourceFormat,
			&attribute.Animation.Width, &attribute.Animation.Height, &attribute.Animation.FrameRate, &attribute.Animation.InPoint, &attribute.Animation.OutPoint,
			&attribute.Document.ID, &attribute.Document.AccessHash, &attribute.Document.FileReference, &attribute.Document.Date,
			&attribute.Document.MimeType, &attribute.Document.Size, &attribute.Document.DCID, &attrsJSON, &thumbsJSON); err != nil {
			return nil, err
		}
		attribute.Animation.SourceFormat = domain.StarGiftAnimationFormat(sourceFormat)
		if attribute.Document.Attributes, err = decodeDocumentAttributes(attrsJSON); err != nil {
			return nil, err
		}
		if attribute.Document.Thumbs, err = decodePhotoSizes(thumbsJSON); err != nil {
			return nil, err
		}
		out = append(out, attribute)
	}
	return out, rows.Err()
}

func listCollectibleBackdrops(ctx context.Context, db sqlcgen.DBTX, revisionID int64, options collectibleRevisionReadOptions) ([]domain.StarGiftCollectibleAttribute, error) {
	prefix := ""
	from := "star_gift_collectible_backdrops a"
	where := "WHERE a.collectible_revision_id=$1"
	args := []any{revisionID}
	if options.samplePerKind > 0 {
		prefix = `WITH picked AS MATERIALIZED (
    SELECT id FROM star_gift_collectible_backdrops
    WHERE collectible_revision_id=$1 AND rarity_kind='permille' AND rarity_permille > 0
    ORDER BY random()
    LIMIT $2
)`
		from = "picked p JOIN star_gift_collectible_backdrops a ON a.id=p.id"
		where = ""
		args = append(args, options.samplePerKind)
	}
	rows, err := db.Query(ctx, fmt.Sprintf(`
%s
SELECT a.id, a.collectible_revision_id, a.name, a.backdrop_id, a.center_color, a.edge_color, a.pattern_color,
       a.text_color, a.rarity_kind, COALESCE(a.rarity_permille,0), a.sort_order
FROM %s
%s
ORDER BY a.sort_order, a.id`, prefix, from, where), args...)
	if err != nil {
		return nil, fmt.Errorf("list collectible backdrops: %w", err)
	}
	defer rows.Close()
	out := make([]domain.StarGiftCollectibleAttribute, 0)
	for rows.Next() {
		attribute := domain.StarGiftCollectibleAttribute{Kind: domain.StarGiftCollectibleBackdrop}
		if err := rows.Scan(&attribute.ID, &attribute.CollectibleRevisionID, &attribute.Name, &attribute.BackdropID,
			&attribute.CenterColor, &attribute.EdgeColor, &attribute.PatternColor, &attribute.TextColor,
			&attribute.RarityKind, &attribute.RarityPermille, &attribute.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, attribute)
	}
	return out, rows.Err()
}

func (s *StarGiftStore) CollectibleAnimationJSON(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error) {
	table := "star_gift_collectible_models"
	if kind == domain.StarGiftCollectiblePattern {
		table = "star_gift_collectible_patterns"
	} else if kind != domain.StarGiftCollectibleModel {
		return nil, false, nil
	}
	var raw []byte
	err := s.db.QueryRow(ctx, fmt.Sprintf(`
SELECT a.animation_json::text FROM %s a
JOIN star_gift_catalog c ON c.collectible_revision_id=a.collectible_revision_id
WHERE c.gift_id=$1 AND a.id=$2`, table), giftID, attributeID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get collectible animation: %w", err)
	}
	return raw, true, nil
}

func (s *StarGiftStore) UniqueBySlug(ctx context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	return s.uniqueByPredicate(ctx, "u.slug=$1", strings.ToLower(strings.TrimSpace(slug)))
}

func (s *StarGiftStore) UniqueByID(ctx context.Context, uniqueGiftID int64) (domain.UniqueStarGift, bool, error) {
	return s.uniqueByPredicate(ctx, "u.id=$1", uniqueGiftID)
}

func (s *StarGiftStore) UniqueByIDs(ctx context.Context, uniqueGiftIDs []int64) (map[int64]domain.UniqueStarGift, error) {
	out := make(map[int64]domain.UniqueStarGift, len(uniqueGiftIDs))
	if len(uniqueGiftIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, uniqueStarGiftQuery("u.id=ANY($1::bigint[])"), uniqueGiftIDs)
	if err != nil {
		return nil, fmt.Errorf("list unique star gifts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		unique, err := scanUniqueStarGift(rows)
		if err != nil {
			return nil, err
		}
		out[unique.ID] = unique
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unique star gifts: %w", err)
	}
	return out, nil
}

func (s *StarGiftStore) ListUniqueByOwner(ctx context.Context, owner domain.Peer, limit int) ([]domain.UniqueStarGift, error) {
	if owner.ID <= 0 || limit <= 0 {
		return []domain.UniqueStarGift{}, nil
	}
	if limit > domain.MaxSavedStarGiftsLimit {
		limit = domain.MaxSavedStarGiftsLimit
	}
	rows, err := s.db.Query(ctx, uniqueStarGiftQuery(`
u.owner_peer_type=$1 AND u.owner_peer_id=$2
AND NOT u.burned AND u.owner_address=''
AND sg.lifecycle_status='active'`)+`
ORDER BY u.id DESC
LIMIT $3`, string(owner.Type), owner.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("list unique star gifts by owner: %w", err)
	}
	defer rows.Close()
	out := make([]domain.UniqueStarGift, 0, limit)
	for rows.Next() {
		gift, err := scanUniqueStarGift(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, gift)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unique star gifts by owner: %w", err)
	}
	return out, nil
}

func (s *StarGiftStore) uniqueByPredicate(ctx context.Context, predicate string, value any) (domain.UniqueStarGift, bool, error) {
	row := s.db.QueryRow(ctx, uniqueStarGiftQuery(predicate), value)
	unique, err := scanUniqueStarGift(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.UniqueStarGift{}, false, nil
		}
		return domain.UniqueStarGift{}, false, err
	}
	return unique, true, nil
}

// ListUniqueThemeAvailable возвращает активные коллекции пользователя
// (владение или хостинг) с флагом theme_available: кандидаты в палитры
// сообщений и темы оформления чатов.
func (s *StarGiftStore) ListUniqueThemeAvailable(ctx context.Context, userID int64, limit int) ([]domain.UniqueStarGift, error) {
	if userID <= 0 {
		return []domain.UniqueStarGift{}, nil
	}
	if limit <= 0 || limit > domain.MaxSavedStarGiftsLimit {
		limit = domain.MaxSavedStarGiftsLimit
	}
	rows, err := s.db.Query(ctx, uniqueStarGiftQuery(`
((u.owner_peer_type='user' AND u.owner_peer_id=$1)
        OR (u.host_peer_type='user' AND u.host_peer_id=$1))
        AND u.theme_available AND NOT u.burned AND u.owner_address=''
        AND sg.lifecycle_status='active'`)+`
ORDER BY u.id DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list unique star gifts theme available: %w", err)
	}
	defer rows.Close()
	out := make([]domain.UniqueStarGift, 0, limit)
	for rows.Next() {
		gift, err := scanUniqueStarGift(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, gift)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unique star gifts theme available: %w", err)
	}
	return out, nil
}

// SetUniqueGiftThemePeer фиксирует peer, для которого коллекция установлена
// темой чата (starGiftUnique.theme_peer). uniqueGiftID<=0 снимает тему: все
// коллекции, указывающие этим theme_peer на peer, очищаются.
func (s *StarGiftStore) SetUniqueGiftThemePeer(ctx context.Context, uniqueGiftID int64, peer domain.Peer) error {
	return withTx(ctx, s.db, "set unique gift theme peer", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
UPDATE unique_star_gifts
SET theme_peer_type = NULL, theme_peer_id = NULL, updated_at = now()
WHERE theme_peer_type = $1 AND theme_peer_id = $2`, string(peer.Type), peer.ID); err != nil {
			return fmt.Errorf("clear unique gift theme peer: %w", err)
		}
		if uniqueGiftID <= 0 {
			return nil
		}
		tag, err := tx.Exec(ctx, `
UPDATE unique_star_gifts
SET theme_peer_type = $2, theme_peer_id = $3, updated_at = now()
WHERE id = $1 AND NOT burned`, uniqueGiftID, string(peer.Type), peer.ID)
		if err != nil {
			return fmt.Errorf("set unique gift theme peer: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrStarGiftNotFound
		}
		return nil
	})
}

func uniqueStarGiftQuery(predicate string) string {
	return fmt.Sprintf(`
SELECT u.id, u.gift_id, u.collectible_revision_id, u.source_saved_gift_id, u.title, u.slug, u.num,
       COALESCE(u.owner_peer_type,''), COALESCE(u.owner_peer_id,0), u.keep_original_details, u.created_at,
       u.require_premium, u.resale_ton_only, u.theme_available, u.burned, u.crafted,
       u.owner_name, u.owner_address, u.gift_address,
       COALESCE(l.currency,''), COALESCE(l.amount,0), COALESCE(l.version,0),
       COALESCE(u.released_by_peer_type,''), COALESCE(u.released_by_peer_id,0),
       u.value_amount, u.value_currency, u.value_usd_amount,
       COALESCE(u.theme_peer_type,''), COALESCE(u.theme_peer_id,0),
       COALESCE(u.host_peer_type,''), COALESCE(u.host_peer_id,0),
       u.offer_min_stars, u.craft_chance_permille, u.last_sale_date,
       u.last_sale_currency, u.last_sale_amount,
       r.issued, r.supply_total, sg.from_user_id, u.original_owner_peer_type, u.original_owner_peer_id,
       sg.gift_date, sg.message, sg.name_hidden, sg.unsaved,
       m.id, m.name, m.rarity_kind, COALESCE(m.rarity_permille,0), m.crafted, md.id, md.access_hash, md.file_reference, md.date,
       md.mime_type, md.size, md.dc_id, md.attributes::text, md.thumbs::text,
       p.id, p.name, p.rarity_kind, COALESCE(p.rarity_permille,0), pd.id, pd.access_hash, pd.file_reference, pd.date,
       pd.mime_type, pd.size, pd.dc_id, pd.attributes::text, pd.thumbs::text,
       b.id, b.name, b.backdrop_id, b.center_color, b.edge_color, b.pattern_color, b.text_color,
       b.rarity_kind, COALESCE(b.rarity_permille,0)
FROM unique_star_gifts u
JOIN star_gift_collectible_revisions r ON r.id=u.collectible_revision_id
JOIN star_gift_collectible_models m ON m.id=u.model_attribute_id
JOIN documents md ON md.id=m.document_id
JOIN star_gift_collectible_patterns p ON p.id=u.pattern_attribute_id
JOIN documents pd ON pd.id=p.document_id
JOIN star_gift_collectible_backdrops b ON b.id=u.backdrop_attribute_id
JOIN peer_star_gifts sg ON sg.id=u.source_saved_gift_id
LEFT JOIN star_gift_listings l ON l.unique_gift_id=u.id
WHERE %s`, predicate)
}

func scanUniqueStarGift(row rowScanner) (domain.UniqueStarGift, error) {
	var unique domain.UniqueStarGift
	var ownerType, originalOwnerType, listingCurrency, releasedByType, themePeerType, hostPeerType, lastSaleCurrency string
	var listingAmount, lastSaleAmount int64
	unique.Model.Kind = domain.StarGiftCollectibleModel
	unique.Pattern.Kind = domain.StarGiftCollectiblePattern
	unique.Backdrop.Kind = domain.StarGiftCollectibleBackdrop
	unique.Model.Document = &domain.Document{}
	unique.Pattern.Document = &domain.Document{}
	var modelAttrs, modelThumbs, patternAttrs, patternThumbs string
	if err := row.Scan(&unique.ID, &unique.GiftID, &unique.CollectibleRevisionID, &unique.SourceSavedGiftID,
		&unique.Title, &unique.Slug, &unique.Num, &ownerType, &unique.Owner.ID, &unique.KeepOriginalDetails,
		&unique.CreatedAt, &unique.RequirePremium, &unique.ResaleTonOnly, &unique.ThemeAvailable,
		&unique.Burned, &unique.Crafted, &unique.OwnerName, &unique.OwnerAddress, &unique.GiftAddress,
		&listingCurrency, &listingAmount, &unique.ResellVersion, &releasedByType, &unique.ReleasedBy.ID,
		&unique.ValueAmount, &unique.ValueCurrency, &unique.ValueUSD,
		&themePeerType, &unique.ThemePeer.ID, &hostPeerType, &unique.Host.ID,
		&unique.OfferMinStars, &unique.CraftChancePermille, &unique.LastSaleDate,
		&lastSaleCurrency, &lastSaleAmount,
		&unique.AvailabilityIssued, &unique.AvailabilityTotal,
		&unique.OriginalFromUserID, &originalOwnerType, &unique.OriginalOwner.ID, &unique.OriginalDate,
		&unique.OriginalMessage, &unique.OriginalNameHidden, &unique.Unsaved,
		&unique.Model.ID, &unique.Model.Name, &unique.Model.RarityKind, &unique.Model.RarityPermille, &unique.Model.Crafted,
		&unique.Model.Document.ID, &unique.Model.Document.AccessHash, &unique.Model.Document.FileReference,
		&unique.Model.Document.Date, &unique.Model.Document.MimeType, &unique.Model.Document.Size,
		&unique.Model.Document.DCID, &modelAttrs, &modelThumbs,
		&unique.Pattern.ID, &unique.Pattern.Name, &unique.Pattern.RarityKind, &unique.Pattern.RarityPermille,
		&unique.Pattern.Document.ID, &unique.Pattern.Document.AccessHash, &unique.Pattern.Document.FileReference,
		&unique.Pattern.Document.Date, &unique.Pattern.Document.MimeType, &unique.Pattern.Document.Size,
		&unique.Pattern.Document.DCID, &patternAttrs, &patternThumbs,
		&unique.Backdrop.ID, &unique.Backdrop.Name, &unique.Backdrop.BackdropID, &unique.Backdrop.CenterColor,
		&unique.Backdrop.EdgeColor, &unique.Backdrop.PatternColor, &unique.Backdrop.TextColor,
		&unique.Backdrop.RarityKind, &unique.Backdrop.RarityPermille); err != nil {
		return domain.UniqueStarGift{}, fmt.Errorf("get unique star gift: %w", err)
	}
	unique.Owner.Type = domain.PeerType(ownerType)
	unique.OriginalOwner.Type = domain.PeerType(originalOwnerType)
	unique.ReleasedBy.Type = domain.PeerType(releasedByType)
	unique.ThemePeer.Type = domain.PeerType(themePeerType)
	unique.Host.Type = domain.PeerType(hostPeerType)
	if listingCurrency != "" && listingAmount > 0 {
		unique.ResellAmount = &domain.StarGiftAmount{Currency: domain.StarGiftCurrency(listingCurrency), Amount: listingAmount}
		// Эквивалентные цены рынка (звёзды/нанотон) считаются от валюты листинга.
		unique.ResellStars, unique.ResellTon = domain.StarGiftResalePrices(*unique.ResellAmount)
	}
	if lastSaleCurrency != "" && unique.LastSaleDate > 0 {
		unique.LastSaleAmount = &domain.StarGiftAmount{Currency: domain.StarGiftCurrency(lastSaleCurrency), Amount: lastSaleAmount}
	}
	unique.Model.CollectibleRevisionID = unique.CollectibleRevisionID
	unique.Pattern.CollectibleRevisionID = unique.CollectibleRevisionID
	unique.Backdrop.CollectibleRevisionID = unique.CollectibleRevisionID
	var err error
	if unique.Model.Document.Attributes, err = decodeDocumentAttributes(modelAttrs); err != nil {
		return domain.UniqueStarGift{}, err
	}
	if unique.Model.Document.Thumbs, err = decodePhotoSizes(modelThumbs); err != nil {
		return domain.UniqueStarGift{}, err
	}
	if unique.Pattern.Document.Attributes, err = decodeDocumentAttributes(patternAttrs); err != nil {
		return domain.UniqueStarGift{}, err
	}
	if unique.Pattern.Document.Thumbs, err = decodePhotoSizes(patternThumbs); err != nil {
		return domain.UniqueStarGift{}, err
	}
	return unique, nil
}

func (s *StarGiftStore) ListCollections(ctx context.Context, owner domain.Peer) ([]domain.StarGiftCollection, error) {
	rows, err := s.db.Query(ctx, `
SELECT c.collection_id, c.title, c.hash, c.sort_order, c.created_at, c.updated_at, i.saved_gift_id
FROM star_gift_collections c
LEFT JOIN star_gift_collection_items i ON i.collection_id=c.collection_id
WHERE c.owner_peer_type=$1 AND c.owner_peer_id=$2
ORDER BY c.sort_order, c.collection_id, i.sort_order, i.saved_gift_id`, string(owner.Type), owner.ID)
	if err != nil {
		return nil, fmt.Errorf("list star gift collections: %w", err)
	}
	defer rows.Close()
	out := make([]domain.StarGiftCollection, 0)
	index := make(map[int]int)
	for rows.Next() {
		var collection domain.StarGiftCollection
		var giftID pgtype.Int8
		if err := rows.Scan(&collection.CollectionID, &collection.Title, &collection.Hash, &collection.SortOrder,
			&collection.CreatedAt, &collection.UpdatedAt, &giftID); err != nil {
			return nil, err
		}
		position, ok := index[collection.CollectionID]
		if !ok {
			collection.Owner = owner
			position = len(out)
			index[collection.CollectionID] = position
			out = append(out, collection)
		}
		if giftID.Valid {
			out[position].GiftIDs = append(out[position].GiftIDs, giftID.Int64)
		}
	}
	return out, rows.Err()
}

func (s *StarGiftStore) CreateCollection(ctx context.Context, owner domain.Peer, title string, savedGiftIDs []int64) (domain.StarGiftCollection, error) {
	title = strings.TrimSpace(title)
	if !validPostgresStarGiftOwner(owner) || title == "" || len([]rune(title)) > domain.MaxStarGiftCollectionTitleRunes {
		return domain.StarGiftCollection{}, domain.ErrStarGiftCollectibleInvalid
	}
	var result domain.StarGiftCollection
	err := withTx(ctx, s.db, "create star gift collection", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_collections WHERE owner_peer_type=$1 AND owner_peer_id=$2`, string(owner.Type), owner.ID).Scan(&count); err != nil {
			return err
		}
		if count >= domain.MaxStarGiftCollectionsPerPeer {
			return domain.ErrStarGiftCollectionsFull
		}
		ids, err := validatePostgresCollectionGiftIDs(ctx, tx, owner, savedGiftIDs)
		if err != nil {
			return err
		}
		result = domain.StarGiftCollection{Owner: owner, Title: title, GiftIDs: ids, SortOrder: count}
		result.Hash = domain.StarGiftCollectionHash(title, ids)
		if err := tx.QueryRow(ctx, `
INSERT INTO star_gift_collections(owner_peer_type, owner_peer_id, title, sort_order, hash)
VALUES ($1,$2,$3,$4,$5) RETURNING collection_id, created_at, updated_at`, string(owner.Type), owner.ID,
			title, count, result.Hash).Scan(&result.CollectionID, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return err
		}
		return replaceCollectionItems(ctx, tx, result.CollectionID, ids)
	})
	return result, err
}

func (s *StarGiftStore) UpdateCollection(ctx context.Context, owner domain.Peer, collectionID int, patch domain.StarGiftCollectionPatch) (domain.StarGiftCollection, error) {
	var result domain.StarGiftCollection
	err := withTx(ctx, s.db, "update star gift collection", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
SELECT title, hash, sort_order, created_at, updated_at FROM star_gift_collections
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND collection_id=$3 FOR UPDATE`, string(owner.Type), owner.ID, collectionID).Scan(
			&result.Title, &result.Hash, &result.SortOrder, &result.CreatedAt, &result.UpdatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftCollectionNotFound
			}
			return err
		}
		result.Owner = owner
		result.CollectionID = collectionID
		rows, err := tx.Query(ctx, `SELECT saved_gift_id FROM star_gift_collection_items WHERE collection_id=$1 ORDER BY sort_order, saved_gift_id`, collectionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			result.GiftIDs = append(result.GiftIDs, id)
		}
		rows.Close()
		if patch.Title != nil {
			title := strings.TrimSpace(*patch.Title)
			if title == "" || len([]rune(title)) > domain.MaxStarGiftCollectionTitleRunes {
				return domain.ErrStarGiftCollectibleInvalid
			}
			result.Title = title
		}
		deleted := make(map[int64]struct{}, len(patch.DeleteIDs))
		for _, id := range patch.DeleteIDs {
			deleted[id] = struct{}{}
		}
		next := make([]int64, 0, len(result.GiftIDs)+len(patch.AddIDs))
		for _, id := range result.GiftIDs {
			if _, ok := deleted[id]; !ok {
				next = append(next, id)
			}
		}
		add, err := validatePostgresCollectionGiftIDs(ctx, tx, owner, patch.AddIDs)
		if err != nil {
			return err
		}
		next = appendUniquePostgresIDs(next, add...)
		if patch.Order != nil {
			order, err := validatePostgresCollectionGiftIDs(ctx, tx, owner, patch.Order)
			if err != nil || !samePostgresIDSet(order, next) {
				return domain.ErrStarGiftCollectibleInvalid
			}
			next = order
		}
		if len(next) > domain.MaxStarGiftCollectionItems {
			return domain.ErrStarGiftCollectibleInvalid
		}
		result.GiftIDs = next
		result.Hash = domain.StarGiftCollectionHash(result.Title, result.GiftIDs)
		if err := tx.QueryRow(ctx, `
UPDATE star_gift_collections SET title=$4, hash=$5, updated_at=now()
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND collection_id=$3 RETURNING updated_at`,
			string(owner.Type), owner.ID, collectionID, result.Title, result.Hash).Scan(&result.UpdatedAt); err != nil {
			return err
		}
		return replaceCollectionItems(ctx, tx, collectionID, result.GiftIDs)
	})
	return result, err
}

func (s *StarGiftStore) DeleteCollection(ctx context.Context, owner domain.Peer, collectionID int) (bool, error) {
	var changed bool
	err := withTx(ctx, s.db, "delete star gift collection", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM star_gift_collections WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND collection_id=$3`, string(owner.Type), owner.ID, collectionID)
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() > 0
		if changed {
			_, err = tx.Exec(ctx, `
WITH ordered AS (
    SELECT collection_id, row_number() OVER (ORDER BY sort_order, collection_id) - 1 AS next_order
    FROM star_gift_collections WHERE owner_peer_type=$1 AND owner_peer_id=$2
)
UPDATE star_gift_collections c SET sort_order=o.next_order, updated_at=now()
FROM ordered o WHERE c.collection_id=o.collection_id`, string(owner.Type), owner.ID)
		}
		return err
	})
	return changed, err
}

func (s *StarGiftStore) ReorderCollections(ctx context.Context, owner domain.Peer, collectionIDs []int) error {
	return withTx(ctx, s.db, "reorder star gift collections", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT collection_id FROM star_gift_collections WHERE owner_peer_type=$1 AND owner_peer_id=$2 FOR UPDATE`, string(owner.Type), owner.ID)
		if err != nil {
			return err
		}
		existing := make([]int, 0)
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			existing = append(existing, id)
		}
		rows.Close()
		if !samePostgresIntSet(existing, collectionIDs) {
			return domain.ErrStarGiftCollectibleInvalid
		}
		for order, id := range collectionIDs {
			if _, err := tx.Exec(ctx, `UPDATE star_gift_collections SET sort_order=$4, updated_at=now() WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND collection_id=$3`, string(owner.Type), owner.ID, id, order); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *StarGiftStore) SetPinned(ctx context.Context, owner domain.Peer, savedGiftIDs []int64) error {
	if len(savedGiftIDs) > domain.MaxPinnedStarGifts {
		return domain.ErrStarGiftCollectibleInvalid
	}
	return withTx(ctx, s.db, "set pinned star gifts", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, starGiftCollectionLockKey(owner)); err != nil {
			return err
		}
		ids, err := validatePostgresCollectionGiftIDs(ctx, tx, owner, savedGiftIDs)
		if err != nil {
			return err
		}
		if len(ids) != len(savedGiftIDs) {
			return domain.ErrStarGiftCollectibleInvalid
		}
		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET pinned_order=0 WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND pinned_order<>0`, string(owner.Type), owner.ID); err != nil {
			return err
		}
		for order, id := range ids {
			if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET pinned_order=$2,unsaved=false WHERE id=$1`, id, order+1); err != nil {
				return err
			}
		}
		return nil
	})
}

func validatePostgresCollectionGiftIDs(ctx context.Context, db sqlcgen.DBTX, owner domain.Peer, ids []int64) ([]int64, error) {
	ids = dedupePostgresIDs(ids)
	if len(ids) > domain.MaxStarGiftCollectionItems {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	if len(ids) == 0 {
		return []int64{}, nil
	}
	rows, err := db.Query(ctx, `
SELECT id FROM peer_star_gifts
WHERE owner_peer_type=$1 AND owner_peer_id=$2 AND lifecycle_status='active' AND id=ANY($3::bigint[])
FOR UPDATE`, string(owner.Type), owner.ID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[int64]struct{}, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found[id] = struct{}{}
	}
	if len(found) != len(ids) {
		return nil, domain.ErrStarGiftNotFound
	}
	return ids, rows.Err()
}

// removeSavedGiftFromCollections runs under the owner advisory lock. It removes
// terminal gifts and updates every affected collection hash in bounded batches,
// so getStarGiftCollections cannot return NotModified for changed membership.
func removeSavedGiftFromCollections(ctx context.Context, tx pgx.Tx, owner domain.Peer, savedGiftID int64) error {
	rows, err := tx.Query(ctx, `
SELECT c.collection_id, c.title
FROM star_gift_collections c
JOIN star_gift_collection_items i ON i.collection_id=c.collection_id
WHERE c.owner_peer_type=$1 AND c.owner_peer_id=$2 AND i.saved_gift_id=$3
ORDER BY c.collection_id
FOR UPDATE OF c`, string(owner.Type), owner.ID, savedGiftID)
	if err != nil {
		return fmt.Errorf("lock converted gift collections: %w", err)
	}
	titles := make(map[int]string)
	ids := make([]int, 0)
	for rows.Next() {
		var id int
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
		titles[id] = title
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM star_gift_collection_items WHERE saved_gift_id=$1`, savedGiftID); err != nil {
		return fmt.Errorf("remove converted gift collection memberships: %w", err)
	}

	memberships := make(map[int][]int64, len(ids))
	itemRows, err := tx.Query(ctx, `
SELECT collection_id, saved_gift_id
FROM star_gift_collection_items
WHERE collection_id=ANY($1::integer[])
ORDER BY collection_id, sort_order, saved_gift_id`, ids)
	if err != nil {
		return fmt.Errorf("list remaining collection memberships: %w", err)
	}
	for itemRows.Next() {
		var collectionID int
		var giftID int64
		if err := itemRows.Scan(&collectionID, &giftID); err != nil {
			itemRows.Close()
			return err
		}
		memberships[collectionID] = append(memberships[collectionID], giftID)
	}
	if err := itemRows.Err(); err != nil {
		itemRows.Close()
		return err
	}
	itemRows.Close()

	hashes := make([]int64, len(ids))
	for i, collectionID := range ids {
		hashes[i] = domain.StarGiftCollectionHash(titles[collectionID], memberships[collectionID])
	}
	if _, err := tx.Exec(ctx, `
UPDATE star_gift_collections c SET hash=x.hash, updated_at=now()
FROM unnest($1::integer[], $2::bigint[]) AS x(collection_id, hash)
WHERE c.collection_id=x.collection_id`, ids, hashes); err != nil {
		return fmt.Errorf("refresh converted gift collection hashes: %w", err)
	}
	return nil
}

func replaceCollectionItems(ctx context.Context, tx pgx.Tx, collectionID int, ids []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM star_gift_collection_items WHERE collection_id=$1`, collectionID); err != nil {
		return err
	}
	for order, id := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_collection_items(collection_id, saved_gift_id, sort_order) VALUES ($1,$2,$3)`, collectionID, id, order); err != nil {
			return err
		}
	}
	return nil
}

func validPostgresStarGiftOwner(owner domain.Peer) bool {
	return owner.ID > 0 && (owner.Type == domain.PeerTypeUser || owner.Type == domain.PeerTypeChannel)
}

func starGiftCollectionLockKey(owner domain.Peer) string {
	return fmt.Sprintf("star_gift_collection:%s:%d", owner.Type, owner.ID)
}

func dedupePostgresIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func appendUniquePostgresIDs(dst []int64, values ...int64) []int64 {
	seen := make(map[int64]struct{}, len(dst)+len(values))
	for _, id := range dst {
		seen[id] = struct{}{}
	}
	for _, id := range values {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			dst = append(dst, id)
		}
	}
	return dst
}

func samePostgresIDSet(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]int64(nil), a...)
	b = append([]int64(nil), b...)
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func samePostgresIntSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int]struct{}, len(a))
	for _, id := range a {
		seen[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := seen[id]; !ok {
			return false
		}
		delete(seen, id)
	}
	return len(seen) == 0
}

// SyncActiveCollectibleRevision (раунд 37) правит СУЩЕСТВУЮЩУЮ опубликованную
// ревизию подарка «на месте». Форма присылает ПОЛНЫЙ пул: строки атрибутов с
// ID обновляются (id и, значит, ссылки выпущенных подарков сохраняются), без
// ID — добавляются, отсутствующие в форме — снимаются. Снятие строки, на
// которую ссылается выпущенный подарок, отклоняется понятной ошибкой (FK
// RESTRICT тоже защищает, но явная проверка даёт осмысленный текст).
//
// Основные поля ревизии (upgrade_stars/supply_total/slug_prefix) обновляются
// в той же строке ревизии; новая ревизия не создаётся и issued не трогается.
// Валидация та же, что и у публикации: полный пул, минимум два selectable
// атрибута каждого анимированного вида, различные документы.
func (s *StarGiftStore) SyncActiveCollectibleRevision(ctx context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error) {
	write.SlugPrefix = strings.ToLower(strings.TrimSpace(write.SlugPrefix))
	write.Actor = strings.TrimSpace(write.Actor)
	write.CommandID = strings.TrimSpace(write.CommandID)
	// Нормализация строк правки: UPDATE-строки, ссылающиеся на сохранённый
	// документ (Document есть, Blob нет — как их отдаёт чтение ревизии),
	// валидируются как переиспользование; новые строки с файлами материализует
	// вызывающий (app-слой).
	for i := range write.Models {
		if write.Models[i].ID > 0 && write.Models[i].Document != nil && write.Models[i].Blob == nil {
			write.Models[i].ReuseDocument = true
		}
	}
	for i := range write.Patterns {
		if write.Patterns[i].ID > 0 && write.Patterns[i].Document != nil && write.Patterns[i].Blob == nil {
			write.Patterns[i].ReuseDocument = true
		}
	}
	if err := domain.ValidateStarGiftCollectibleWrite(write); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	var result domain.StarGiftCollectibleRevision
	err := withTx(ctx, s.db, "sync collectible star gift revision", func(tx pgx.Tx) error {
		var activeRevisionID pgtype.Int8
		if err := tx.QueryRow(ctx, `SELECT collectible_revision_id FROM star_gift_catalog WHERE gift_id=$1 FOR UPDATE`,
			write.GiftID).Scan(&activeRevisionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftNotFound
			}
			return fmt.Errorf("lock collectible catalog gift: %w", err)
		}
		if !activeRevisionID.Valid {
			// Активной ревизии нет — синхронизировать нечего; вызывающий
			// должен публиковать новую (PublishCollectibleRevision).
			return domain.ErrStarGiftCollectibleInvalid
		}
		revisionID := activeRevisionID.Int64
		var revisionGiftID int64
		if err := tx.QueryRow(ctx, `SELECT gift_id FROM star_gift_collectible_revisions WHERE id=$1 FOR UPDATE`,
			revisionID).Scan(&revisionGiftID); err != nil {
			return fmt.Errorf("lock collectible revision: %w", err)
		}
		if revisionGiftID != write.GiftID {
			return domain.ErrStarGiftCollectibleInvalid
		}
		if _, err := tx.Exec(ctx, `
UPDATE star_gift_collectible_revisions
SET upgrade_stars=$2, supply_total=$3, slug_prefix=$4, created_by=$5, command_id=$6
WHERE id=$1`, revisionID, write.UpgradeStars, write.SupplyTotal, write.SlugPrefix, write.Actor, write.CommandID); err != nil {
			return fmt.Errorf("update collectible revision: %w", err)
		}
		media := NewMediaStore(tx)
		if err := syncAnimatedCollectibleAttributes(ctx, tx, media, revisionID, "star_gift_collectible_models",
			domain.StarGiftCollectibleModel, write.Models); err != nil {
			return err
		}
		if err := syncAnimatedCollectibleAttributes(ctx, tx, media, revisionID, "star_gift_collectible_patterns",
			domain.StarGiftCollectiblePattern, write.Patterns); err != nil {
			return err
		}
		if err := syncCollectibleBackdrops(ctx, tx, revisionID, write.Backdrops); err != nil {
			return err
		}
		var err error
		result, err = collectibleRevisionByID(ctx, tx, revisionID)
		return err
	})
	return result, err
}

// syncAnimatedCollectibleAttributes синхронизирует один анимированный пул
// (модели или узоры) опубликованной ревизии: UPDATE по ID, INSERT без ID,
// DELETE отсутствующих (с проверкой ссылок выпущенных подарков).
func syncAnimatedCollectibleAttributes(ctx context.Context, tx pgx.Tx, media *MediaStore, revisionID int64,
	table string, kind domain.StarGiftCollectibleAttributeKind, attributes []domain.StarGiftCollectibleAttribute) error {
	if kind == domain.StarGiftCollectibleModel {
		// Редкость моделей: permille с ограничением 1..1000 либо именованная
		// crafted-редкость — констрейнт star_gift_collectible_model_rarity_check.
	} else if kind != domain.StarGiftCollectiblePattern {
		return domain.ErrStarGiftCollectibleInvalid
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT id FROM %s WHERE collectible_revision_id=$1`, table), revisionID)
	if err != nil {
		return fmt.Errorf("list collectible %s ids: %w", kind, err)
	}
	existing := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan collectible %s id: %w", kind, err)
		}
		existing[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate collectible %s ids: %w", kind, err)
	}
	rows.Close()

	kept := make(map[int64]struct{}, len(attributes))
	for _, attribute := range attributes {
		if attribute.ID > 0 {
			if _, ok := existing[attribute.ID]; !ok {
				return fmt.Errorf("%w: %s attribute %d does not belong to revision %d",
					domain.ErrStarGiftCollectibleInvalid, kind, attribute.ID, revisionID)
			}
			kept[attribute.ID] = struct{}{}
			// crafted существует только у моделей (как в INSERT-пути).
			updateQuery := fmt.Sprintf(`
UPDATE %s SET
    name=$3, document_id=$4, animation_json=$5::jsonb, animation_sha256=$6,
    source_name=$7, source_format=$8, width=$9, height=$10, frame_rate=$11, in_point=$12, out_point=$13,
    rarity_kind=$14, rarity_permille=$15, crafted=$16, sort_order=$17
WHERE id=$1 AND collectible_revision_id=$2`, table)
			updateArgs := []any{attribute.ID, revisionID, strings.TrimSpace(attribute.Name), attribute.Document.ID,
				string(attribute.Animation.JSON), attribute.Animation.SHA256, attribute.Animation.SourceName,
				string(attribute.Animation.SourceFormat), attribute.Animation.Width, attribute.Animation.Height,
				attribute.Animation.FrameRate, attribute.Animation.InPoint, attribute.Animation.OutPoint,
				string(attribute.RarityKind), nullablePermille(attribute), attribute.Crafted, attribute.SortOrder}
			if kind != domain.StarGiftCollectibleModel {
				updateQuery = fmt.Sprintf(`
UPDATE %s SET
    name=$3, document_id=$4, animation_json=$5::jsonb, animation_sha256=$6,
    source_name=$7, source_format=$8, width=$9, height=$10, frame_rate=$11, in_point=$12, out_point=$13,
    rarity_kind=$14, rarity_permille=$15, sort_order=$16
WHERE id=$1 AND collectible_revision_id=$2`, table)
				updateArgs = []any{attribute.ID, revisionID, strings.TrimSpace(attribute.Name), attribute.Document.ID,
					string(attribute.Animation.JSON), attribute.Animation.SHA256, attribute.Animation.SourceName,
					string(attribute.Animation.SourceFormat), attribute.Animation.Width, attribute.Animation.Height,
					attribute.Animation.FrameRate, attribute.Animation.InPoint, attribute.Animation.OutPoint,
					string(attribute.RarityKind), nullablePermille(attribute), attribute.SortOrder}
			}
			if _, err := tx.Exec(ctx, updateQuery, updateArgs...); err != nil {
				return fmt.Errorf("update collectible %s attribute: %w", kind, err)
			}
			continue
		}
		// Новая строка: документ и blob сохраняются (если не копия).
		if !attribute.ReuseDocument {
			if err := media.PutDocument(ctx, *attribute.Document); err != nil {
				return fmt.Errorf("put collectible %s document: %w", kind, err)
			}
			if err := media.PutFileBlob(ctx, *attribute.Blob); err != nil {
				return fmt.Errorf("put collectible %s blob: %w", kind, err)
			}
		}
		animation := attribute.Animation
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
INSERT INTO %s
    (collectible_revision_id, name, document_id, animation_json, animation_sha256,
     source_name, source_format, width, height, frame_rate, in_point, out_point,
     rarity_kind, rarity_permille, crafted, official_document_id, sort_order)
VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, table),
			revisionID, strings.TrimSpace(attribute.Name), attribute.Document.ID,
			string(animation.JSON), animation.SHA256, animation.SourceName, string(animation.SourceFormat),
			animation.Width, animation.Height, animation.FrameRate, animation.InPoint, animation.OutPoint,
			string(attribute.RarityKind), nullablePermille(attribute), attribute.Crafted,
			nullablePositiveInt64(attribute.OfficialDocumentID), attribute.SortOrder); err != nil {
			return fmt.Errorf("insert collectible %s attribute: %w", kind, err)
		}
	}
	// Снятие отсутствующих в форме строк: выпущенные подарки держат составной
	// FK (attribute_id, collectible_revision_id) — отказ с понятной ошибкой.
	removed := make([]int64, 0, len(existing))
	for id := range existing {
		if _, ok := kept[id]; !ok {
			removed = append(removed, id)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	idColumn := "model_attribute_id"
	if kind == domain.StarGiftCollectiblePattern {
		idColumn = "pattern_attribute_id"
	}
	var referenced int64
	if err := tx.QueryRow(ctx, fmt.Sprintf(`
SELECT COUNT(*) FROM unique_star_gifts
WHERE collectible_revision_id=$1 AND %s = ANY($2)`, idColumn), revisionID, removed).Scan(&referenced); err != nil {
		return fmt.Errorf("check collectible %s attribute references: %w", kind, err)
	}
	if referenced > 0 {
		return fmt.Errorf("%w: %d issued gift(s) reference removed %s attributes",
			domain.ErrStarGiftCollectibleAttributeInUse, referenced, kind)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE collectible_revision_id=$1 AND id = ANY($2)`, table),
		revisionID, removed); err != nil {
		return fmt.Errorf("delete collectible %s attributes: %w", kind, err)
	}
	return nil
}

// syncCollectibleBackdrops синхронизирует пул фонов опубликованной ревизии:
// UPDATE по ID, INSERT без ID, DELETE отсутствующих (с проверкой ссылок).
func syncCollectibleBackdrops(ctx context.Context, tx pgx.Tx, revisionID int64, attributes []domain.StarGiftCollectibleAttribute) error {
	rows, err := tx.Query(ctx, `SELECT id FROM star_gift_collectible_backdrops WHERE collectible_revision_id=$1`, revisionID)
	if err != nil {
		return fmt.Errorf("list collectible backdrop ids: %w", err)
	}
	existing := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan collectible backdrop id: %w", err)
		}
		existing[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate collectible backdrop ids: %w", err)
	}
	rows.Close()

	kept := make(map[int64]struct{}, len(attributes))
	for _, attribute := range attributes {
		if attribute.ID > 0 {
			if _, ok := existing[attribute.ID]; !ok {
				return fmt.Errorf("%w: backdrop attribute %d does not belong to revision %d",
					domain.ErrStarGiftCollectibleInvalid, attribute.ID, revisionID)
			}
			kept[attribute.ID] = struct{}{}
			if _, err := tx.Exec(ctx, `
UPDATE star_gift_collectible_backdrops SET
    name=$3, backdrop_id=$4, center_color=$5, edge_color=$6, pattern_color=$7, text_color=$8,
    rarity_kind=$9, rarity_permille=$10, sort_order=$11
WHERE id=$1 AND collectible_revision_id=$2`,
				attribute.ID, revisionID, strings.TrimSpace(attribute.Name), attribute.BackdropID,
				attribute.CenterColor, attribute.EdgeColor, attribute.PatternColor, attribute.TextColor,
				string(attribute.RarityKind), nullablePermille(attribute), attribute.SortOrder); err != nil {
				return fmt.Errorf("update collectible backdrop: %w", err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO star_gift_collectible_backdrops
    (collectible_revision_id, name, backdrop_id, center_color, edge_color, pattern_color,
     text_color, rarity_kind, rarity_permille, sort_order)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, revisionID, strings.TrimSpace(attribute.Name), attribute.BackdropID,
			attribute.CenterColor, attribute.EdgeColor, attribute.PatternColor, attribute.TextColor,
			string(attribute.RarityKind), nullablePermille(attribute), attribute.SortOrder); err != nil {
			return fmt.Errorf("insert collectible backdrop: %w", err)
		}
	}
	removed := make([]int64, 0, len(existing))
	for id := range existing {
		if _, ok := kept[id]; !ok {
			removed = append(removed, id)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	var referenced int64
	if err := tx.QueryRow(ctx, `
SELECT COUNT(*) FROM unique_star_gifts
WHERE collectible_revision_id=$1 AND backdrop_attribute_id = ANY($2)`, revisionID, removed).Scan(&referenced); err != nil {
		return fmt.Errorf("check collectible backdrop references: %w", err)
	}
	if referenced > 0 {
		return fmt.Errorf("%w: %d issued gift(s) reference removed backdrop attributes",
			domain.ErrStarGiftCollectibleAttributeInUse, referenced)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM star_gift_collectible_backdrops WHERE collectible_revision_id=$1 AND id = ANY($2)`,
		revisionID, removed); err != nil {
		return fmt.Errorf("delete collectible backdrops: %w", err)
	}
	return nil
}
