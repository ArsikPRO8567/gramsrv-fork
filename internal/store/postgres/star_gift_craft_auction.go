package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	starGiftAuctionRoundDuration = 3600
	maxStarGiftAuctionAcquired   = 1000
)

// parseAuctionRoundDurations разбирает star_gift_auctions.round_durations
// (раунд 20). Основная форма — объект {"round_1":120,"round_2":300,"round_4":1000};
// также принимается форма файла-образца — массив из одного такого объекта.
// Для пропущенных раундов действует общий round_duration; NULL, пустой или
// некорректный JSON дают nil (общий режим). Ключи не вида round_N и значения
// <= 0 игнорируются.
func parseAuctionRoundDurations(raw []byte) map[int]int {
	if len(raw) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	collect := func(decoded map[string]int) map[int]int {
		out := make(map[int]int, len(decoded))
		for key, value := range decoded {
			if value <= 0 || !strings.HasPrefix(key, "round_") {
				continue
			}
			round, err := strconv.Atoi(strings.TrimPrefix(key, "round_"))
			if err != nil || round < 1 {
				continue
			}
			out[round] = value
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if trimmed[0] == '[' {
		var decoded []map[string]int
		if err := json.Unmarshal(trimmed, &decoded); err != nil || decoded == nil {
			return nil
		}
		merged := make(map[string]int)
		for _, entry := range decoded {
			for key, value := range entry {
				merged[key] = value
			}
		}
		return collect(merged)
	}
	var decoded map[string]int
	if err := json.Unmarshal(trimmed, &decoded); err != nil || decoded == nil {
		return nil
	}
	return collect(decoded)
}

// auctionRoundDurationFor — длительность конкретного раунда: индивидуальная из
// round_durations, иначе общая.
func auctionRoundDurationFor(durations map[int]int, round, common int) int {
	if round >= 1 && durations != nil {
		if d, ok := durations[round]; ok && d > 0 {
			return d
		}
	}
	return common
}

// auctionRoundBoundary восстанавливает границу конца раунда round: start_date
// плюс сумма длительностей раундов 1..round. Инвариант совпадает с пошаговым
// next_round_at += duration(settle), поэтому пересчёт безопасен и одновременно
// применяет индивидуальные длительности к уже идущему аукциону (в т.ч. если
// jsonb заполнили после создания строки). extendSeconds — накопленное продление
// текущего раунда ставками (star_gift_auctions.round_extend_seconds).
func auctionRoundBoundary(startDate int, durations map[int]int, round, common, extendSeconds int) int {
	boundary := startDate
	for r := 1; r <= round; r++ {
		boundary += auctionRoundDurationFor(durations, r, common)
	}
	return boundary + extendSeconds
}

// derivedAuctionEnd — минимально возможное время окончания аукциона (раунд 21,
// исправление): граница конца текущего раунда (nextRoundAt, уже включающая
// продления текущего раунда) плюс суммарная длительность оставшихся раундов
// currentRound+1..totalRounds. Продление любого раунда отодвигает конец
// аукциона, поэтому аукцион не может завершиться по старому end_date раньше,
// чем пройдут все раунды. Для последнего раунда (currentRound >= totalRounds)
// возвращает nextRoundAt.
func derivedAuctionEnd(durations map[int]int, currentRound, totalRounds, common, nextRoundAt int) int {
	end := nextRoundAt
	for r := currentRound + 1; r <= totalRounds; r++ {
		end += auctionRoundDurationFor(durations, r, common)
	}
	return end
}

// parseAuctionRoundExtends разбирает star_gift_auctions.round_extends
// (раунд 21). Основная форма — объект
// {"round_2":{"window":30,"top":3,"threshold":120}}; также принимается форма
// файла-образца — массив из одного такого объекта. Допустимые псевдонимы
// ключей: window/extend_window, top/extend_top, threshold/extend_threshold.
// Для раунда продление действует, когда window > 0 и top > 0; пропущенный или
// неположительный threshold означает window. NULL, пустой или некорректный
// JSON дают nil (продления выключены). Ключи не вида round_N игнорируются.
func parseAuctionRoundExtends(raw []byte) map[int]domain.StarGiftAuctionRoundExtend {
	if len(raw) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	type rawExtend struct {
		Window    *int `json:"window"`
		Top       *int `json:"top"`
		Threshold *int `json:"threshold"`
		// Официальные псевдонимы полей starGiftAuctionRoundExtendable.
		ExtendWindow    *int `json:"extend_window"`
		ExtendTop       *int `json:"extend_top"`
		ExtendThreshold *int `json:"extend_threshold"`
	}
	collect := func(decoded map[string]rawExtend) map[int]domain.StarGiftAuctionRoundExtend {
		out := make(map[int]domain.StarGiftAuctionRoundExtend, len(decoded))
		for key, value := range decoded {
			if !strings.HasPrefix(key, "round_") {
				continue
			}
			round, err := strconv.Atoi(strings.TrimPrefix(key, "round_"))
			if err != nil || round < 1 {
				continue
			}
			window := value.Window
			if window == nil {
				window = value.ExtendWindow
			}
			top := value.Top
			if top == nil {
				top = value.ExtendTop
			}
			threshold := value.Threshold
			if threshold == nil {
				threshold = value.ExtendThreshold
			}
			if window == nil || top == nil || *window <= 0 || *top <= 0 {
				continue
			}
			extend := domain.StarGiftAuctionRoundExtend{WindowSeconds: *window, TopBidders: *top}
			if threshold != nil && *threshold > 0 {
				extend.ThresholdSeconds = *threshold
			} else {
				extend.ThresholdSeconds = *window
			}
			out[round] = extend
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if trimmed[0] == '[' {
		var decoded []map[string]rawExtend
		if err := json.Unmarshal(trimmed, &decoded); err != nil || decoded == nil {
			return nil
		}
		merged := make(map[string]rawExtend)
		for _, entry := range decoded {
			for key, value := range entry {
				merged[key] = value
			}
		}
		return collect(merged)
	}
	var decoded map[string]rawExtend
	if err := json.Unmarshal(trimmed, &decoded); err != nil || decoded == nil {
		return nil
	}
	return collect(decoded)
}

// starGiftCraftOutputIntent is the immutable message intent committed with a
// successful craft outcome. The aggregate may subsequently be hidden, listed
// or otherwise edited; an exact retry must still use the first intent and its
// fingerprint instead of rebuilding a different message from mutable state.
type starGiftCraftOutputIntent struct {
	Media       *domain.MessageMedia
	Fingerprint []byte
	Date        int
	SavedGiftID int64
}

func starGiftCraftOutputMedia(userID int64, gift domain.UniqueStarGift, saved domain.SavedStarGift) *domain.MessageMedia {
	return &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
		Kind: domain.MessageServiceActionStarGiftUnique, StarGiftUnique: &domain.MessageStarGiftUniqueAction{
			Gift: gift, FromUserID: userID, Saved: !saved.Unsaved, Craft: true,
			CanExportAt: saved.CanExportAt, TransferStars: saved.TransferStars, CanTransferAt: saved.CanTransferAt,
			CanResellAt: saved.CanResellAt, DropOriginalDetailsStars: saved.DropOriginalDetailsStars,
			CanCraftAt: saved.CanCraftAt,
		},
	}}
}

func starGiftCraftOutputRequest(req domain.StarGiftCraftRequest, intent starGiftCraftOutputIntent) domain.SendPrivateTextRequest {
	return domain.SendPrivateTextRequest{
		SenderUserID: req.UserID, RecipientUserID: req.UserID,
		RandomID: lifecycleCommandRandomID("craft", req.UserID, req.CommandKey), Date: intent.Date,
		OriginAuthKeyID: req.OriginAuthKeyID, OriginSessionID: req.OriginSessionID, OriginUserID: req.UserID,
		IdempotencyFingerprint: append([]byte(nil), intent.Fingerprint...), Media: intent.Media,
	}
}

func defaultStarGiftCraftDraw(upper int) (int, error) {
	if upper <= 0 {
		return 0, domain.ErrStarGiftCraftUnavailable
	}
	draw, err := rand.Int(rand.Reader, big.NewInt(int64(upper)))
	if err != nil {
		return 0, err
	}
	return int(draw.Int64()), nil
}

func starGiftCraftInputAvailable(gift domain.UniqueStarGift, owner domain.Peer, survivor bool) bool {
	if gift.Owner != owner || gift.Burned || gift.OwnerAddress != "" || gift.CraftChancePermille <= 0 {
		return false
	}
	// The first input survives a successful craft. Official clients reject an
	// already-addressed NFT in that slot because its on-chain identity cannot be
	// reconciled with the newly crafted model. Addressed burn-only inputs remain
	// valid in the other slots.
	return !survivor || gift.GiftAddress == ""
}

// SweepStarGiftLifecycle advances time-driven aggregates without requiring a
// foreground client RPC. All effects remain local PostgreSQL ledger/message
// mutations; this worker never talks to TON, Fragment, wallets or chain nodes.
func (s *StarGiftLifecycleStore) SweepStarGiftLifecycle(ctx context.Context, now, limit int) error {
	if s == nil || s.db == nil || s.messages == nil || now <= 0 || limit <= 0 {
		return domain.ErrStarGiftUnavailable
	}
	if limit > 10000 {
		limit = 10000
	}
	// Payment forms are short-lived intents, not permanent receipts. Committed
	// purchases replay from star_gift_purchase_commands/private-send receipts,
	// so expired form rows can be removed independently in a bounded batch.
	formLimit := limit
	if formLimit > 1000 {
		formLimit = 1000
	}
	if _, err := s.db.Exec(ctx, `WITH stale AS (
SELECT buyer_user_id,form_id FROM star_gift_purchase_forms
WHERE expires_at<$1 ORDER BY expires_at,buyer_user_id,form_id
FOR UPDATE SKIP LOCKED LIMIT $2)
DELETE FROM star_gift_purchase_forms f USING stale
WHERE f.buyer_user_id=stale.buyer_user_id AND f.form_id=stale.form_id`, now, formLimit); err != nil {
		return err
	}
	remaining := limit
	for remaining > 0 {
		batch := remaining
		if batch > 100 {
			batch = 100
		}
		count, err := s.expireStarGiftOffersBatch(ctx, now, batch)
		if err != nil {
			return err
		}
		remaining -= count
		if count < batch {
			break
		}
	}
	for remaining > 0 {
		batch := remaining
		if batch > 100 {
			batch = 100
		}
		count, err := s.dispatchStarGiftOfferResolutions(ctx, batch)
		if err != nil {
			return err
		}
		remaining -= count
		if count < batch {
			break
		}
	}

	auctionLimit := remaining
	if auctionLimit > 100 {
		auctionLimit = 100
	}
	if auctionLimit > 0 {
		rows, err := s.db.Query(ctx, `SELECT gift_id FROM star_gift_auctions
WHERE (status='pending' AND start_date<=$1) OR
      (status='active' AND (next_round_at<=$1 OR end_date<=$1))
ORDER BY next_round_at,gift_id LIMIT $2`, now, auctionLimit)
		if err != nil {
			return err
		}
		giftIDs := make([]int64, 0, auctionLimit)
		for rows.Next() {
			var giftID int64
			if err := rows.Scan(&giftID); err != nil {
				rows.Close()
				return err
			}
			giftIDs = append(giftIDs, giftID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, giftID := range giftIDs {
			if err := s.settleStarGiftAuction(ctx, giftID, now); err != nil {
				return err
			}
			if err := s.dispatchStarGiftAuctionAwards(ctx, giftID); err != nil {
				return err
			}
		}
		remaining -= len(giftIDs)
	}

	// A prior process can commit award rows and stop before delivery. Drain those
	// rows even if their auction clock is no longer due.
	if remaining > 0 {
		rows, err := s.db.Query(ctx, `SELECT DISTINCT gift_id FROM star_gift_auction_acquired
WHERE saved_gift_id IS NULL ORDER BY gift_id LIMIT $1`, minAuctionInt(remaining, 100))
		if err != nil {
			return err
		}
		giftIDs := make([]int64, 0)
		for rows.Next() {
			var giftID int64
			if err := rows.Scan(&giftID); err != nil {
				rows.Close()
				return err
			}
			giftIDs = append(giftIDs, giftID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, giftID := range giftIDs {
			if err := s.dispatchStarGiftAuctionAwards(ctx, giftID); err != nil {
				return err
			}
		}
	}
	_, err := s.dispatchChannelStarGiftNotifications(ctx, now, minAuctionInt(limit, 100), 0)
	return err
}

func (s *StarGiftLifecycleStore) ListCraftStarGifts(ctx context.Context, userID, giftID int64, offset string, limit int) (domain.SavedStarGiftPage, error) {
	if s == nil || s.db == nil || userID <= 0 || giftID <= 0 || limit <= 0 || limit > domain.MaxSavedStarGiftsLimit || len(offset) > domain.MaxStarGiftsOffsetBytes {
		return domain.SavedStarGiftPage{}, domain.ErrStarGiftCraftUnavailable
	}
	args := []any{userID, giftID}
	where := `p.owner_peer_type='user' AND p.owner_peer_id=$1 AND p.gift_id=$2
        AND p.lifecycle_status='active' AND p.unique_gift_id IS NOT NULL
        AND p.can_craft_at>0 AND p.can_craft_at<=EXTRACT(EPOCH FROM now())::integer
        AND NOT u.burned AND u.owner_address='' AND u.craft_chance_permille>0
        AND EXISTS (SELECT 1 FROM star_gift_collectible_models m
                    WHERE m.collectible_revision_id=u.collectible_revision_id AND m.crafted)`
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM peer_star_gifts p JOIN unique_star_gifts u ON u.id=p.unique_gift_id WHERE `+where, args...).Scan(&total); err != nil {
		return domain.SavedStarGiftPage{}, fmt.Errorf("count craft star gifts: %w", err)
	}
	if cursor, ok := domain.DecodeStarGiftCursor(offset); ok {
		args = append(args, cursor)
		where += fmt.Sprintf(" AND p.id<$%d", len(args))
	} else if offset != "" {
		return domain.SavedStarGiftPage{}, domain.ErrStarGiftCraftUnavailable
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, `SELECT p.id,p.owner_peer_type,p.owner_peer_id,p.from_user_id,p.gift_id,p.catalog_revision_id,
p.msg_id,p.saved_id,p.gift_date,p.name_hidden,p.unsaved,p.converted,p.convert_stars,p.prepaid_upgrade_stars,p.prepaid_upgrade_hash,p.gift_num,
p.lifecycle_status,p.transfer_stars,p.can_export_at,p.can_transfer_at,p.can_resell_at,p.drop_original_details_stars,p.can_craft_at,
p.message,COALESCE(p.message_entities,'[]'::jsonb)::text,COALESCE(p.unique_gift_id,0),p.upgrade_msg_id,p.pinned_order,
COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order,i.collection_id) FROM star_gift_collection_items i
JOIN star_gift_collections c ON c.collection_id=i.collection_id WHERE i.saved_gift_id=p.id),ARRAY[]::integer[]),COALESCE(p.refunded, false)
FROM peer_star_gifts p JOIN unique_star_gifts u ON u.id=p.unique_gift_id WHERE `+where+`
ORDER BY p.id DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return domain.SavedStarGiftPage{}, fmt.Errorf("list craft star gifts: %w", err)
	}
	defer rows.Close()
	gifts := make([]domain.SavedStarGift, 0, limit+1)
	uniqueIDs := make([]int64, 0, limit+1)
	for rows.Next() {
		gift, scanErr := scanSavedStarGift(rows)
		if scanErr != nil {
			return domain.SavedStarGiftPage{}, scanErr
		}
		gifts = append(gifts, gift)
		uniqueIDs = append(uniqueIDs, gift.UniqueGiftID)
	}
	if err := rows.Err(); err != nil {
		return domain.SavedStarGiftPage{}, err
	}
	hasMore := len(gifts) > limit
	if hasMore {
		gifts, uniqueIDs = gifts[:limit], uniqueIDs[:limit]
	}
	uniqueByID, err := NewStarGiftStore(s.db).UniqueByIDs(ctx, uniqueIDs)
	if err != nil {
		return domain.SavedStarGiftPage{}, err
	}
	for i := range gifts {
		unique, ok := uniqueByID[gifts[i].UniqueGiftID]
		if !ok {
			return domain.SavedStarGiftPage{}, domain.ErrStarGiftCraftUnavailable
		}
		gifts[i].Unique = &unique
	}
	page := domain.SavedStarGiftPage{Count: total, Gifts: gifts}
	if hasMore && len(gifts) > 0 {
		page.NextOffset = domain.EncodeStarGiftCursor(gifts[len(gifts)-1].ID)
	}
	return page, nil
}

func (s *StarGiftLifecycleStore) CraftStarGift(ctx context.Context, req domain.StarGiftCraftRequest) (domain.StarGiftCraftResult, error) {
	if s == nil || s.db == nil || s.messages == nil || s.craftDraw == nil || req.UserID <= 0 || len(req.Refs) < 1 || len(req.Refs) > 4 || req.Date <= 0 ||
		strings.TrimSpace(req.CommandKey) == "" || len(req.CommandKey) > 256 {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}
	for _, ref := range req.Refs {
		if !ref.Valid() || ref.Owner != owner {
			return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
		}
	}
	// A committed failed craft has already moved every input out of the active
	// lifecycle. Consult the immutable receipt before active-gift resolution so
	// an exact transport retry can still replay the same terminal result.
	if replay, output, found, err := s.loadCraftReplay(ctx, req); err != nil || found {
		if err != nil || !replay.Success {
			return replay, err
		}
		return s.deliverCraftSuccess(ctx, req, replay, output)
	}
	savedIDs, err := NewStarGiftStore(s.db).ResolveSavedIDs(ctx, owner, req.Refs)
	if err != nil {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	if len(sortedUniqueInt64(savedIDs)) != len(savedIDs) {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	var result domain.StarGiftCraftResult
	var output starGiftCraftOutputIntent
	err = withTx(ctx, s.db, "craft star gift", func(tx pgx.Tx) error {
		lockedRows, err := tx.Query(ctx, `SELECT id FROM peer_star_gifts WHERE id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`, sortedUniqueInt64(savedIDs))
		if err != nil {
			return err
		}
		locked := 0
		for lockedRows.Next() {
			locked++
		}
		lockedRows.Close()
		if locked != len(savedIDs) {
			return domain.ErrStarGiftCraftUnavailable
		}

		savedByID := make(map[int64]domain.SavedStarGift, len(savedIDs))
		uniqueIDs := make([]int64, 0, len(savedIDs))
		var giftID, revisionID int64
		chance := 0
		for i := range req.Refs {
			saved, err := lockSavedStarGiftByID(ctx, tx, savedIDs[i])
			if err != nil || !saved.LifecycleStatus.Live() || saved.UniqueGiftID == 0 || saved.CanCraftAt <= 0 || saved.CanCraftAt > req.Date {
				return domain.ErrStarGiftCraftUnavailable
			}
			unique, found, err := NewStarGiftStore(tx).UniqueByID(ctx, saved.UniqueGiftID)
			if err != nil || !found || !starGiftCraftInputAvailable(unique, owner, i == 0) {
				return domain.ErrStarGiftCraftUnavailable
			}
			if giftID == 0 {
				giftID, revisionID = unique.GiftID, unique.CollectibleRevisionID
			} else if unique.GiftID != giftID || unique.CollectibleRevisionID != revisionID {
				return domain.ErrStarGiftCraftUnavailable
			}
			savedByID[saved.ID] = saved
			uniqueIDs = append(uniqueIDs, unique.ID)
			chance += unique.CraftChancePermille
		}
		if chance > 1000 {
			chance = 1000
		}
		var craftable bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM star_gift_collectible_models
WHERE collectible_revision_id=$1 AND crafted
)`, revisionID).Scan(&craftable); err != nil {
			return err
		}
		if !craftable {
			return domain.ErrStarGiftCraftUnavailable
		}
		draw, err := s.craftDraw(1000)
		if err != nil {
			return fmt.Errorf("draw star gift craft outcome: %w", err)
		}
		result.Chance = chance
		result.Success = draw < chance

		if _, err := tx.Exec(ctx, `SELECT id FROM unique_star_gifts WHERE id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`, sortedUniqueInt64(uniqueIDs)); err != nil {
			return err
		}
		// TDesktop deliberately keeps Craft available after an owner lists a
		// collectible. Consuming the gift therefore closes every market claim in
		// the same transaction: pending buyers are refunded before their offers
		// are cancelled, listings disappear, and the catalog resale projection is
		// refreshed before any input is crafted or burned.
		for _, uniqueID := range uniqueIDs {
			if err := s.refundPendingStarGiftOffers(ctx, tx, uniqueID, req.Date, "gift crafted"); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM star_gift_listings WHERE unique_gift_id=ANY($1::bigint[])`, uniqueIDs); err != nil {
			return err
		}
		if err := updateStarGiftResaleProjection(ctx, tx, giftID); err != nil {
			return err
		}
		for _, savedID := range savedIDs {
			saved := savedByID[savedID]
			if err := removeSavedGiftFromCollections(ctx, tx, saved.Owner, saved.ID); err != nil {
				return err
			}
		}

		firstSavedID, firstUniqueID := savedIDs[0], uniqueIDs[0]
		if result.Success {
			modelID, err := chooseCraftedModel(ctx, tx, revisionID)
			if err != nil {
				return err
			}
			patternID, err := chooseCollectibleAttribute(ctx, tx, "star_gift_collectible_patterns", revisionID)
			if err != nil {
				return err
			}
			backdropID, err := chooseCollectibleAttribute(ctx, tx, "star_gift_collectible_backdrops", revisionID)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET model_attribute_id=$2,pattern_attribute_id=$3,
        backdrop_attribute_id=$4,crafted=true,craft_chance_permille=0,offer_min_stars=0,updated_at=now() WHERE id=$1`, firstUniqueID, modelID, patternID, backdropID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET can_craft_at=0 WHERE id=$1`, firstSavedID); err != nil {
				return err
			}
		}
		burnFrom := 0
		if result.Success {
			burnFrom = 1
		}
		if burnFrom < len(uniqueIDs) {
			if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET burned=true,craft_chance_permille=0,
offer_min_stars=0,updated_at=now() WHERE id=ANY($1::bigint[])`, uniqueIDs[burnFrom:]); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET lifecycle_status='burned',unsaved=true,pinned_order=0,
transfer_stars=0,can_export_at=0,can_transfer_at=0,can_resell_at=0,drop_original_details_stars=0,can_craft_at=0
WHERE id=ANY($1::bigint[])`, savedIDs[burnFrom:]); err != nil {
				return err
			}
		}
		sourceEdits, sourceEditPTS, err := s.markCraftInputMessagesTx(ctx, tx, req, savedIDs)
		if err != nil {
			return err
		}
		result.SourceEdits = sourceEdits
		var resultID any
		var outputMediaJSON any
		var outputFingerprint any
		if result.Success {
			resultID = firstUniqueID
			gift, found, err := NewStarGiftStore(tx).UniqueByID(ctx, firstUniqueID)
			if err != nil || !found {
				if err != nil {
					return err
				}
				return domain.ErrStarGiftCraftUnavailable
			}
			saved, found, err := savedStarGiftByID(ctx, tx, firstSavedID)
			if err != nil || !found || saved.Owner != owner || saved.UniqueGiftID != gift.ID ||
				!saved.LifecycleStatus.Live() || gift.Owner != owner || gift.Burned || !gift.Crafted {
				if err != nil {
					return err
				}
				return domain.ErrStarGiftCraftUnavailable
			}
			media := starGiftCraftOutputMedia(req.UserID, gift, saved)
			intent := starGiftCraftOutputIntent{Media: media, Date: req.Date, SavedGiftID: saved.ID}
			fingerprint, err := store.PrivateSendFingerprint(starGiftCraftOutputRequest(req, intent))
			if err != nil {
				return fmt.Errorf("fingerprint crafted gift output: %w", err)
			}
			mediaJSON, err := encodeMessageMedia(media)
			if err != nil {
				return fmt.Errorf("encode crafted gift output: %w", err)
			}
			intent.Fingerprint = fingerprint
			output = intent
			outputMediaJSON = mediaJSON
			outputFingerprint = fingerprint
			giftCopy := gift
			result.Gift = &giftCopy
		}
		_, err = tx.Exec(ctx, `INSERT INTO star_gift_craft_commands(user_id,command_key,input_unique_gift_ids,gift_id,
success,result_unique_gift_id,chance_permille,created_at,source_edit_pts,output_media,output_fingerprint)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, req.UserID, strings.TrimSpace(req.CommandKey), uniqueIDs,
			giftID, result.Success, resultID, chance, req.Date, sourceEditPTS, outputMediaJSON, outputFingerprint)
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			if replay, replayOutput, found, replayErr := s.loadCraftReplay(ctx, req); replayErr != nil || found {
				if replayErr == nil && found && replay.Success {
					return s.deliverCraftSuccess(ctx, req, replay, replayOutput)
				}
				return replay, replayErr
			}
		}
		return domain.StarGiftCraftResult{}, err
	}
	if result.Success {
		return s.deliverCraftSuccess(ctx, req, result, output)
	}
	return result, nil
}

func (s *StarGiftLifecycleStore) deliverCraftSuccess(ctx context.Context, req domain.StarGiftCraftRequest, result domain.StarGiftCraftResult, output starGiftCraftOutputIntent) (domain.StarGiftCraftResult, error) {
	if result.Gift == nil || s.messages == nil || output.Media == nil || output.Date <= 0 || output.SavedGiftID <= 0 ||
		store.ValidateSendFingerprint(output.Fingerprint, "crafted gift output") != nil {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	messageReq := starGiftCraftOutputRequest(req, output)
	hooks := privateSendTxHooks{after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
		return registerUserStarGiftMessageRef(ctx, tx, req.UserID, sent.SenderMessage.ID, output.SavedGiftID, result.Gift.ID)
	}}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		return domain.StarGiftCraftResult{}, err
	}
	registered, err := userStarGiftMessageRefMatches(ctx, s.db, req.UserID, sent.SenderMessage.ID, output.SavedGiftID)
	if err != nil || !registered {
		if err != nil {
			return domain.StarGiftCraftResult{}, err
		}
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	result.Send = sent
	result.Duplicate = result.Duplicate || sent.Duplicate
	return result, nil
}

func (s *StarGiftLifecycleStore) loadCraftReplay(ctx context.Context, req domain.StarGiftCraftRequest) (domain.StarGiftCraftResult, starGiftCraftOutputIntent, bool, error) {
	var success bool
	var resultID *int64
	var chance int
	var inputUniqueIDs []int64
	var sourceEditPTS []int32
	var createdAt int
	var outputMediaJSON, outputFingerprint []byte
	err := s.db.QueryRow(ctx, `SELECT input_unique_gift_ids,success,result_unique_gift_id,chance_permille,source_edit_pts,
created_at,output_media,output_fingerprint
FROM star_gift_craft_commands WHERE user_id=$1 AND command_key=$2`,
		req.UserID, strings.TrimSpace(req.CommandKey)).Scan(&inputUniqueIDs, &success, &resultID, &chance, &sourceEditPTS,
		&createdAt, &outputMediaJSON, &outputFingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, nil
	}
	if err != nil {
		return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, err
	}
	if len(req.Refs) != len(inputUniqueIDs) || len(req.Refs) != len(sourceEditPTS) {
		return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, domain.ErrStarGiftCraftUnavailable
	}
	savedIDs := make([]int64, 0, len(inputUniqueIDs))
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}
	for i, uniqueID := range inputUniqueIDs {
		saved, found, err := savedStarGiftByUniqueID(ctx, s.db, uniqueID)
		if err != nil || !found || saved.Owner != owner || saved.UniqueGiftID != uniqueID {
			return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, domain.ErrStarGiftCraftUnavailable
		}
		ref := req.Refs[i]
		if ref.Owner != owner {
			return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, domain.ErrStarGiftCraftUnavailable
		}
		if ref.Slug != "" {
			unique, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, uniqueID)
			if err != nil || !found || !strings.EqualFold(ref.Slug, unique.Slug) {
				return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, domain.ErrStarGiftCraftUnavailable
			}
		} else if ref.MsgID != saved.MsgID {
			matches, err := userStarGiftMessageRefMatches(ctx, s.db, req.UserID, ref.MsgID, saved.ID)
			if err != nil {
				return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, err
			}
			if !matches {
				return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, domain.ErrStarGiftCraftUnavailable
			}
		}
		savedIDs = append(savedIDs, saved.ID)
	}
	sourceEdits, err := s.loadCraftInputMessageReplays(ctx, req, savedIDs, sourceEditPTS)
	if err != nil {
		return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, err
	}
	result := domain.StarGiftCraftResult{Success: success, Chance: chance, SourceEdits: sourceEdits, Duplicate: true}
	if !success {
		if resultID != nil || len(outputMediaJSON) != 0 || len(outputFingerprint) != 0 {
			return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, domain.ErrStarGiftCraftUnavailable
		}
		return result, starGiftCraftOutputIntent{}, true, nil
	}
	if resultID == nil || createdAt <= 0 || store.ValidateSendFingerprint(outputFingerprint, "crafted gift replay") != nil {
		return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, domain.ErrStarGiftCraftUnavailable
	}
	media, err := decodeMessageMedia(string(outputMediaJSON))
	if err != nil || media == nil || media.Kind != domain.MessageMediaKindService || media.ServiceAction == nil ||
		media.ServiceAction.Kind != domain.MessageServiceActionStarGiftUnique || media.ServiceAction.StarGiftUnique == nil ||
		!media.ServiceAction.StarGiftUnique.Craft || media.ServiceAction.StarGiftUnique.Gift.ID != *resultID {
		return domain.StarGiftCraftResult{}, starGiftCraftOutputIntent{}, false, domain.ErrStarGiftCraftUnavailable
	}
	gift := media.ServiceAction.StarGiftUnique.Gift
	result.Gift = &gift
	output := starGiftCraftOutputIntent{Media: media, Fingerprint: append([]byte(nil), outputFingerprint...),
		Date: createdAt, SavedGiftID: savedIDs[0]}
	return result, output, true, nil
}

func chooseCraftedModel(ctx context.Context, tx pgx.Tx, revisionID int64) (int64, error) {
	var count int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_collectible_models WHERE collectible_revision_id=$1 AND crafted`, revisionID).Scan(&count); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, domain.ErrStarGiftCraftUnavailable
	}
	draw, err := rand.Int(rand.Reader, big.NewInt(count))
	if err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM star_gift_collectible_models WHERE collectible_revision_id=$1 AND crafted ORDER BY sort_order,id OFFSET $2 LIMIT 1`, revisionID, draw.Int64()).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// settleStarGiftAuction lazily advances every elapsed round. Auction rows are
// the clock aggregate; acquired rows are a durable delivery outbox. A winner's
// reserved bid is consumed, while bids that can no longer reach any remaining
// gift are refunded atomically with the state transition.
func (s *StarGiftLifecycleStore) settleStarGiftAuction(ctx context.Context, giftID int64, now int) error {
	if giftID <= 0 || now <= 0 {
		return domain.ErrStarGiftAuctionUnavailable
	}
	return withTx(ctx, s.db, "settle star gift auction", func(tx pgx.Tx) error {
		var startDate, endDate, roundDuration, giftsPerRound, totalRounds, currentRound, nextRoundAt, lastGiftNum, giftsLeft int
		var minBidAmount int64
		var status string
		var raiseMinBid bool
		var roundExtendSeconds int
		var roundDurationsRaw, roundExtendsRaw []byte
		if err := tx.QueryRow(ctx, `SELECT start_date,end_date,round_duration,gifts_per_round,total_rounds,current_round,
next_round_at,last_gift_num,gifts_left,status,round_durations,min_bid_amount,raise_min_bid,round_extends,round_extend_seconds FROM star_gift_auctions WHERE gift_id=$1 FOR UPDATE`, giftID).
			Scan(&startDate, &endDate, &roundDuration, &giftsPerRound, &totalRounds, &currentRound,
				&nextRoundAt, &lastGiftNum, &giftsLeft, &status, &roundDurationsRaw, &minBidAmount, &raiseMinBid, &roundExtendsRaw, &roundExtendSeconds); err != nil {
			return err
		}
		if status == "cancelled" || status == "completed" {
			return nil
		}
		if now < startDate {
			return nil
		}
		roundDurations := parseAuctionRoundDurations(roundDurationsRaw)
		_ = parseAuctionRoundExtends(roundExtendsRaw) // валидация формата; продления применяет ставка
		changed := false
		if status == "pending" {
			status = "active"
			changed = true
			if currentRound == 0 {
				currentRound = 1
			}
		}
		// Раунд 20: индивидуальные длительности раундов. Пересчёт границы из
		// start_date и длительностей 1..currentRound применяется ТОЛЬКО когда
		// jsonb задан (немедленно применяет его к уже идущему аукциону);
		// сохранённое next_round_at остаётся авторитетным для NULL-jsonb —
		// ручные корректировки границы и продления ставок не затираются.
		// Раунд 21: добавка round_extend_seconds сохраняет продления,
		// применённые ставками в середине текущего раунда.
		if currentRound >= 1 && roundDurations != nil {
			nextRoundAt = auctionRoundBoundary(startDate, roundDurations, currentRound, roundDuration, roundExtendSeconds)
		}

		totalBurnedInThisExecution := 0
		// Раунд 21 (исправление): продления раундов сдвигают все последующие
		// границы, поэтому время окончания аукциона должно пересчитываться
		// вместе с ними — иначе аукцион завершается по старому end_date раньше,
		// чем пройдут все раунды. derivedAuctionEnd возвращает минимум конца
		// аукциона: граница текущего раунда плюс длительности оставшихся.
		extendEndDate := func() {
			derived := derivedAuctionEnd(roundDurations, currentRound, totalRounds, roundDuration, nextRoundAt)
			if derived > endDate {
				endDate = derived
				changed = true
			}
		}
		extendEndDate()

		// roundsAdvanced — из текущего вызова прошёл хотя бы один раунд.
		// Возвраты ставок выполняются только на границах раундов и при
		// завершении: посреди раунда перебитый участник обязан иметь
		// возможность поднять ставку до конца раунда (раунд 21, исправление).
		roundsAdvanced := false
		for status == "active" && currentRound <= totalRounds && nextRoundAt <= now {
			// awardLimit — это сколько подарков БЫЛО ВЫСТАВЛЕНО на текущий раунд
			awardLimit := giftsPerRound
			if awardLimit > giftsLeft {
				awardLimit = giftsLeft
			}

			type winner struct {
				userID, recipientID, amount int64
				recipientType               string
				bidDate                     int
				hide                        bool
				message                     string
			}
			winners := make([]winner, 0, awardLimit)
			if awardLimit > 0 {
				rows, err := tx.Query(ctx, `SELECT bidder_user_id,recipient_peer_type,recipient_peer_id,amount,bid_date,hide_name,message
FROM star_gift_auction_bids WHERE gift_id=$1 AND active ORDER BY amount DESC,bid_date,bidder_user_id LIMIT $2 FOR UPDATE`, giftID, awardLimit)
				if err != nil {
					return err
				}
				for rows.Next() {
					var winner winner
					if err := rows.Scan(&winner.userID, &winner.recipientType, &winner.recipientID, &winner.amount,
						&winner.bidDate, &winner.hide, &winner.message); err != nil {
						rows.Close()
						return err
					}
					winners = append(winners, winner)
				}
				rows.Close()
			}

			// Если ставок нет, мы не просто «перематываем» время, а фиксируем сжигание всего раунда
			if len(winners) == 0 {
				// Сгорает весь лимит раунда
				lastGiftNum += awardLimit
				giftsLeft -= awardLimit
				totalBurnedInThisExecution += awardLimit
			} else {
				// Распределяем подарки среди победителей
				for pos, winner := range winners {
					giftNum := lastGiftNum + pos + 1
					if _, err := tx.Exec(ctx, `INSERT INTO star_gift_auction_acquired(gift_id,bidder_user_id,recipient_peer_type,
recipient_peer_id,bid_amount,round,pos,gift_num,acquired_at,hide_name,message)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(gift_id,round,pos) DO NOTHING`,
						giftID, winner.userID, winner.recipientType, winner.recipientID, winner.amount, currentRound, pos+1,
						giftNum, nextRoundAt, winner.hide, winner.message); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `UPDATE star_gift_auction_bids SET active=false,returned=false,
acquired_count=acquired_count+1,version=version+1 WHERE gift_id=$1 AND bidder_user_id=$2 AND active`, giftID, winner.userID); err != nil {
						return err
					}
				}

				// ЛОГИКА СЖИГАНИЯ: даже если победителей 5, а лимит 7, мы увеличиваем
				// lastGiftNum на 7. Номера 6 и 7 «пропускаются».
				burnedInRound := awardLimit - len(winners)
				lastGiftNum += awardLimit // Увеличиваем на весь лимит раунда
				giftsLeft -= awardLimit   // Уменьшаем остаток на весь лимит раунда
				totalBurnedInThisExecution += burnedInRound
			}

			currentRound++
			// Граница нового текущего раунда: индивидуальная длительность
			// из round_durations (раунд 20), для пропущенных раундов — общая.
			nextRoundAt += auctionRoundDurationFor(roundDurations, currentRound, roundDuration)
			// Раунд 21: продление действует только внутри одного раунда —
			// на новой границе счётчик обнуляется.
			roundExtendSeconds = 0
			if raiseMinBid {
				// Режим повышения: по окончании раунда победители выявлены,
				// минимальная ставка откатывается к стоимости подарка в
				// ревизии для следующего раунда.
				var revisionStars int64
				if err := tx.QueryRow(ctx, `SELECT GREATEST(COALESCE((SELECT r.stars FROM star_gift_catalog c
JOIN star_gift_catalog_revisions r ON r.id=c.active_revision_id WHERE c.gift_id=$1), 0), 1)`, giftID).Scan(&revisionStars); err != nil {
					return err
				}
				minBidAmount = revisionStars
			}
			changed = true
			roundsAdvanced = true
			// Раунд 21 (исправление): по окончании раунда определяется новое
			// время окончания аукциона — продленный раунд отодвигает конец
			// аукциона, чтобы все оставшиеся раунды успели пройти.
			extendEndDate()
			if currentRound > totalRounds || giftsLeft <= 0 {
				status = "completed"
			}
		}

		if status == "active" {
			extendEndDate()
			if now >= endDate {
				status = "completed"
				changed = true
			}
		}

		if roundsAdvanced || status == "completed" {
			if err := s.refundUnreachableAuctionBids(ctx, tx, giftID, giftsLeft, status == "completed", now); err != nil {
				return err
			}
		}

		if changed {
			// Обновляем состояние аукциона (last_gift_num теперь включает сгоревшие номера);
			// end_date фиксирует пересчитанное время окончания (см. extendEndDate).
			if _, err := tx.Exec(ctx, `UPDATE star_gift_auctions SET status=$2,current_round=$3,next_round_at=$4,
last_gift_num=$5,gifts_left=$6,round_extend_seconds=$7,min_bid_amount=$8,end_date=$9,version=version+1,updated_at=now() WHERE gift_id=$1`, giftID, status,
				minAuctionInt(currentRound, totalRounds), minAuctionInt(nextRoundAt, endDate), lastGiftNum, giftsLeft,
				roundExtendSeconds, minBidAmount, endDate); err != nil {
				return err
			}
			// ОБЯЗАТЕЛЬНО: Синхронизируем availability_remains в каталоге, вычитая из него ВСЕ
			// ушедшие из аукциона подарки (и выигранные, и сгоревшие).
			// awardedCount здесь заменен на расчет на базе gifts_left.
			if _, err := tx.Exec(ctx, `UPDATE star_gift_catalog SET availability_remains=$2, 
updated_at=now() WHERE gift_id=$1`, giftID, giftsLeft); err != nil {
				return err
			}
			if err := markCatalogSoldOutIfExhausted(ctx, tx, giftID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *StarGiftLifecycleStore) refundUnreachableAuctionBids(ctx context.Context, tx pgx.Tx, giftID int64, giftsLeft int, all bool, date int) error {
	offset := giftsLeft
	if all {
		offset = 0
	}
	rows, err := tx.Query(ctx, `SELECT bidder_user_id,recipient_peer_type,recipient_peer_id,amount FROM star_gift_auction_bids
WHERE gift_id=$1 AND active ORDER BY amount DESC,bid_date,bidder_user_id OFFSET $2 FOR UPDATE`, giftID, offset)
	if err != nil {
		return err
	}
	type refundable struct {
		userID, peerID, amount int64
		peerType               string
	}
	items := make([]refundable, 0)
	for rows.Next() {
		var item refundable
		if err := rows.Scan(&item.userID, &item.peerType, &item.peerID, &item.amount); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if err := s.creditLifecycleAmount(ctx, tx, item.userID,
			domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: item.amount},
			domain.StarsReasonGiftAuction, domain.Peer{Type: domain.PeerType(item.peerType), ID: item.peerID}, date,
			"", starsLedgerExtra{giftID: giftID, refunded: true}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE star_gift_auction_bids SET active=false,returned=true,version=version+1
WHERE gift_id=$1 AND bidder_user_id=$2 AND active`, giftID, item.userID); err != nil {
			return err
		}
	}
	return nil
}

func minAuctionInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Константы конверсии выигрыша аукциона: официальный дамп 185743 несёт
// gift.convert_stars ровно 85% выигрышной ставки (4444500 → 3777825).
const (
	starGiftAuctionConvertNumerator   = 850
	starGiftAuctionConvertDenominator = 1000
)

func (s *StarGiftLifecycleStore) dispatchStarGiftAuctionAwards(ctx context.Context, giftID int64) error {
	if s.messages == nil {
		return domain.ErrStarGiftAuctionUnavailable
	}
	gift, found, err := NewStarGiftStore(s.db).CatalogGift(ctx, giftID)
	if err != nil || !found {
		return domain.ErrStarGiftAuctionUnavailable
	}
	// Экземпляр победителя уходит от имени 777000: строка системного аккаунта
	// должна существовать до вставки бокса сообщения (идемпотентно).
	if err := ensureOfficialSystemUserWithDB(ctx, s.db, domain.Message{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}}); err != nil {
		return err
	}
	dispatched := 0
	for dispatched < maxStarGiftAuctionAcquired {
		rows, err := s.db.Query(ctx, `SELECT id,bidder_user_id,recipient_peer_type,recipient_peer_id,bid_amount,
round,pos,COALESCE(gift_num,0),acquired_at,hide_name,message FROM star_gift_auction_acquired
WHERE gift_id=$1 AND saved_gift_id IS NULL ORDER BY id LIMIT 100`, giftID)
		if err != nil {
			return err
		}
		items := make([]struct {
			id, bidder, recipientID, amount int64
			recipientType                   string
			round, pos, giftNum, date       int
			hide                            bool
			message                         string
		}, 0)
		for rows.Next() {
			var item struct {
				id, bidder, recipientID, amount int64
				recipientType                   string
				round, pos, giftNum, date       int
				hide                            bool
				message                         string
			}
			if err := rows.Scan(&item.id, &item.bidder, &item.recipientType, &item.recipientID, &item.amount,
				&item.round, &item.pos, &item.giftNum, &item.date, &item.hide, &item.message); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			owner := domain.Peer{Type: domain.PeerType(item.recipientType), ID: item.recipientID}
			// Раунд 21: ревизия улучшений с нулевой ценой — полученные в аукционе
			// подарки улучшаются бесплатно.
			freeUpgrade := false
			if revision, foundRev, errRev := NewStarGiftStore(s.db).ActiveCollectibleProjection(ctx, gift.ID, 0); errRev == nil && foundRev {
				freeUpgrade = revision.UpgradeStars == 0
			}
			// Раунд 22 (механика оригинального сервера): при выигрыше аукциона
			// отправляются ДВА экземпляра сообщения.
			//
			// 1) Экземпляр победителя: сервисное сообщение от имени 777000 в чат
			//    «Telegram». Подпись «Вы выиграли в аукционе со ставкой…» клиент
			//    показывает только в сервисном диалоге при auction_acquired и
			//    ПУСТОМ from_id (MessageObject: UserObject.isService(dialogId) &&
			//    action.from_id == null → ActionGiftAuctionSelf), поэтому from_id
			//    здесь не ставится.
			//
			//    Раунд 23 (дамп 185743 — точная форма официального сообщения):
			//    saved=false (подарок ещё не «сохранён», официальная копия этот
			//    флаг не несёт), name_hidden=true, gift_num отсутствует,
			//    to_id=Peer(owner) — официальный сервер ставит to_id на копии
			//    победителя ВСЕГДА, включая self-выигрыш (в дампе
			//    to_id=PeerUser(2200266520) — сам победитель), а блок StarGift
			//    несёт gift.convert_stars=85% ставки при пустом
			//    action.convert_stars. В дампе can_upgrade отсутствует, потому
			//    что у Lime нет улучшений вовсе (upgrade_variants=null).
			//
			//    Раунд 24: при наличии улучшений (бесплатная ревизия либо
			//    платная с неисчерпанным пулом) копия победителя несёт
			//    can_upgrade — клиент рисует кнопку улучшения с описанием из
			//    gift.upgrade_stars (бесплатно / «за N звёзд»). released_by
			//    («кем выпущено») проецируется fillStarGiftSnapshot из каталога.
			sticker := gift.Sticker
			winAction := fillStarGiftSnapshot(&domain.MessageStarGiftAction{GiftID: gift.ID,
				// item.amount — выигрышная ставка победителя: цена в блоке
				// подарка показывает её, а не цену каталога.
				Stars: item.amount, Title: gift.Title, Sticker: &sticker,
				PeerUserID: owner.ID, NameHidden: true,
				// to_id нужен только копии победителя (официально — всегда).
				To:              owner,
				AuctionAcquired: true,
				GiftConvertStars: item.amount * starGiftAuctionConvertNumerator /
					starGiftAuctionConvertDenominator}, gift)
			// Платные улучшения ставит fillStarGiftSnapshot (gift.upgrade_stars>0
			// при issued<total); prepaid_upgrade не ставится никогда — клиент
			// рендерил бы строку «Вы отправили улучшение стоимостью 0 звёзд».
			if freeUpgrade {
				winAction.CanUpgrade = true
			}
			if _, err := s.messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: domain.OfficialSystemUserID,
				RecipientUserID: item.bidder, RandomID: lifecycleCommandRandomID("auction-win", giftID, item.round, item.pos),
				Date: item.date, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
					Kind: domain.MessageServiceActionStarGift, StarGift: winAction}}}); err != nil {
				return err
			}
			// 2) Экземпляр получателя: обычное сервисное сообщение о покупке
			//    (себе или кому-то) — выглядит как обычная покупка подарка, но
			//    цена наследуется из выигрышной ставки. Без auction_acquired и
			//    to_id: клиент рисует стандартные подписи («Вы приобрели подарок»
			//    / «Вы отправили подарок …»). Его msgID становится якорем
			//    сохранённого подарка.
			var msgID int
			if owner.Type == domain.PeerTypeUser {
				sticker := gift.Sticker
				action := fillStarGiftSnapshot(&domain.MessageStarGiftAction{GiftID: gift.ID,
					Stars: item.amount, ConvertStars: 0, Title: gift.Title, Sticker: &sticker, Message: item.message,
					FromUserID: item.bidder, PeerUserID: owner.ID, NameHidden: item.hide,
					To: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
					// Свежий подарок, как и при обычной покупке: Saved=false —
					// иначе клиент показывает «подарок виден в вашем профиле»
					// вместо подсказки об улучшении.
					Saved: false}, gift)
				if freeUpgrade {
					action.CanUpgrade = true
				}
				sent, err := s.messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: item.bidder,
					RecipientUserID: owner.ID, RandomID: lifecycleCommandRandomID("auction-award", giftID, item.round, item.pos),
					Date: item.date, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
						Kind: domain.MessageServiceActionStarGift, StarGift: action}}})
				if err != nil {
					return err
				}
				msgID = sent.RecipientMessage.ID
				if msgID <= 0 {
					msgID = sent.SenderMessage.ID
				}
			}
			var awardedSavedID int64
			if err := withTx(ctx, s.db, "save star gift auction award", func(tx pgx.Tx) error {
				var savedID *int64
				if err := tx.QueryRow(ctx, `SELECT saved_gift_id FROM star_gift_auction_acquired WHERE id=$1 FOR UPDATE`, item.id).Scan(&savedID); err != nil {
					return err
				}
				if savedID != nil {
					return nil
				}
				id, err := NewStarGiftStore(tx).Create(ctx, domain.SavedStarGift{Owner: owner, FromUserID: item.bidder,
					GiftID: gift.ID, RevisionID: gift.RevisionID, MsgID: msgID, Date: item.date, NameHidden: item.hide,
					ConvertStars: 0, Message: item.message, GiftNum: item.giftNum})
				if err != nil {
					return err
				}
				if owner.Type == domain.PeerTypeChannel {
					sticker := gift.Sticker
					// item.amount — выигрышная ставка: блок подарка в admin log
					// и уведомлениях показывает её, а не цену каталога.
					action := domain.ChannelMessageAction{Type: domain.ChannelActionStarGift, StarGift: fillStarGiftSnapshot(&domain.MessageStarGiftAction{
						GiftID: gift.ID, Stars: item.amount, ConvertStars: 0, Title: gift.Title, Sticker: &sticker,
						Message: item.message, FromUserID: item.bidder, PeerChannelID: owner.ID, SavedID: id,
						NameHidden: item.hide, Saved: true, AuctionAcquired: true, GiftNum: item.giftNum,
					}, gift)}
					if err := NewChannelStore(tx).appendStarGiftAdminLogTx(ctx, tx, owner.ID, item.bidder, id, item.date, action); err != nil {
						return err
					}
					// Раунд 23: канал-получатель аукционного подарка тоже получает
					// сервисное уведомление от «Telegram» владельцу/админам — та же
					// подсистема, что у обычных канальных подарков.
					if err := enqueueChannelStarGiftNotifications(ctx, tx, id, owner.ID, item.date, action.StarGift, 0); err != nil {
						return err
					}
				}
				_, err = tx.Exec(ctx, `UPDATE star_gift_auction_acquired SET saved_gift_id=$2 WHERE id=$1`, item.id, id)
				awardedSavedID = id
				return err
			}); err != nil {
				return err
			}
			if owner.Type == domain.PeerTypeChannel && awardedSavedID > 0 {
				// Раунд 23: мгновенная доставка канального уведомления (очередь
				// подхватит и sweeper, но лучше не ждать интервала).
				_, _ = s.dispatchChannelStarGiftNotifications(ctx, item.date,
					maxChannelStarGiftNotificationRecipients, awardedSavedID)
			}
			dispatched++
		}
	}
	return nil
}

func (s *StarGiftLifecycleStore) StarGiftAuctionState(ctx context.Context, userID int64, giftID int64, slug string, now int) (domain.StarGiftAuction, error) {
	if s == nil || s.db == nil || userID <= 0 || now <= 0 || giftID <= 0 && strings.TrimSpace(slug) == "" {
		return domain.StarGiftAuction{}, domain.ErrStarGiftAuctionUnavailable
	}
	resolvedGiftID, err := s.ensureStarGiftAuction(ctx, giftID, strings.TrimSpace(slug), now)
	if err != nil {
		return domain.StarGiftAuction{}, err
	}
	if err := s.settleStarGiftAuction(ctx, resolvedGiftID, now); err != nil {
		return domain.StarGiftAuction{}, err
	}
	if err := s.dispatchStarGiftAuctionAwards(ctx, resolvedGiftID); err != nil {
		return domain.StarGiftAuction{}, err
	}
	return s.loadStarGiftAuction(ctx, userID, resolvedGiftID)
}

func (s *StarGiftLifecycleStore) ActiveStarGiftAuctions(ctx context.Context, userID int64, now int) ([]domain.StarGiftAuction, error) {
	if userID <= 0 || now <= 0 {
		return nil, domain.ErrStarGiftAuctionUnavailable
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT a.gift_id FROM star_gift_auctions a
JOIN star_gift_auction_bids b ON b.gift_id=a.gift_id
WHERE b.bidder_user_id=$1 AND a.end_date>$2 AND a.status<>'cancelled' ORDER BY a.gift_id`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	giftIDs := make([]int64, 0)
	for rows.Next() {
		var giftID int64
		if err := rows.Scan(&giftID); err != nil {
			return nil, err
		}
		giftIDs = append(giftIDs, giftID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.StarGiftAuction, 0)
	for _, giftID := range giftIDs {
		state, err := s.StarGiftAuctionState(ctx, userID, giftID, "", now)
		if err != nil {
			return nil, err
		}
		if !state.Finished && state.UserState.BidDate > 0 {
			out = append(out, state)
		}
	}
	return out, nil
}

func (s *StarGiftLifecycleStore) StarGiftAuctionAcquired(ctx context.Context, userID, giftID int64) ([]domain.StarGiftAuctionAcquired, error) {
	if userID <= 0 || giftID <= 0 {
		return nil, domain.ErrStarGiftAuctionUnavailable
	}
	if err := s.dispatchStarGiftAuctionAwards(ctx, giftID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT recipient_peer_type,recipient_peer_id,acquired_at,bid_amount,round,pos,message,
COALESCE(gift_num,0),hide_name FROM star_gift_auction_acquired WHERE bidder_user_id=$1 AND gift_id=$2 ORDER BY id DESC LIMIT $3`,
		userID, giftID, maxStarGiftAuctionAcquired)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.StarGiftAuctionAcquired, 0)
	for rows.Next() {
		var item domain.StarGiftAuctionAcquired
		var peerType string
		if err := rows.Scan(&peerType, &item.Peer.ID, &item.Date, &item.BidAmount, &item.Round, &item.Pos,
			&item.Message, &item.GiftNum, &item.NameHidden); err != nil {
			return nil, err
		}
		item.Peer.Type = domain.PeerType(peerType)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *StarGiftLifecycleStore) BidStarGiftAuction(ctx context.Context, req domain.StarGiftAuctionBidRequest) (domain.StarGiftAuction, domain.StarsBalance, error) {
	if s == nil || s.db == nil || req.UserID <= 0 || req.GiftID <= 0 || !validLifecyclePeer(req.Peer) ||
		req.BidAmount <= 0 || req.FormID == 0 || req.Date <= 0 || len([]rune(req.Message)) > 128 {
		return domain.StarGiftAuction{}, domain.StarsBalance{}, domain.ErrStarGiftAuctionUnavailable
	}
	if _, err := s.ensureStarGiftAuction(ctx, req.GiftID, "", req.Date); err != nil {
		return domain.StarGiftAuction{}, domain.StarsBalance{}, err
	}
	if err := s.settleStarGiftAuction(ctx, req.GiftID, req.Date); err != nil {
		return domain.StarGiftAuction{}, domain.StarsBalance{}, err
	}
	if balance, found, err := s.loadAuctionBidReplay(ctx, req.UserID, req.FormID, req.GiftID); err != nil || found {
		if err != nil {
			return domain.StarGiftAuction{}, domain.StarsBalance{}, err
		}
		state, stateErr := s.loadStarGiftAuction(ctx, req.UserID, req.GiftID)
		return state, balance, stateErr
	}
	var balance domain.StarsBalance
	err := withTx(ctx, s.db, "bid star gift auction", func(tx pgx.Tx) error {
		var startDate, endDate, currentRound, nextRoundAt, totalRounds, roundDuration int
		var minimum int64
		var status string
		var raiseMinBid bool
		var roundExtendSeconds int
		var roundExtendsRaw, roundDurationsRaw []byte
		if err := tx.QueryRow(ctx, `SELECT start_date,end_date,min_bid_amount,status,current_round,next_round_at,
raise_min_bid,round_extends,round_extend_seconds,total_rounds,round_duration,round_durations FROM star_gift_auctions WHERE gift_id=$1 FOR UPDATE`, req.GiftID).
			Scan(&startDate, &endDate, &minimum, &status, &currentRound, &nextRoundAt, &raiseMinBid, &roundExtendsRaw, &roundExtendSeconds,
				&totalRounds, &roundDuration, &roundDurationsRaw); err != nil {
			return err
		}
		roundDurations := parseAuctionRoundDurations(roundDurationsRaw)

		// Раунд 21: два режима минимальной ставки.
		// raise_min_bid=false — несменяемый минимум: полом ставки служит сам
		// min_bid_amount (административное значение), ставки ниже текущей
		// лидирующей допускаются и переносятся на следующие раунды.
		// raise_min_bid=true — режим повышения: пол = максимум из min_bid_amount
		// и лидирующей активной ставки, УВЕЛИЧЕННОЙ НА 1: если топ-1 поставил
		// 600 звёзд, следующая допустимая ставка — 601; равная топ-1 ставка
		// отклоняется. Пока ставок в раунде нет, действует исходный
		// min_bid_amount (= стоимость ревизии).
		var topBid int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(amount),0) FROM star_gift_auction_bids WHERE gift_id=$1 AND active`, req.GiftID).
			Scan(&topBid); err != nil {
			return err
		}
		minimum = auctionBidFloorMode(raiseMinBid, minimum, topBid)

		// ИСПРАВЛЕНИЕ: Разрешаем ставки, если статус 'pending' (предстоящий) ИЛИ 'active'.
		// Убираем жесткую проверку req.Date < startDate.
		if (status != "active" && status != "pending") || req.Date >= endDate || req.BidAmount < minimum {
			return domain.ErrStarGiftAuctionUnavailable
		}

		// Раунд 21: продление раунда. Для текущего раунда с настройкой
		// round_extends: если в последние extend.ThresholdSeconds раунда ставка
		// изменит состав/порядок топ-extend.TopBidders участников, сервер
		// прибавляет к оставшемуся времени раунда extend.WindowSeconds.
		// Состав топа сравнивается до и после записи ставки (упорядоченный
		// список bidder_user_id), поэтому расширяется только раунд, в котором
		// действительно произошла ротация лидеров.
		extend := parseAuctionRoundExtends(roundExtendsRaw)[currentRound]
		var topSetBefore []int64
		if extend.WindowSeconds > 0 {
			topSetBefore = auctionTopBidderIDs(ctx, tx, req.GiftID, extend.TopBidders)
		}

		var oldAmount int64
		var oldActive bool
		err := tx.QueryRow(ctx, `SELECT amount,active FROM star_gift_auction_bids WHERE gift_id=$1 AND bidder_user_id=$2 FOR UPDATE`, req.GiftID, req.UserID).Scan(&oldAmount, &oldActive)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if oldActive && (!req.UpdateBid || req.BidAmount <= oldAmount) || !oldActive && req.UpdateBid && oldAmount <= 0 {
			// Активная ставка: повторная — только update_bid и строго выше
			// собственной. Возвращённая (returned) ставка активной не
			// является: и «повысить» (update_bid=true), и новая ставка
			// (update_bid=false) списываются целиком (stars уже возвращены),
			// поэтому допускаются при любом update_bid (раунд 21,
			// исправление STARGIFT_INVALID у перебитых участников).
			return domain.ErrStarGiftAuctionUnavailable
		}
		reserved := int64(0)
		if oldActive {
			reserved = oldAmount
		}
		delta := req.BidAmount - reserved
		balance, err = s.debitLifecycleAmount(ctx, tx, req.UserID,
			domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: delta}, domain.StarsReasonGiftAuction,
			req.Peer, req.Date, "", starsLedgerExtra{giftID: req.GiftID})
		if err != nil {
			return err
		}

		// Раунд 31: получатель ставки (recipient_peer_*) обновляется из запроса
		// всегда. При повышении ставки клиент может прислать peer (официальная
		// документация не запрещает peer при update_bid) — он резолвится в RPC
		// и приходит здесь; без peer в счёте RPC подставляет прежний bid_peer,
		// поэтому значение не меняется. hide_name/message при АКТИВНОЙ ставке
		// сохраняются: они «cannot be changed for existing bids» и в счёте
		// обновления не передаются.
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_auction_bids(gift_id,bidder_user_id,recipient_peer_type,recipient_peer_id,
amount,bid_date,hide_name,message) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(gift_id,bidder_user_id) DO UPDATE SET
recipient_peer_type=EXCLUDED.recipient_peer_type,
recipient_peer_id=EXCLUDED.recipient_peer_id,
amount=EXCLUDED.amount,bid_date=EXCLUDED.bid_date,
hide_name=CASE WHEN star_gift_auction_bids.active THEN star_gift_auction_bids.hide_name ELSE EXCLUDED.hide_name END,
message=CASE WHEN star_gift_auction_bids.active THEN star_gift_auction_bids.message ELSE EXCLUDED.message END,
returned=false,active=true,version=star_gift_auction_bids.version+1`,
			req.GiftID, req.UserID, string(req.Peer.Type), req.Peer.ID, req.BidAmount, req.Date, req.HideName, req.Message); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_auction_bid_payments(user_id,form_id,gift_id,bid_amount,balance_after,created_at)
VALUES($1,$2,$3,$4,$5,$6)`, req.UserID, req.FormID, req.GiftID, req.BidAmount, balance.Balance, req.Date); err != nil {
			return err
		}
		// Раунд 21: применяем продление раунда, если ставка изменила топ.
		// Инвариант topSetBefore/topSetAfter — упорядоченный список (amount
		// DESC, bid_date, bidder_user_id), поэтому сравнение списков ловит и
		// вход нового участника в топ, и перестановку внутри него. Первая
		// ставка раунда (топ был пуст) продления не даёт: некого перебивать.
		// Раунд 21 (исправление): окно применяется ЦЕЛИКОМ и отодвигает
		// время окончания аукциона (end_date) — прежняя обрезка по старому
		// end_date не давала раунду реально продлиться, и аукцион мог
		// завершиться раньше, чем пройдут все раунды.
		if extend.WindowSeconds > 0 && len(topSetBefore) > 0 {
			topSetAfter := auctionTopBidderIDs(ctx, tx, req.GiftID, extend.TopBidders)
			if !auctionTopSetsEqual(topSetBefore, topSetAfter) && req.Date >= nextRoundAt-extend.ThresholdSeconds {
				nextRoundAt += extend.WindowSeconds
				roundExtendSeconds += extend.WindowSeconds
				if derived := derivedAuctionEnd(roundDurations, currentRound, totalRounds, roundDuration, nextRoundAt); derived > endDate {
					endDate = derived
				}
			}
		}
		// min_bid_amount: в режиме повышения монотонно поднимается до новой
		// нижней границы «топ-1 + 1», чтобы следующее состояние аукциона (и
		// клиент) показывали актуальный пол ставки. В режиме несменяемого
		// минимума значение не меняется. Обновление также фиксирует продление
		// раунда, пересчитанное end_date и поднимает version — клиенты
		// получат новые границы через updateStarGiftAuctionState.
		if raiseMinBid {
			_, err = tx.Exec(ctx, `UPDATE star_gift_auctions SET version=version+1,updated_at=now(),
min_bid_amount=GREATEST(star_gift_auctions.min_bid_amount,$2),next_round_at=$3,round_extend_seconds=$4,end_date=GREATEST(star_gift_auctions.end_date,$5) WHERE gift_id=$1`,
				req.GiftID, minimum, nextRoundAt, roundExtendSeconds, endDate)
		} else {
			_, err = tx.Exec(ctx, `UPDATE star_gift_auctions SET version=version+1,updated_at=now(),
next_round_at=$2,round_extend_seconds=$3,end_date=GREATEST(star_gift_auctions.end_date,$4) WHERE gift_id=$1`, req.GiftID, nextRoundAt, roundExtendSeconds, endDate)
		}
		return err
	})
	if err != nil {
		return domain.StarGiftAuction{}, domain.StarsBalance{}, err
	}
	state, err := s.loadStarGiftAuction(ctx, req.UserID, req.GiftID)
	return state, balance, err
}

func (s *StarGiftLifecycleStore) ensureStarGiftAuction(ctx context.Context, giftID int64, slug string, now int) (int64, error) {
	if giftID == 0 {
		if err := s.db.QueryRow(ctx, `SELECT gift_id FROM star_gift_catalog_revisions WHERE auction AND auction_slug=$1 ORDER BY id DESC LIMIT 1`, slug).Scan(&giftID); err != nil {
			return 0, domain.ErrStarGiftAuctionUnavailable
		}
	}
	gift, found, err := NewStarGiftStore(s.db).CatalogGift(ctx, giftID)
	if err != nil || !found || !gift.Auction || gift.GiftsPerRound <= 0 || gift.AuctionSlug == "" {
		return 0, domain.ErrStarGiftAuctionUnavailable
	}
	if slug != "" && slug != gift.AuctionSlug {
		return 0, domain.ErrStarGiftAuctionUnavailable
	}
	supply := gift.AvailabilityTotal
	if supply <= 0 {
		supply = gift.UpgradeTotal
	}
	if supply <= 0 {
		return 0, domain.ErrStarGiftAuctionUnavailable
	}
	start := gift.AuctionStartDate
	if start <= 0 {
		start = now
	}
	totalRounds := (supply + gift.GiftsPerRound - 1) / gift.GiftsPerRound
	end := start + totalRounds*starGiftAuctionRoundDuration
	status := "pending"
	currentRound := 0
	if now >= start {
		status, currentRound = "active", 1
	}
	_, err = s.db.Exec(ctx, `INSERT INTO star_gift_auctions(gift_id,slug,start_date,end_date,round_duration,gifts_per_round,
total_rounds,current_round,next_round_at,gifts_left,min_bid_amount,status)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(gift_id) DO NOTHING`, gift.ID, gift.AuctionSlug,
		start, end, starGiftAuctionRoundDuration, gift.GiftsPerRound, totalRounds, currentRound,
		start+starGiftAuctionRoundDuration, supply, maxInt64(1, gift.Stars), status)
	if err != nil {
		return 0, err
	}
	return gift.ID, nil
}

func (s *StarGiftLifecycleStore) loadStarGiftAuction(ctx context.Context, userID, giftID int64) (domain.StarGiftAuction, error) {
	gift, found, err := NewStarGiftStore(s.db).CatalogGift(ctx, giftID)
	if err != nil || !found {
		return domain.StarGiftAuction{}, domain.ErrStarGiftAuctionUnavailable
	}
	out := domain.StarGiftAuction{Gift: gift}
	var status string
	var roundDurationsRaw, roundExtendsRaw []byte
	if err := s.db.QueryRow(ctx, `SELECT version,start_date,end_date,min_bid_amount,next_round_at,last_gift_num,gifts_left,
current_round,total_rounds,round_duration,status,round_durations,raise_min_bid,round_extends,round_extend_seconds FROM star_gift_auctions WHERE gift_id=$1`, giftID).Scan(&out.Version, &out.StartDate,
		&out.EndDate, &out.MinBidAmount, &out.NextRoundAt, &out.LastGiftNum, &out.GiftsLeft, &out.CurrentRound,
		&out.TotalRounds, &out.RoundDuration, &status, &roundDurationsRaw, &out.RaiseMinBid, &roundExtendsRaw, &out.RoundExtendSeconds); err != nil {
		return domain.StarGiftAuction{}, err
	}
	out.RoundDurations = parseAuctionRoundDurations(roundDurationsRaw)
	out.RoundExtends = parseAuctionRoundExtends(roundExtendsRaw)
	out.Finished = status == "completed" || status == "cancelled"
	// Раунд 20: граница текущего раунда для отображения пересчитывается из
	// индивидуальных длительностей — только когда jsonb задан (при NULL
	// авторитетно сохранённое next_round_at). Значение капится end_date так
	// же, как его пишет settle. Раунд 21: добавка round_extend_seconds
	// отражает продления, применённые ставками.
	if !out.Finished && out.CurrentRound >= 1 && out.CurrentRound <= out.TotalRounds && out.RoundDurations != nil {
		boundary := auctionRoundBoundary(out.StartDate, out.RoundDurations, out.CurrentRound, out.RoundDuration, out.RoundExtendSeconds)
		if boundary > out.EndDate {
			boundary = out.EndDate
		}
		out.NextRoundAt = boundary
	}
	if out.Finished {
		if err := s.db.QueryRow(ctx, `SELECT COALESCE(AVG(bid_amount)::bigint,0) FROM star_gift_auction_acquired WHERE gift_id=$1`, giftID).
			Scan(&out.AveragePrice); err != nil {
			return domain.StarGiftAuction{}, err
		}
		if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_listings l JOIN unique_star_gifts u ON u.id=l.unique_gift_id WHERE u.gift_id=$1`, giftID).
			Scan(&out.ListedCount); err != nil {
			return domain.StarGiftAuction{}, err
		}
	}
	rows, err := s.db.Query(ctx, `SELECT amount,bid_date FROM star_gift_auction_bids WHERE gift_id=$1 AND active
ORDER BY amount DESC,bid_date,bidder_user_id LIMIT 20`, giftID)
	if err != nil {
		return domain.StarGiftAuction{}, err
	}
	for rows.Next() {
		var level domain.StarGiftAuctionBidLevel
		level.Pos = len(out.BidLevels) + 1
		if err := rows.Scan(&level.Amount, &level.Date); err != nil {
			rows.Close()
			return domain.StarGiftAuction{}, err
		}
		out.BidLevels = append(out.BidLevels, level)
	}
	rows.Close()
	// Раунд 20 (исправление): сумма лидирующей активной ставки + 1 — нижняя
	// граница следующей ставки в режиме повышения. Минимум в состоянии
	// поднимается до «топ-1 + 1» и здесь, а не только при записи ставки:
	// клиент в поле «Минимальная цена» видит корректное значение сразу.
	// Раунд 21: в режиме несменяемого минимума (raise_min_bid=false)
	// отображается само административное значение min_bid_amount.
	if len(out.BidLevels) > 0 {
		out.TopBidAmount = out.BidLevels[0].Amount
		out.MinBidAmount = auctionBidFloorMode(out.RaiseMinBid, out.MinBidAmount, out.TopBidAmount)
	}
	topRows, err := s.db.Query(ctx, `SELECT bidder_user_id FROM star_gift_auction_bids WHERE gift_id=$1 AND active
ORDER BY amount DESC,bid_date,bidder_user_id LIMIT 3`, giftID)
	if err != nil {
		return domain.StarGiftAuction{}, err
	}
	for topRows.Next() {
		var id int64
		if err := topRows.Scan(&id); err != nil {
			topRows.Close()
			return domain.StarGiftAuction{}, err
		}
		out.TopBidders = append(out.TopBidders, id)
	}
	topRows.Close()
	var peerType string
	var active bool
	err = s.db.QueryRow(ctx, `SELECT returned,active,amount,bid_date,recipient_peer_type,recipient_peer_id,acquired_count
FROM star_gift_auction_bids WHERE gift_id=$1 AND bidder_user_id=$2`, giftID, userID).Scan(&out.UserState.Returned,
		&active, &out.UserState.BidAmount, &out.UserState.BidDate, &peerType, &out.UserState.BidPeer.ID, &out.UserState.AcquiredCount)
	if err == nil {
		out.UserState.BidActive = active
		if active {
			// Активная ставка: персональный пол — «своя ставка + 1»
			// (повторная ставка строго выше собственной).
			out.UserState.BidPeer.Type = domain.PeerType(peerType)
			out.UserState.MinBidAmount = out.UserState.BidAmount + 1
		} else if out.UserState.Returned {
			// Раунд 21 (исправление): возвращённая ставка (перебита и
			// возвращена на границе раунда) НЕ поднимает персональный
			// пол: повторное участие — новая ставка с общим минимумом
			// starGiftAuctionState.min_bid_amount. bid_amount остаётся в
			// состоянии (клиент показывает его вместе с returned), но
			// сервер не считает её препятствием для новой ставки.
			out.UserState.BidPeer.Type = domain.PeerType(peerType)
			out.UserState.MinBidAmount = 0
		} else {
			out.UserState.BidAmount, out.UserState.BidDate, out.UserState.BidPeer = 0, 0, domain.Peer{}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftAuction{}, err
	}
	return out, nil
}

func (s *StarGiftLifecycleStore) loadAuctionBidReplay(ctx context.Context, userID, formID, giftID int64) (domain.StarsBalance, bool, error) {
	var storedGiftID, balance int64
	err := s.db.QueryRow(ctx, `SELECT gift_id,balance_after FROM star_gift_auction_bid_payments WHERE user_id=$1 AND form_id=$2`, userID, formID).
		Scan(&storedGiftID, &balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarsBalance{}, false, nil
	}
	if err != nil {
		return domain.StarsBalance{}, false, err
	}
	if storedGiftID != giftID {
		return domain.StarsBalance{}, false, domain.ErrStarGiftAuctionUnavailable
	}
	return domain.StarsBalance{UserID: userID, Balance: balance}, true, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// auctionBidFloor — нижняя граница следующей ставки аукциона (раунд 20,
// исправление): максимум из сохранённого min_bid_amount и «топ-1 + 1».
// Требование поведения: если лидирующая ставка 600 звёзд, следующая допустимая
// ставка — 601; равная топ-1 ставка отклоняется. Пока активных ставок нет
// (topBid == 0), действует исходный min_bid_amount без надбавки.
func auctionBidFloor(minBidAmount, topBid int64) int64 {
	if topBid > 0 && topBid+1 > minBidAmount {
		return topBid + 1
	}
	return minBidAmount
}

// auctionBidFloorMode — нижняя граница следующей ставки с учётом режима
// raise_min_bid (раунд 21). Режим повышения (true) — auctionBidFloor:
// «топ-1 + 1». Несменяемый минимум (false) — чистый min_bid_amount: ставки
// ниже лидирующей допускаются и переносятся на следующие раунды, значение
// минимума не меняется сервером.
func auctionBidFloorMode(raiseMinBid bool, minBidAmount, topBid int64) int64 {
	if !raiseMinBid {
		return minBidAmount
	}
	return auctionBidFloor(minBidAmount, topBid)
}

// auctionTopBidderIDs — упорядоченный список bidder_user_id первых limit
// активных ставок (amount DESC, bid_date, bidder_user_id). limit <= 0 даёт nil.
func auctionTopBidderIDs(ctx context.Context, tx pgx.Tx, giftID int64, limit int) []int64 {
	if limit <= 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT bidder_user_id FROM star_gift_auction_bids WHERE gift_id=$1 AND active
ORDER BY amount DESC,bid_date,bidder_user_id LIMIT $2`, giftID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return nil
	}
	return ids
}

// auctionTopSetsEqual сравнивает состав топа до и после ставки.
func auctionTopSetsEqual(before, after []int64) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if before[i] != after[i] {
			return false
		}
	}
	return true
}
