package rpc

import (
        "context"
        "fmt"
        "strings"

        "github.com/iamxvbaba/td/tg"

        "telesrv/internal/domain"
)

// payments.getStarsRevenueStats выручка: дневные серии из леджера + statsGraph JSON.
//
// Контракт JSON зафиксирован клиентским парсером (DrKLO ChartData /
// StackBarChartData) и проверен на экране монетизации:
//
//      columns: [["x", ms...], ["y0", v...]] — x в миллисекундах суток, y0 для
//               ton=true в нанотонах, для ton=false в звёздах (обе — int64);
//      colors:  {"y0": "blue#54bbf1"} — до '#': суффикс theme-key
//               (statisticChartLine_<name>), после — цвет светлой темы. Клиент
//               красит TON-график («Вознаграждение за рекламу») именно по этому
//               полю: без colors линия падает в чёрный, а светло-жёлтый цвет —
//               признак чужой палитры. Звёздный график клиент перекрашивает в
//               key_statisticChartLine_golden принудительно, поэтому цвет в JSON
//               там косметический;
//      names:   {"y0": ...} — имя линии в тултипе;
//      yTickFormatter / yTooltipFormatter: строка, содержащая "TON" (или валюту
//               звёзд) — включает денежные форматтеры оси и тултипа; без неё
//               клиент печатает «сырые» int64.
//
// revenue_graph — обязательное поле конструктора
// payments.starsRevenueStats#6c207376: Encoder падает с
// "field revenue_graph is nil" и весь RPC публикуется как INTERNAL, поэтому
// пустые данные дают нулевую 30-дневную серию, а не statsGraphError
// (statsGraphError клиент парсит в «графика нет» — StatisticActivity
// createViewData возвращает null, секция не рисуется вовсе).

const (
        // revenueStatsWindowDays — число дневных столбцов в серии (последний —
        // текущие UTC-сутки).
        revenueStatsWindowDays = 30
        // revenueStatsMaxRowsScan — потолок прочитанных строк леджера за один
        // запрос статистики (keyset-пагинация новее → старше).
        revenueStatsMaxRowsScan = 2000
        // revenueStatsTonUsdRate — курс GRAM→USD для usd_rate и конверсий «≈$».
        // GRAM ≈ $1: ровно такое соотношение видно на эталонном экране
        // официального сервера (1.80 GRAM ≈ 1.80 $).
        revenueStatsTonUsdRate = 1.0
        // revenueStatsStarsUsdRate — классический курс Telegram Stars.
        revenueStatsStarsUsdRate = 0.013
)

// revenueStatsStartDay возвращает номер UTC-суток начала 30-дневного окна.
func revenueStatsStartDay(nowUnix int64) int64 {
        return nowUnix/86400 - (revenueStatsWindowDays - 1)
}

// revenueStatsGraph строит statsGraph#8ea464b6 с дневной серией.
func revenueStatsGraph(series []int64, startDay int64, ton bool) *tg.StatsGraph {
        color := "gold#f5a623"
        formatter := "XTR"
        name := "Stars"
        if ton {
                color = "blue#54bbf1"
                formatter = "TON"
                name = "TON"
        }
        var b strings.Builder
        b.WriteString(`{"columns":[["x"`)
        dayUnix := startDay * 86400
        for i := 0; i < len(series); i++ {
                fmt.Fprintf(&b, ",%d", dayUnix*1000)
                dayUnix += 86400
        }
        b.WriteString(`],["y0"`)
        for _, v := range series {
                fmt.Fprintf(&b, ",%d", v)
        }
        b.WriteString(`]],"names":{"y0":"`)
        b.WriteString(name)
        b.WriteString(`"},"colors":{"y0":"`)
        b.WriteString(color)
        b.WriteString(`"},"yTickFormatter":"`)
        b.WriteString(formatter)
        b.WriteString(`","yTooltipFormatter":"`)
        b.WriteString(formatter)
        b.WriteString(`"}`)
        return &tg.StatsGraph{JSON: tg.DataJSON{Data: b.String()}}
}

// revenueStatsPayload собирает итоговый payments.starsRevenueStats: статус с
// балансами нужного типа (starsAmount против starsTonAmount — по этому типу
// клиент тоже отличает валюту) и ненулевой график. WithdrawalEnabled остаётся
// снятым: внешней выплаты нет, балансы приравнены к пожизненной выручке.
func revenueStatsPayload(balance int64, series []int64, startDay int64, ton bool) *tg.PaymentsStarsRevenueStats {
        var amount tg.StarsAmountClass = &tg.StarsAmount{Amount: balance}
        usdRate := revenueStatsStarsUsdRate
        if ton {
                amount = &tg.StarsTonAmount{Amount: balance}
                usdRate = revenueStatsTonUsdRate
        }
        return &tg.PaymentsStarsRevenueStats{
                RevenueGraph: revenueStatsGraph(series, startDay, ton),
                Status: tg.StarsRevenueStatus{
                        CurrentBalance:   amount,
                        AvailableBalance: amount,
                        OverallRevenue:   amount,
                },
                UsdRate: usdRate,
        }
}

// revenueLedgerPage — одна страница любого леджера выручки в терминах
// агрегатора: значения суммы и даты плюс keyset-курсор.
type revenueLedgerPage[T any] struct {
        Transactions []T
        NextOffset   string
        Amount       func(T) int64
        Date         func(T) int
}

// collectRevenueDailySeries агрегирует кредиты леджера по UTC-суткам окна.
// Пагинация keyset идёт новее → старше; чтение останавливается, как только
// страница целиком ушла старше окна, курсор кончился или исчерпан потолок
// строк. Дебеты (Amount <= 0) не влияют на выручку — она накопительная.
func collectRevenueDailySeries[T any](ctx context.Context, fetch func(domain.StarsTransactionQuery) (revenueLedgerPage[T], error), startDay int64) ([]int64, error) {
        buckets := make([]int64, revenueStatsWindowDays)
        query := domain.StarsTransactionQuery{Limit: domain.MaxStarsTransactionsLimit}
        scanned := 0
        for {
                page, err := fetch(query)
                if err != nil {
                        return nil, err
                }
                if len(page.Transactions) == 0 {
                        break
                }
                for _, txn := range page.Transactions {
                        amount, date := page.Amount(txn), page.Date(txn)
                        if amount <= 0 || date <= 0 {
                                continue
                        }
                        day := date/86400 - int(startDay)
                        if day >= 0 && day < len(buckets) {
                                buckets[day] += amount
                        }
                }
                scanned += len(page.Transactions)
                if page.NextOffset == "" || scanned >= revenueStatsMaxRowsScan {
                        break
                }
                if oldest := page.Date(page.Transactions[len(page.Transactions)-1]); oldest > 0 && oldest/86400 < int(startDay) {
                        break
                }
                query.Offset = page.NextOffset
        }
        return buckets, nil
}

// tonRevenueDailySeries агрегирует кредиты TON-леджера пользователя или бота
// (нанотоны, единицы хранилища).
func (r *Router) tonRevenueDailySeries(ctx context.Context, ownerUserID int64, startDay int64) ([]int64, error) {
        if r.deps.Gifts == nil {
                return make([]int64, revenueStatsWindowDays), nil
        }
        return collectRevenueDailySeries(ctx, func(query domain.StarsTransactionQuery) (revenueLedgerPage[domain.TonTransaction], error) {
                page, err := r.deps.Gifts.TonTransactions(ctx, ownerUserID, query)
                if err != nil {
                        return revenueLedgerPage[domain.TonTransaction]{}, err
                }
                return revenueLedgerPage[domain.TonTransaction]{
                        Transactions: page.Transactions,
                        NextOffset:   page.NextOffset,
                        Amount:       func(t domain.TonTransaction) int64 { return t.Amount },
                        Date:         func(t domain.TonTransaction) int { return t.Date },
                }, nil
        }, startDay)
}

// starsRevenueDailySeries — звёздный аналог (значения в звёздах).
func (r *Router) starsRevenueDailySeries(ctx context.Context, ownerUserID int64, startDay int64) ([]int64, error) {
        if r.deps.Stars == nil {
                return make([]int64, revenueStatsWindowDays), nil
        }
        return collectRevenueDailySeries(ctx, func(query domain.StarsTransactionQuery) (revenueLedgerPage[domain.StarsTransaction], error) {
                page, err := r.deps.Stars.ListTransactions(ctx, ownerUserID, query)
                if err != nil {
                        return revenueLedgerPage[domain.StarsTransaction]{}, err
                }
                return revenueLedgerPage[domain.StarsTransaction]{
                        Transactions: page.Transactions,
                        NextOffset:   page.NextOffset,
                        Amount:       func(t domain.StarsTransaction) int64 { return t.Amount },
                        Date:         func(t domain.StarsTransaction) int { return t.Date },
                }, nil
        }, startDay)
}

// channelTonRevenueDailySeries / channelStarsRevenueDailySeries — те же серии
// для канального леджера (владелец канала/мегагруппы).
func (r *Router) channelTonRevenueDailySeries(ctx context.Context, channelID int64, startDay int64) ([]int64, error) {
        ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
        if !ok {
                return make([]int64, revenueStatsWindowDays), nil
        }
        return collectRevenueDailySeries(ctx, func(query domain.StarsTransactionQuery) (revenueLedgerPage[domain.TonTransaction], error) {
                page, err := ledger.ChannelTonTransactions(ctx, channelID, query)
                if err != nil {
                        return revenueLedgerPage[domain.TonTransaction]{}, err
                }
                return revenueLedgerPage[domain.TonTransaction]{
                        Transactions: page.Transactions,
                        NextOffset:   page.NextOffset,
                        Amount:       func(t domain.TonTransaction) int64 { return t.Amount },
                        Date:         func(t domain.TonTransaction) int { return t.Date },
                }, nil
        }, startDay)
}

func (r *Router) channelStarsRevenueDailySeries(ctx context.Context, channelID int64, startDay int64) ([]int64, error) {
        ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
        if !ok {
                return make([]int64, revenueStatsWindowDays), nil
        }
        return collectRevenueDailySeries(ctx, func(query domain.StarsTransactionQuery) (revenueLedgerPage[domain.StarsTransaction], error) {
                page, err := ledger.ChannelStarsTransactions(ctx, channelID, query)
                if err != nil {
                        return revenueLedgerPage[domain.StarsTransaction]{}, err
                }
                return revenueLedgerPage[domain.StarsTransaction]{
                        Transactions: page.Transactions,
                        NextOffset:   page.NextOffset,
                        Amount:       func(t domain.StarsTransaction) int64 { return t.Amount },
                        Date:         func(t domain.StarsTransaction) int { return t.Date },
                }, nil
        }, startDay)
}
