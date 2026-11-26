package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// Раунд 47 (статистика пополнения баланса): getStarsRevenueStats для СВОЕГО
// аккаунта отдаёт реальные числа — «Общий баланс» (current_balance),
// «Всего поступлений» (overall_revenue = сумма кредитных проводок),
// «Доступно для вывода» = 0 (вывод имплементируется позже).
func TestRound47PersonalStarsRevenueStatsSelf(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000047)
	users := memory.NewUserStore()
	user, err := users.Create(context.Background(), domain.User{AccessHash: 4747, Phone: "15550000471", FirstName: "Stats User"})
	if err != nil {
		t.Fatal(err)
	}
	_ = user
	stars := appstars.NewService(memory.NewStarsStore())
	r := New(Config{}, Deps{Users: appusers.NewService(users), Stars: stars}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})

	if _, err := stars.CreditFlags(ctx, 1000000047, 250, domain.StarsReasonGift, domain.Peer{Type: domain.PeerTypeUser, ID: 1000000048}, domain.StarsLedgerFlags{}); err != nil {
		t.Fatal(err)
	}
	if _, err := stars.CreditFlags(ctx, 1000000047, 100, domain.StarsReasonTopup, domain.Peer{}, domain.StarsLedgerFlags{}); err != nil {
		t.Fatal(err)
	}
	if _, err := stars.DebitFlags(ctx, 1000000047, 50, domain.StarsReasonPremium, domain.Peer{}, domain.StarsLedgerFlags{}, "", ""); err != nil {
		t.Fatal(err)
	}

	revenue, err := r.onPaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("self revenue stats: %v", err)
	}
	// Баланс: 250 + 100 - 50 + (стартовый грант 1000, если configured).
	// Проверяем не абсолютное число, а соответствие леджеру.
	bal, err := stars.GetBalance(ctx, 1000000047)
	if err != nil {
		t.Fatal(err)
	}
	current, ok := revenue.Status.CurrentBalance.(*tg.StarsAmount)
	if !ok || current.Amount != bal.Balance {
		t.Fatalf("self revenue current = %+v, want balance %d", revenue.Status.CurrentBalance, bal.Balance)
	}
	overall, ok := revenue.Status.OverallRevenue.(*tg.StarsAmount)
	if !ok || overall.Amount != bal.Balance+50 || overall.Amount <= 0 {
		t.Fatalf("self revenue overall = %+v, want gross credits %d", revenue.Status.OverallRevenue, bal.Balance+50)
	}
	if revenue.Status.WithdrawalEnabled {
		t.Fatalf("self revenue withdrawal_enabled, want false")
	}
	if avail, ok := revenue.Status.AvailableBalance.(*tg.StarsAmount); !ok || avail.Amount != 0 {
		t.Fatalf("self revenue available = %+v, want 0 (вывод не имплементирован)", revenue.Status.AvailableBalance)
	}
}

// Раунд 48 (приватность баланса бота): Stars/TON баланс ЧУЖОГО бота видит
// только владелец (OwnsBot). Сторонний аккаунт получает PEER_ID_INVALID на
// getStarsStatus/getStarsTransactions и нейтральный bounded-ответ на
// getStarsRevenueStats; владелец видит реальные числа (вкладка баланса бота
// в профиле работает, как в раунде 47).
func TestRound48BotStarsStatsOwnerOnly(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000047)
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(users, botStore, messages)
	owner, err := users.Create(context.Background(), domain.User{AccessHash: 4847, Phone: "15550000480", FirstName: "Bot Owner"})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := bots.CreateBot(context.Background(), owner.ID, "Stats Bot", "stats_round48_bot")
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(context.Background(), domain.User{AccessHash: 4849, Phone: "15550000482", FirstName: "Other Human"})
	if err != nil {
		t.Fatal(err)
	}
	stars := appstars.NewService(memory.NewStarsStore())
	r := New(Config{}, Deps{
		Users: appusers.NewService(users),
		Stars: stars,
		Bots:  bots,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})
	if _, err := stars.CreditFlags(context.Background(), bot.ID, 500, domain.StarsReasonGift, domain.Peer{Type: domain.PeerTypeUser, ID: 1000000047}, domain.StarsLedgerFlags{}); err != nil {
		t.Fatal(err)
	}
	bal, err := stars.GetBalance(context.Background(), bot.ID)
	if err != nil {
		t.Fatal(err)
	}

	botPeer := &tg.InputPeerUser{UserID: bot.ID, AccessHash: bot.AccessHash}

	// 1) Сторонний аккаунт: реальные числа недоступны.
	if _, err := r.onPaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: botPeer}); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("other user bot status err = %v, want PEER_ID_INVALID", err)
	}
	if _, err := r.onPaymentsGetStarsTransactions(ctx, &tg.PaymentsGetStarsTransactionsRequest{Peer: botPeer}); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("other user bot transactions err = %v, want PEER_ID_INVALID", err)
	}
	neutral, err := r.onPaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: botPeer})
	if err != nil {
		t.Fatalf("other user bot revenue stats: %v", err)
	}
	if current, ok := neutral.Status.CurrentBalance.(*tg.StarsAmount); !ok || current.Amount != 0 {
		t.Fatalf("other user bot revenue current = %+v, want neutral 0", neutral.Status.CurrentBalance)
	}

	// 2) Владелец: реальные числа и транзакции доступны.
	ownerCtx := WithUserID(context.Background(), owner.ID)
	ownerRevenue, err := r.onPaymentsGetStarsRevenueStats(ownerCtx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: botPeer})
	if err != nil {
		t.Fatalf("owner bot revenue stats: %v", err)
	}
	if current, ok := ownerRevenue.Status.CurrentBalance.(*tg.StarsAmount); !ok || current.Amount != bal.Balance || current.Amount <= 0 {
		t.Fatalf("owner bot revenue current = %+v, want bot balance %d", ownerRevenue.Status.CurrentBalance, bal.Balance)
	}
	ownerStatus, err := r.onPaymentsGetStarsStatus(ownerCtx, &tg.PaymentsGetStarsStatusRequest{Peer: botPeer})
	if err != nil {
		t.Fatalf("owner bot stars status: %v", err)
	}
	if amount, ok := ownerStatus.Balance.(*tg.StarsAmount); !ok || amount.Amount != bal.Balance {
		t.Fatalf("owner bot stars status balance = %+v, want %d", ownerStatus.Balance, bal.Balance)
	}

	// 3) Чужой обычный пользователь — приватность сохраняется (раунд 47).
	otherPeer := &tg.InputPeerUser{UserID: other.ID, AccessHash: other.AccessHash}
	if _, err := r.onPaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: otherPeer}); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("other user status err = %v, want PEER_ID_INVALID", err)
	}
}

// Раунд 48 (график статистики баланса): revenue_graph несёт statsGraph с
// JSON StackBarChartData — колонки x (UTC-дни в миллисекундах, все дни от
// первого дня с поступлениями до сегодня) и y0 (сумма поступлений за день).
func TestRound48StarsRevenueGraph(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000047)
	users := memory.NewUserStore()
	user, err := users.Create(context.Background(), domain.User{AccessHash: 4750, Phone: "15550000483", FirstName: "Graph User"})
	if err != nil {
		t.Fatal(err)
	}
	_ = user
	starsStore := memory.NewStarsStore()
	now := time.Unix(1_800_000_000, 0) // 2027-01-15 UTC
	stars := appstars.NewService(starsStore, appstars.WithClock(func() time.Time { return now }))
	r := New(Config{}, Deps{Users: appusers.NewService(users), Stars: stars}, zaptest.NewLogger(t), fixedClock{now: now})

	today := int(now.Unix()) - int(now.Unix())%86400
	yesterday := today - 86400
	// Проводки кладём прямо в стор с явными датами: график обязан покрыть
	// все дни от первого дня с поступлениями до сегодня.
	if _, err := starsStore.Credit(context.Background(), 1000000047, 250, domain.StarsReasonGift, domain.Peer{}, today, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := starsStore.Credit(context.Background(), 1000000047, 100, domain.StarsReasonTopup, domain.Peer{}, yesterday, "", ""); err != nil {
		t.Fatal(err)
	}

	revenue, err := r.onPaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("self revenue stats: %v", err)
	}
	graph, ok := revenue.RevenueGraph.(*tg.StatsGraph)
	if !ok {
		t.Fatalf("revenue graph = %T, want *tg.StatsGraph", revenue.RevenueGraph)
	}
	var payload struct {
		Columns []json.RawMessage `json:"columns"`
	}
	if err := json.Unmarshal([]byte(graph.JSON.Data), &payload); err != nil {
		t.Fatalf("graph json: %v", err)
	}
	if len(payload.Columns) != 2 {
		t.Fatalf("graph columns = %d, want 2", len(payload.Columns))
	}
	var xRow []any
	if err := json.Unmarshal(payload.Columns[0], &xRow); err != nil {
		t.Fatalf("x row: %v", err)
	}
	if len(xRow) < 3 || xRow[0] != "x" {
		t.Fatalf("x row = %v, want header 'x' + дни", xRow)
	}
	if len(xRow)-1 < 2 {
		t.Fatalf("x days = %d, want >= 2 (вчера и сегодня)", len(xRow)-1)
	}
	var yRow []any
	if err := json.Unmarshal(payload.Columns[1], &yRow); err != nil {
		t.Fatalf("y row: %v", err)
	}
	if len(yRow) != len(xRow) || yRow[0] != "y0" {
		t.Fatalf("y row = %v, want header 'y0' + те же %d дней", yRow, len(xRow)-1)
	}
	// Сумма поступлений: 250 (сегодня) + 100 (вчера) + стартовый грант 1000
	// (GetBalance в обработчике кладёт грант «сегодня») = 1350.
	var sum float64
	for _, v := range yRow[1:] {
		if n, ok := v.(float64); ok {
			sum += n
		}
	}
	if sum != 1350 {
		t.Fatalf("graph total = %v, want 1350", sum)
	}
	// x — миллисекунды UTC-дней; диапазон ровно вчера..сегодня.
	firstDay, ok := xRow[1].(float64)
	if !ok || int64(firstDay) != int64(yesterday)*1000 {
		t.Fatalf("first x = %v, want %d", xRow[1], int64(yesterday)*1000)
	}
	lastDay, ok := xRow[len(xRow)-1].(float64)
	if !ok || int64(lastDay) != int64(today)*1000 {
		t.Fatalf("last x = %v, want %d", xRow[len(xRow)-1], int64(today)*1000)
	}
}

// Раунд 47 (задача ④): Processing/Failed проецируются в starsTransaction
// pending (flags.4) / failed (flags.6) — клиент подписывает строку
// «В обработке»/«Ошибка» по этим флагам.
func TestRound47TransactionPendingFailedProjection(t *testing.T) {
	in := []domain.StarsTransaction{
		{ID: 1, Amount: 100, Date: 10, Reason: domain.StarsReasonGift, Processing: true},
		{ID: 2, Amount: -30, Date: 11, Reason: domain.StarsReasonPremium, Failed: true},
		{ID: 3, Amount: 50, Date: 12, Reason: domain.StarsReasonTopup},
	}
	out := tgStarsTransactions(in, starsGiftPayloads{})
	if len(out) != 3 {
		t.Fatalf("projection len = %d", len(out))
	}
	if !out[0].Pending || out[0].Failed {
		t.Fatalf("txn1 pending/failed = %v/%v, want true/false", out[0].Pending, out[0].Failed)
	}
	if out[1].Pending || !out[1].Failed {
		t.Fatalf("txn2 pending/failed = %v/%v, want false/true", out[1].Pending, out[1].Failed)
	}
	if out[2].Pending || out[2].Failed {
		t.Fatalf("txn3 pending/failed = %v/%v, want false/false", out[2].Pending, out[2].Failed)
	}
	tons := tgTonTransactions([]domain.TonTransaction{
		{ID: 4, Amount: 900, Date: 13, Reason: domain.StarsReasonGift, Processing: true},
	}, starsGiftPayloads{})
	if len(tons) != 1 || !tons[0].Pending || tons[0].Failed {
		t.Fatalf("ton projection = %+v, want pending only", tons)
	}
}
