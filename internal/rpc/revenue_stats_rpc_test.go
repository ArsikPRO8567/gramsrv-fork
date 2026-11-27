package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// --- фикстуры ---

type revenueStatsGifts struct {
	GiftsService
	tonBalance int64
	tonTxns    []domain.TonTransaction
}

func (s *revenueStatsGifts) TonBalance(context.Context, int64) (int64, error) {
	return s.tonBalance, nil
}

func (s *revenueStatsGifts) TonTransactions(context.Context, int64, domain.StarsTransactionQuery) (domain.TonTransactionPage, error) {
	return domain.TonTransactionPage{Balance: s.tonBalance, Transactions: s.tonTxns}, nil
}

type revenueStatsBots struct {
	BotsService
	owned map[int64]bool
}

func (s *revenueStatsBots) OwnsBot(_ context.Context, _, botUserID int64) (bool, error) {
	return s.owned[botUserID], nil
}

func revenueStatsGraphJSON(t *testing.T, graph tg.StatsGraphClass) map[string]any {
	t.Helper()
	if graph == nil {
		t.Fatal("revenue graph is nil: encoder would reject the whole response")
	}
	specific, ok := graph.(*tg.StatsGraph)
	if !ok {
		t.Fatalf("revenue graph = %T, want *tg.StatsGraph with JSON payload", graph)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(specific.JSON.Data), &payload); err != nil {
		t.Fatalf("revenue graph JSON: %v\n%s", err, specific.JSON.Data)
	}
	return payload
}

func revenueStatsColumns(t *testing.T, payload map[string]any) (xs []float64, ys []float64) {
	t.Helper()
	columns, ok := payload["columns"].([]any)
	if !ok || len(columns) != 2 {
		t.Fatalf("columns = %#v, want [x, y0]", payload["columns"])
	}
	for i, column := range columns {
		values, ok := column.([]any)
		if !ok || len(values) < 2 {
			t.Fatalf("column %d = %#v, want label + points", i, column)
		}
		out := make([]float64, 0, len(values)-1)
		for _, value := range values[1:] {
			number, ok := value.(float64)
			if !ok {
				t.Fatalf("column %d value %#v is not numeric", i, value)
			}
			out = append(out, number)
		}
		if i == 0 {
			xs = out
		} else {
			ys = out
		}
	}
	return xs, ys
}

// --- график ---

func TestRevenueStatsGraphJSONShape(t *testing.T) {
	series := []int64{0, 1_000_000_000, 2_500_000_000}
	startDay := revenueStatsStartDay(time.Now().Unix())

	ton := revenueStatsGraph(series, startDay, true)
	payload := revenueStatsGraphJSON(t, ton)
	xs, ys := revenueStatsColumns(t, payload)
	if len(xs) != len(series) || len(ys) != len(series) {
		t.Fatalf("columns len = %d/%d, want %d", len(xs), len(ys), len(series))
	}
	if xs[0] != float64(startDay*86400*1000) {
		t.Fatalf("first x = %v, want %d (millis)", xs[0], startDay*86400*1000)
	}
	if ys[2] != 2_500_000_000 {
		t.Fatalf("last y = %v, want nanoton", ys[2])
	}
	colors, ok := payload["colors"].(map[string]any)
	if !ok || colors["y0"] != "blue#54bbf1" {
		t.Fatalf("ton colors = %#v, want blue#54bbf1", payload["colors"])
	}
	if formatters := [2]any{payload["yTickFormatter"], payload["yTooltipFormatter"]}; formatters[0] != "TON" || formatters[1] != "TON" {
		t.Fatalf("ton formatters = %#v, want TON", formatters)
	}

	stars := revenueStatsGraph(series, startDay, false)
	starPayload := revenueStatsGraphJSON(t, stars)
	starColors, ok := starPayload["colors"].(map[string]any)
	if !ok || starColors["y0"] != "gold#f5a623" {
		t.Fatalf("stars colors = %#v, want gold#f5a623", starPayload["colors"])
	}
	if starPayload["yTickFormatter"] != "XTR" {
		t.Fatalf("stars formatter = %#v, want XTR", starPayload["yTickFormatter"])
	}
}

// --- ответ RPC ---

func TestOnPaymentsGetStarsRevenueStatsPersonalTon(t *testing.T) {
	const userID int64 = 1000000001
	today := time.Now().Unix() / 86400 * 86400
	gifts := &revenueStatsGifts{
		tonBalance: 2_500_000_000,
		tonTxns: []domain.TonTransaction{
			{ID: 1, Amount: 1_000_000_000, Date: int(today + 3600)},
			{ID: 2, Amount: -500, Date: int(today + 7200)},
			{ID: 3, Amount: 2_000_000_000, Date: int(today - 86400)},
		},
	}
	r := New(Config{}, Deps{Gifts: gifts}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), userID)

	req := &tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerSelf{}}
	req.SetTon(true)
	stats, err := r.onPaymentsGetStarsRevenueStats(ctx, req)
	if err != nil {
		t.Fatalf("personal ton revenue: %v", err)
	}
	if current, ok := stats.Status.CurrentBalance.(*tg.StarsTonAmount); !ok || current.Amount != 2_500_000_000 {
		t.Fatalf("ton current balance = %#v, want StarsTonAmount 2.5e9", stats.Status.CurrentBalance)
	}
	if stats.UsdRate != revenueStatsTonUsdRate {
		t.Fatalf("usd rate = %v, want %v", stats.UsdRate, revenueStatsTonUsdRate)
	}
	payload := revenueStatsGraphJSON(t, stats.RevenueGraph)
	_, ys := revenueStatsColumns(t, payload)
	todayIdx := revenueStatsWindowDays - 1
	if ys[todayIdx] != 1_000_000_000 || ys[todayIdx-1] != 2_000_000_000 {
		t.Fatalf("daily buckets = %v, want credits aggregated by day", ys)
	}
}

func TestOnPaymentsGetStarsRevenueStatsStarsGrant(t *testing.T) {
	r := starsRouter(t, 1000)
	ctx := WithUserID(context.Background(), 1000000001)
	stats, err := r.onPaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("personal stars revenue: %v", err)
	}
	if current, ok := stats.Status.CurrentBalance.(*tg.StarsAmount); !ok || current.Amount != 1000 {
		t.Fatalf("stars current balance = %#v, want StarsAmount 1000", stats.Status.CurrentBalance)
	}
	if stats.UsdRate != revenueStatsStarsUsdRate {
		t.Fatalf("usd rate = %v, want %v", stats.UsdRate, revenueStatsStarsUsdRate)
	}
	payload := revenueStatsGraphJSON(t, stats.RevenueGraph)
	_, ys := revenueStatsColumns(t, payload)
	if ys[revenueStatsWindowDays-1] != 1000 {
		t.Fatalf("today bucket = %v, want 1000", ys[revenueStatsWindowDays-1])
	}
}

func TestOnPaymentsGetStarsRevenueStatsBotOwned(t *testing.T) {
	const ownerID, botID int64 = 1000000001, 2000000003
	gifts := &revenueStatsGifts{tonBalance: 900}
	bots := &revenueStatsBots{owned: map[int64]bool{botID: true}}
	r := New(Config{}, Deps{Gifts: gifts, Bots: bots}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), ownerID)
	peer := &tg.InputPeerUser{UserID: botID, AccessHash: 777}

	req := &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer}
	req.SetTon(true)
	stats, err := r.onPaymentsGetStarsRevenueStats(ctx, req)
	if err != nil {
		t.Fatalf("bot ton revenue: %v", err)
	}
	if current, ok := stats.Status.CurrentBalance.(*tg.StarsTonAmount); !ok || current.Amount != 900 {
		t.Fatalf("bot ton balance = %#v, want real ledger balance", stats.Status.CurrentBalance)
	}
	if _, ok := stats.RevenueGraph.(*tg.StatsGraph); !ok {
		t.Fatalf("bot ton revenue graph = %#v, want statsGraph", stats.RevenueGraph)
	}

	// Не владелец получает терминальную заглушку без графика.
	strangers := &revenueStatsBots{owned: map[int64]bool{}}
	r2 := New(Config{}, Deps{Gifts: gifts, Bots: strangers}, zaptest.NewLogger(t), clock.System)
	stub, err := r2.onPaymentsGetStarsRevenueStats(ctx, req)
	if err != nil {
		t.Fatalf("stranger bot revenue: %v", err)
	}
	if _, ok := stub.RevenueGraph.(*tg.StatsGraphError); !ok {
		t.Fatalf("stranger revenue graph = %#v, want statsGraphError stub", stub.RevenueGraph)
	}
}

// --- центральная проекция ответов ---

func TestProjectResponsePeerObjectsBotCanEdit(t *testing.T) {
	const ownerID, botID int64 = 1000000001, 2000000003
	bots := &revenueStatsBots{owned: map[int64]bool{botID: true}}
	r := New(Config{}, Deps{Bots: bots}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), ownerID)

	botUser := &tg.User{ID: botID, Bot: true}
	strangerBot := &tg.User{ID: 2000000004, Bot: true}
	r.projectResponsePeerObjects(ctx, []tg.UserClass{botUser, strangerBot})
	if !botUser.GetBotCanEdit() {
		t.Fatal("owner's bot lost bot_can_edit on the response boundary")
	}
	if strangerBot.GetBotCanEdit() {
		t.Fatal("foreign bot unexpectedly carries bot_can_edit")
	}

	// Форма updates тоже покрывается: клиент перезаписывает кэш из них.
	botUser.Flags2.Unset(1)
	r.projectResponsePeerObjects(ctx, &tg.Updates{Users: []tg.UserClass{botUser}})
	if !botUser.GetBotCanEdit() {
		t.Fatal("updates-shaped response lost bot_can_edit")
	}
}

func TestProjectResponsePeerObjectsUsernamesVector(t *testing.T) {
	const userID int64 = 1000000001
	registry := newFakeUsernameRegistry()
	registry.byPeer[domain.Peer{Type: domain.PeerTypeUser, ID: userID}] = []domain.Username{
		{Username: "base", Editable: true, Active: true},
		{Username: "collectible3", CollectibleID: 11, Active: true},
	}
	r := New(Config{}, Deps{Usernames: registry}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), userID)

	user := &tg.User{ID: userID, Username: "base"}
	r.projectResponsePeerObjects(ctx, []tg.UserClass{user})
	if user.Username != "" {
		t.Fatalf("scalar username = %q, want cleared when collectibles exist", user.Username)
	}
	vector, ok := user.GetUsernames()
	if !ok || len(vector) != 2 {
		t.Fatalf("usernames vector = %#v ok=%v, want 2 entries", vector, ok)
	}
}
