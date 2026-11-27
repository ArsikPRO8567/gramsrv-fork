package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// Раунд 49: графики статистики баланса для каналов/групп (звёзды и TON) и
// TON-график личного баланса (пользователь/бот) доходят до клиента. Корень
// дефекта раунда 48: app-слой Gifts не прокидывал дневные серии
// (*ReceivedDaily) из lifecycle-хранилища, поэтому type assertion в RPC-слое
// всегда проваливался и сервер отвечал нейтральным StatsGraphError-стабом.

var round49Now = time.Unix(1_800_000_000, 0) // 2027-01-15 UTC, как в TestRound48StarsRevenueGraph

func round49DayOffsets() (today, yesterday int64) {
	t := int(round49Now.Unix())
	today = int64(t - t%86400)
	yesterday = today - 86400
	return today, yesterday
}

type round49ChannelLedgerGifts struct {
	GiftsService
	starsBalance int64
	tonBalance   int64
	starsDaily   []domain.StarsDailyPoint
	tonDaily     []domain.StarsDailyPoint
}

func (s *round49ChannelLedgerGifts) ChannelStarsBalance(context.Context, int64) (int64, error) {
	return s.starsBalance, nil
}

func (s *round49ChannelLedgerGifts) ChannelStarsTransactions(context.Context, int64, domain.StarsTransactionQuery) (domain.StarsTransactionPage, error) {
	return domain.StarsTransactionPage{}, nil
}

func (s *round49ChannelLedgerGifts) ChannelStarsTransactionsByID(context.Context, int64, []string) ([]domain.StarsTransaction, error) {
	return nil, nil
}

func (s *round49ChannelLedgerGifts) ChannelTonBalance(context.Context, int64) (int64, error) {
	return s.tonBalance, nil
}

func (s *round49ChannelLedgerGifts) ChannelTonTransactions(context.Context, int64, domain.StarsTransactionQuery) (domain.TonTransactionPage, error) {
	return domain.TonTransactionPage{}, nil
}

func (s *round49ChannelLedgerGifts) ChannelTonTransactionsByID(context.Context, int64, []string) ([]domain.TonTransaction, error) {
	return nil, nil
}

func (s *round49ChannelLedgerGifts) ChannelStarsReceivedDaily(context.Context, int64) ([]domain.StarsDailyPoint, error) {
	return s.starsDaily, nil
}

func (s *round49ChannelLedgerGifts) ChannelTonReceivedDaily(context.Context, int64) ([]domain.StarsDailyPoint, error) {
	return s.tonDaily, nil
}

func round49GraphTotal(t *testing.T, stage string, graph tg.StatsGraphClass) (float64, string) {
	t.Helper()
	stats, ok := graph.(*tg.StatsGraph)
	if !ok {
		t.Fatalf("%s graph = %T, want *tg.StatsGraph", stage, graph)
	}
	var payload struct {
		Columns []json.RawMessage `json:"columns"`
		Names   map[string]string `json:"names"`
	}
	if err := json.Unmarshal([]byte(stats.JSON.Data), &payload); err != nil {
		t.Fatalf("%s graph json: %v", stage, err)
	}
	if len(payload.Columns) != 2 {
		t.Fatalf("%s graph columns = %d, want 2", stage, len(payload.Columns))
	}
	var yRow []any
	if err := json.Unmarshal(payload.Columns[1], &yRow); err != nil {
		t.Fatalf("%s y row: %v", stage, err)
	}
	if len(yRow) < 2 || yRow[0] != "y0" {
		t.Fatalf("%s y row = %v, want header y0 + дни", stage, yRow)
	}
	var sum float64
	for _, v := range yRow[1:] {
		if n, ok := v.(float64); ok {
			sum += n
		}
	}
	return sum, payload.Names["y0"]
}

func TestRound49ChannelStarsRevenueGraph(t *testing.T) {
	const viewerID, channelID int64 = 1000000049, 2000000049
	view := domain.ChannelView{
		Channel: domain.Channel{ID: channelID, AccessHash: 4901, Title: "Graph Channel", Broadcast: true, CreatorUserID: viewerID},
		Self:    domain.ChannelMember{ChannelID: channelID, UserID: viewerID, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive},
	}
	today, yesterday := round49DayOffsets()
	gifts := &round49ChannelLedgerGifts{
		starsBalance: 300,
		tonBalance:   1500,
		starsDaily:   []domain.StarsDailyPoint{{Day: today, Amount: 200}, {Day: yesterday, Amount: 100}},
		tonDaily:     []domain.StarsDailyPoint{{Day: yesterday, Amount: 900}, {Day: today, Amount: 600}},
	}
	r := New(Config{}, Deps{Gifts: gifts, Channels: &channelLedgerChannels{view: view}}, zaptest.NewLogger(t), fixedClock{now: round49Now})
	ctx := WithUserID(context.Background(), viewerID)
	peer := &tg.InputPeerChannel{ChannelID: channelID, AccessHash: view.Channel.AccessHash}

	starsRevenue, err := r.onPaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer})
	if err != nil {
		t.Fatalf("channel stars revenue: %v", err)
	}
	sum, series := round49GraphTotal(t, "channel stars", starsRevenue.RevenueGraph)
	if sum != 300 {
		t.Fatalf("channel stars graph total = %v, want 300", sum)
	}
	if series != "XTR" {
		t.Fatalf("channel stars graph series = %q, want XTR", series)
	}

	revenueReq := &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer}
	revenueReq.SetTon(true)
	tonRevenue, err := r.onPaymentsGetStarsRevenueStats(ctx, revenueReq)
	if err != nil {
		t.Fatalf("channel ton revenue: %v", err)
	}
	sum, series = round49GraphTotal(t, "channel ton", tonRevenue.RevenueGraph)
	if sum != 1500 {
		t.Fatalf("channel ton graph total = %v, want 1500", sum)
	}
	if series != "TON" {
		t.Fatalf("channel ton graph series = %q, want TON", series)
	}
	if current, ok := tonRevenue.Status.CurrentBalance.(*tg.StarsTonAmount); !ok || current.Amount != 1500 {
		t.Fatalf("channel ton revenue current = %+v, want StarsTonAmount 1500", tonRevenue.Status.CurrentBalance)
	}
}

type round49PersonalGifts struct {
	GiftsService
	tonBalance int64
	total      int64
	daily      []domain.StarsDailyPoint
}

func (s *round49PersonalGifts) TonBalance(context.Context, int64) (int64, error) {
	return s.tonBalance, nil
}

func (s *round49PersonalGifts) TonTotalReceived(context.Context, int64) (int64, error) {
	return s.total, nil
}

func (s *round49PersonalGifts) TonReceivedDaily(context.Context, int64) ([]domain.StarsDailyPoint, error) {
	return s.daily, nil
}

// TestRound49PersonalTonRevenueGraph: TON-вкладка баланса пользователя (и
// владельца бота) получает график дневных поступлений, а не нейтральный стаб.
func TestRound49PersonalTonRevenueGraph(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000049)
	today, yesterday := round49DayOffsets()
	gifts := &round49PersonalGifts{
		tonBalance: 4200,
		total:      4200,
		daily:      []domain.StarsDailyPoint{{Day: yesterday, Amount: 2700}, {Day: today, Amount: 1500}},
	}
	r := New(Config{}, Deps{Gifts: gifts}, zaptest.NewLogger(t), fixedClock{now: round49Now})

	req := &tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerSelf{}}
	req.SetTon(true)
	revenue, err := r.onPaymentsGetStarsRevenueStats(ctx, req)
	if err != nil {
		t.Fatalf("personal ton revenue: %v", err)
	}
	sum, series := round49GraphTotal(t, "personal ton", revenue.RevenueGraph)
	if sum != 4200 {
		t.Fatalf("personal ton graph total = %v, want 4200", sum)
	}
	if series != "TON" {
		t.Fatalf("personal ton graph series = %q, want TON", series)
	}
	if current, ok := revenue.Status.CurrentBalance.(*tg.StarsTonAmount); !ok || current.Amount != 4200 {
		t.Fatalf("personal ton current = %+v, want StarsTonAmount 4200", revenue.Status.CurrentBalance)
	}
	if overall, ok := revenue.Status.OverallRevenue.(*tg.StarsTonAmount); !ok || overall.Amount != 4200 {
		t.Fatalf("personal ton overall = %+v, want StarsTonAmount 4200", revenue.Status.OverallRevenue)
	}
}

// Раунд 49 (анти-мерцание): прямой пуш с «наивным» tg.User (скалярный username,
// без bot_verification_icon) проходит границу доставки applyPushPeerOverlays —
// вектор коллекционных юзернеймов и значок кастомной верификации штампуются, а
// скаляр сбрасывается. Без этого клиент затирает кэш пира каждым пушем и значки
// «мерцают» между ответами.
func TestRound49PushBoundaryOverlaysStampNaivePush(t *testing.T) {
	const viewerID, targetID int64 = 1000000050, 1000000051
	registry := newFakeUsernameRegistry()
	registry.byPeer[domain.Peer{Type: domain.PeerTypeUser, ID: targetID}] = []domain.Username{
		{Username: "owner_slot", Editable: true, Active: true, SortOrder: 1},
		{Username: "nft49", Active: true, SortOrder: 0, CollectibleID: 4949},
	}
	verification := newFakeBotVerifications()
	verification.marks[domain.Peer{Type: domain.PeerTypeUser, ID: targetID}] = domain.CustomVerification{
		VerifierBotID:  1000000052,
		IconDocumentID: 555777,
	}
	sessions := &captureSessions{onlineUserIDs: []int64{viewerID}}
	r := New(Config{}, Deps{
		Usernames:        registry,
		BotVerifications: verification,
		Sessions:         sessions,
	}, zap.NewNop(), clock.System)

	push := func() *tg.User {
		naive := &tg.User{ID: targetID, AccessHash: 49, FirstName: "Owner"}
		naive.SetUsername("owner_slot")
		r.pushUserUpdates(context.Background(), viewerID, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: targetID}},
			Users:   []tg.UserClass{naive},
			Date:    int(round49Now.Unix()),
		})
		updates := sessions.lastUserPush().(*tg.Updates)
		return updates.Users[0].(*tg.User)
	}

	stamped := push()
	assertVectorOnlyUsernames(t, "pushed user", stamped, []string{"nft49", "owner_slot"})
	if icon, ok := stamped.GetBotVerificationIcon(); !ok || icon != 555777 {
		t.Fatalf("pushed verification icon = %d/%v, want 555777/true", icon, ok)
	}

	// Повторный пуш того же пира другим наивным объектом — значения
	// детерминированы (иконка не «мерцает» между пушами).
	stamped = push()
	assertVectorOnlyUsernames(t, "pushed user again", stamped, []string{"nft49", "owner_slot"})
	if icon, ok := stamped.GetBotVerificationIcon(); !ok || icon != 555777 {
		t.Fatalf("second push verification icon = %d/%v, want 555777/true", icon, ok)
	}
}

// Раунд 49 (анти-мерцание): канал с кастомной верификацией в фан-ауте сообщений
// несёт bot_verification_icon в каждом пуш-объекте (иконки предзагружаются один
// батч на fan-out и штампуются билдером), поэтому значок канала не пропадает
// после каждого нового сообщения.
func TestRound49ChannelFanoutPushesCarryVerificationIcon(t *testing.T) {
	const ownerID, channelID int64 = 1000000053, 2000000053
	verification := newFakeBotVerifications()
	verification.marks[domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}] = domain.CustomVerification{
		VerifierBotID:  1000000052,
		IconDocumentID: 909090,
	}
	registry := newFakeUsernameRegistry()
	sessions := &captureSessions{onlineUserIDs: []int64{ownerID}}
	r := New(Config{}, Deps{
		Usernames:        registry,
		BotVerifications: verification,
		Sessions:         sessions,
	}, zap.NewNop(), clock.System)

	res := domain.SendChannelMessageResult{
		Channel: domain.Channel{ID: channelID, AccessHash: 53, Title: "Verified", Broadcast: true, CreatorUserID: ownerID},
		Event:   domain.ChannelUpdateEvent{Pts: 7, Date: int(round49Now.Unix())},
		Message: domain.ChannelMessage{ChannelID: channelID, SenderUserID: ownerID, Date: int(round49Now.Unix())},
	}
	r.enqueueChannelMessageFanout(WithUserID(context.Background(), ownerID), ownerID, res, nil)

	updates := sessions.lastUserPush().(*tg.Updates)
	found := false
	for _, chat := range updates.Chats {
		ch, ok := chat.(*tg.Channel)
		if !ok || ch.ID != channelID {
			continue
		}
		found = true
		if icon, ok := ch.GetBotVerificationIcon(); !ok || icon != 909090 {
			t.Fatalf("fanout channel verification icon = %d/%v, want 909090/true", icon, ok)
		}
	}
	if !found {
		t.Fatalf("fanout push chats = %+v, want channel %d", updates.Chats, channelID)
	}
}
