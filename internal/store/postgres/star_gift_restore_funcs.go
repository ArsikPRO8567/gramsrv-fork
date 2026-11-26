package postgres

// Функции административного управления аукционом и освобождения слота
// улучшения выделены в отдельный файл: большие store-файлы
// (star_gift_lifecycle.go / star_gift_craft_auction.go) переписывались
// загрузками через веб-интерфейс и теряли эти определения; отдельный
// небольшой файл переживает такие перезаписи без потерь.

import (
	"context"
	"fmt"
	"encoding/json"
	"strings"
	"time"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
)

// releaseUpgradeSlotOnConvertTx (раунд 41/42) — конвертация редкого подарка,
// имеющего улучшения (у каталога есть активная опубликованная коллекционная
// ревизия), уменьшает САПЛАЙ УЛУЧШЕНИЙ (star_gift_collectible_revisions.
// supply_total) на 1: экземпляр подарка уничтожен и никогда не станет
// уникальным коллекционным, поэтому пул возможных улучшений сжимается.
// Клиенты и админка читают supply_total как «supply» пула улучшений
// (gift.upgrade_total / collectibles preview supply_total), поэтому раунд 41
// с декрементом issued (освобождавший слот, но не менявший видимый supply)
// проблему не закрывал — теперь уменьшается именно supply_total.
//
// Гварды: уменьшаем только пока supply_total > issued (нельзя сжать пул ниже
// уже выпущенных коллекционных — CHECK issued <= supply_total), транзакция
// помечается локальной настройкой gramsrv.star_gift_release_slot = 'on'
// (set_config с is_local=true действует до конца транзакции и автоматически
// сбрасывается при фиксации/откате; миграция 0199 разрешает supply_total = 0).
// У каталога без пула улучшений (или с черновой ревизией) конвертация ничего
// не меняет.
func releaseUpgradeSlotOnConvertTx(ctx context.Context, tx pgx.Tx, giftID int64) error {
        if giftID <= 0 {
                return nil
        }
        if _, err := tx.Exec(ctx, `SELECT set_config('gramsrv.star_gift_release_slot', 'on', true)`); err != nil {
                return fmt.Errorf("mark gift-conversion transaction: %w", err)
        }
        if _, err := tx.Exec(ctx, `
UPDATE star_gift_collectible_revisions r
SET supply_total = r.supply_total - 1
FROM star_gift_catalog c
WHERE c.gift_id = $1
  AND r.id = c.collectible_revision_id
  AND r.status = 'published'
  AND r.supply_total > r.issued`, giftID); err != nil {
                return fmt.Errorf("release collectible upgrade slot on convert: %w", err)
        }
        return nil
}


// SetStarGiftAuctionSettings (раунд 34/36) применяет административные правки
// рабочего состояния аукциона: длительность раундов (общая и индивидуальные
// jsonb), минимальную ставку, режим повышения, даты, количество раундов,
// остаток подарков и последний выпущенный номер. Строка создаётся лениво,
// как в StarGiftAuctionState. После правки границы next_round_at/end_date
// ПЕРЕСЧИТЫВАЮТСЯ (исправление «время раундов не влияет на итоговый
// вариант»): next_round_at — граница конца текущего раунда (start + сумма
// индивидуальных длительностей 1..current_round + продления), end_date —
// ручное значение из той же команды либо производное (граница текущего
// раунда + оставшиеся раунды). version растёт ровно на 1 за вызов.
// Валидация (статус, jsonb, неположительные/отрицательные значения)
// выполняется до любых записей — чистая domain.ErrStarGiftInvalid.
func (s *StarGiftLifecycleStore) SetStarGiftAuctionSettings(ctx context.Context, write domain.StarGiftAuctionRuntimeWrite) (domain.StarGiftAuction, error) {
        if s == nil || s.db == nil || write.GiftID <= 0 {
                return domain.StarGiftAuction{}, domain.ErrStarGiftInvalid
        }
        status := strings.ToLower(strings.TrimSpace(write.Status))
        if status != "" && status != "pending" && status != "active" && status != "completed" && status != "cancelled" {
                return domain.StarGiftAuction{}, domain.ErrStarGiftInvalid
        }
        for _, raw := range []string{write.RoundDurationsJSON, write.RoundExtendsJSON} {
                trimmed := strings.TrimSpace(raw)
                if trimmed == "" || trimmed == "null" {
                        continue
                }
                if !json.Valid([]byte(trimmed)) {
                        return domain.StarGiftAuction{}, domain.ErrStarGiftInvalid
                }
        }
        if write.RoundDuration != nil && *write.RoundDuration <= 0 ||
                write.MinBidAmount != nil && *write.MinBidAmount <= 0 ||
                write.StartDate != nil && *write.StartDate <= 0 ||
                write.EndDate != nil && *write.EndDate <= 0 ||
                write.TotalRounds != nil && *write.TotalRounds <= 0 ||
                write.GiftsLeft != nil && *write.GiftsLeft < 0 ||
                write.LastGiftNum != nil && *write.LastGiftNum < 0 {
                return domain.StarGiftAuction{}, domain.ErrStarGiftInvalid
        }
        now := int(time.Now().Unix())
        if _, err := s.ensureStarGiftAuction(ctx, write.GiftID, "", now); err != nil {
                return domain.StarGiftAuction{}, err
        }
        err := withTx(ctx, s.db, "set star gift auction settings", func(tx pgx.Tx) error {
                var startDate, endDate, currentRound, nextRoundAt, totalRounds, roundDuration, roundExtendSeconds, giftsLeft int
                var lastGiftNum int64
                var minBidAmount int64
                var statusText string
                var raiseMinBid bool
                var roundDurationsRaw, roundExtendsRaw []byte
                if err := tx.QueryRow(ctx, `SELECT start_date,end_date,min_bid_amount,status,current_round,next_round_at,
raise_min_bid,round_extends,round_extend_seconds,total_rounds,round_duration,round_durations,gifts_left,last_gift_num
FROM star_gift_auctions WHERE gift_id=$1 FOR UPDATE`, write.GiftID).
                        Scan(&startDate, &endDate, &minBidAmount, &statusText, &currentRound, &nextRoundAt,
                                &raiseMinBid, &roundExtendsRaw, &roundExtendSeconds, &totalRounds, &roundDuration, &roundDurationsRaw,
                                &giftsLeft, &lastGiftNum); err != nil {
                        return err
                }
                if write.RoundDuration != nil {
                        roundDuration = *write.RoundDuration
                }
                if write.MinBidAmount != nil {
                        minBidAmount = *write.MinBidAmount
                }
                if write.RaiseMinBid != nil {
                        raiseMinBid = *write.RaiseMinBid
                }
                if write.StartDate != nil {
                        startDate = *write.StartDate
                }
                if write.TotalRounds != nil {
                        totalRounds = *write.TotalRounds
                }
                if write.GiftsLeft != nil {
                        giftsLeft = *write.GiftsLeft
                }
                if write.LastGiftNum != nil {
                        lastGiftNum = int64(*write.LastGiftNum)
                }
                if status != "" {
                        statusText = status
                }
                // jsonb-поля: пустая строка = не менять (оставить сохранённое),
                // "null" = NULL (отключить), иначе валидный json-объект.
                roundDurationsChanged, roundExtendsChanged := false, false
                var roundDurationsParam, roundExtendsParam any
                if raw := strings.TrimSpace(write.RoundDurationsJSON); raw != "" {
                        roundDurationsChanged = true
                        if raw != "null" {
                                roundDurationsParam = []byte(raw)
                        }
                }
                if raw := strings.TrimSpace(write.RoundExtendsJSON); raw != "" {
                        roundExtendsChanged = true
                        if raw != "null" {
                                roundExtendsParam = []byte(raw)
                        }
                }
                if roundDurationsChanged {
                        if bytes, ok := roundDurationsParam.([]byte); ok {
                                roundDurationsRaw = bytes
                        } else {
                                roundDurationsRaw = nil
                        }
                }
                // Пересчёт границ под новые длительности (раунд 34).
                durations := parseAuctionRoundDurations(roundDurationsRaw)
                boundaryRound := currentRound
                if boundaryRound < 1 {
                        boundaryRound = 1
                }
                nextRoundAt = auctionRoundBoundary(startDate, durations, boundaryRound, roundDuration, roundExtendSeconds)
                if write.EndDate != nil {
                        // Ручной end_date той же команды приоритетнее производного.
                        endDate = *write.EndDate
                } else {
                        endDate = derivedAuctionEnd(durations, boundaryRound, totalRounds, roundDuration, nextRoundAt)
                }
                if _, err := tx.Exec(ctx, `UPDATE star_gift_auctions SET start_date=$2, end_date=$3, min_bid_amount=$4,
raise_min_bid=$5, total_rounds=$6, round_duration=$7, gifts_left=$8, last_gift_num=$9, status=$10,
round_durations = CASE WHEN $11 THEN $12::jsonb ELSE round_durations END,
round_extends = CASE WHEN $13 THEN $14::jsonb ELSE round_extends END,
next_round_at=$15, version=version+1, updated_at=now()
WHERE gift_id=$1`, write.GiftID, startDate, endDate, minBidAmount, raiseMinBid, totalRounds, roundDuration,
                        giftsLeft, lastGiftNum, statusText, roundDurationsChanged, roundDurationsParam,
                        roundExtendsChanged, roundExtendsParam, nextRoundAt); err != nil {
                        return err
                }
                return nil
        })
        if err != nil {
                return domain.StarGiftAuction{}, err
        }
        return s.loadStarGiftAuction(ctx, 0, write.GiftID)
}

