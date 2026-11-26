package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

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

// Раунд 47 (вкладки балансов): Stars/TON баланс бота публичен — другой аккаунт
// видит реальные числа пира бота (BotStarsActivity), чужой обычный пользователь
// по-прежнему PEER_ID_INVALID.
func TestRound47BotStarsStatsPublicView(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000047)
	users := memory.NewUserStore()
	bot, err := users.Create(context.Background(), domain.User{AccessHash: 4848, Phone: "15550000481", FirstName: "Stats Bot", Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(context.Background(), domain.User{AccessHash: 4849, Phone: "15550000482", FirstName: "Other Human"})
	if err != nil {
		t.Fatal(err)
	}
	stars := appstars.NewService(memory.NewStarsStore())
	r := New(Config{}, Deps{Users: appusers.NewService(users), Stars: stars}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})
	if _, err := stars.CreditFlags(context.Background(), bot.ID, 500, domain.StarsReasonGift, domain.Peer{Type: domain.PeerTypeUser, ID: 1000000047}, domain.StarsLedgerFlags{}); err != nil {
		t.Fatal(err)
	}

	botPeer := &tg.InputPeerUser{UserID: bot.ID, AccessHash: bot.AccessHash}
	// Просмотр баланса бота сторонним аккаунтом — реальные числа.
	revenue, err := r.onPaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: botPeer})
	if err != nil {
		t.Fatalf("bot revenue stats: %v", err)
	}
	bal, err := stars.GetBalance(context.Background(), bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, ok := revenue.Status.CurrentBalance.(*tg.StarsAmount)
	if !ok || current.Amount != bal.Balance || current.Amount <= 0 {
		t.Fatalf("bot revenue current = %+v, want bot balance %d", revenue.Status.CurrentBalance, bal.Balance)
	}

	// Транзакции бота тоже доступны (вкладка Transactions).
	status, err := r.onPaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: botPeer})
	if err != nil {
		t.Fatalf("bot stars status: %v", err)
	}
	if amount, ok := status.Balance.(*tg.StarsAmount); !ok || amount.Amount != bal.Balance {
		t.Fatalf("bot stars status balance = %+v, want %d", status.Balance, bal.Balance)
	}

	// Чужой обычный пользователь — приватность сохраняется.
	otherPeer := &tg.InputPeerUser{UserID: other.ID, AccessHash: other.AccessHash}
	if _, err := r.onPaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: otherPeer}); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("other user status err = %v, want PEER_ID_INVALID", err)
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
