package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// Раунд 50: revenue_graph в payments.starsRevenueStats#6c207376 — обязательное
// поле TL-конструктора. Раунд 48 оставлял его nil для леджера без единого
// поступления, энкодер слоя отвечал «field revenue_graph is nil», mtprotoedge
// публиковал INTERNAL, и клиент терял весь ответ: пропадали числа баланса
// вместе с графиком (звёзды у всех пиров, TON у групп), а экран «Баланс»
// настроек бота открывался ошибкой. Фикс: starsRevenueGraph всегда возвращает
// валидный StatsGraphClass (StatsGraphError-стаб «Not enough data to display»
// для пустого леджера), поэтому ответ кодируется всегда.

func round50StatsMustEncode(t *testing.T, stage string, stats *tg.PaymentsStarsRevenueStats) {
	t.Helper()
	var buf bin.Buffer
	if err := stats.Encode(&buf); err != nil {
		t.Fatalf("%s: encode starsRevenueStats: %v", stage, err)
	}
	if stats.RevenueGraph == nil {
		t.Fatalf("%s: revenue_graph is nil", stage)
	}
	if _, ok := stats.RevenueGraph.(*tg.StatsGraphError); !ok {
		t.Fatalf("%s: empty ledger graph = %T, want *tg.StatsGraphError", stage, stats.RevenueGraph)
	}
}

// Личный леджер без единого поступления: звёзды и TON обе кодируются.
func TestRound50EmptyPersonalLedgerRevenueStatsEncodes(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000050)
	gifts := &round49PersonalGifts{}
	r := New(Config{}, Deps{Gifts: gifts}, zaptest.NewLogger(t), fixedClock{now: round49Now})

	starsStats, err := r.onPaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("personal stars stats: %v", err)
	}
	round50StatsMustEncode(t, "personal stars", starsStats)

	req := &tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerSelf{}}
	req.SetTon(true)
	tonStats, err := r.onPaymentsGetStarsRevenueStats(ctx, req)
	if err != nil {
		t.Fatalf("personal ton stats: %v", err)
	}
	round50StatsMustEncode(t, "personal ton", tonStats)
	if _, ok := tonStats.Status.CurrentBalance.(*tg.StarsTonAmount); !ok {
		t.Fatalf("personal ton current = %T, want *tg.StarsTonAmount", tonStats.Status.CurrentBalance)
	}
}

// Канал/группа с пустым леджером (график есть не у каждого владельца на момент
// запроса): и звёзды, и TON кодируются целиком — клиент видит нулевые числа и
// «Not enough data to display» вместо INTERNAL.
func TestRound50EmptyChannelLedgerRevenueStatsEncodes(t *testing.T) {
	const viewerID, channelID int64 = 1000000050, 2000000050
	view := domain.ChannelView{
		Channel: domain.Channel{ID: channelID, AccessHash: 5001, Title: "Empty Ledger Group", Megagroup: true, CreatorUserID: viewerID},
		Self:    domain.ChannelMember{ChannelID: channelID, UserID: viewerID, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive},
	}
	gifts := &round49ChannelLedgerGifts{}
	r := New(Config{}, Deps{Gifts: gifts, Channels: &channelLedgerChannels{view: view}}, zaptest.NewLogger(t), fixedClock{now: round49Now})
	ctx := WithUserID(context.Background(), viewerID)
	peer := &tg.InputPeerChannel{ChannelID: channelID, AccessHash: view.Channel.AccessHash}

	starsStats, err := r.onPaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer})
	if err != nil {
		t.Fatalf("channel stars stats: %v", err)
	}
	round50StatsMustEncode(t, "channel stars", starsStats)

	req := &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer}
	req.SetTon(true)
	tonStats, err := r.onPaymentsGetStarsRevenueStats(ctx, req)
	if err != nil {
		t.Fatalf("channel ton stats: %v", err)
	}
	round50StatsMustEncode(t, "channel ton", tonStats)
}

// Раунд 50 (баланс бота): userFull#6cbe645 can_view_revenue (flags2.9) —
// гейт клиентского экрана «Баланс» в настройках бота. Владелец (и собственная
// bot-сессия) получает флаг; сторонний аккаунт — нет.
func TestRound50BotFullCanViewRevenueOwnerOnly(t *testing.T) {
	ownerStore := memory.NewUserStore()
	users := appusers.NewService(ownerStore)
	botStore := memory.NewBotStore(ownerStore)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(ownerStore, botStore, messages)
	owner, err := ownerStore.Create(context.Background(), domain.User{AccessHash: 5050, Phone: "15550005001", FirstName: "Bot Owner 50"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := ownerStore.Create(context.Background(), domain.User{AccessHash: 5051, Phone: "15550005002", FirstName: "Other Human 50"})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := bots.CreateBot(context.Background(), owner.ID, "Round50 Bot", "round50_balancebot")
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{
		Users:    users,
		Bots:     bots,
		Messages: &captureMessages{},
	}, zaptest.NewLogger(t), fixedClock{now: round49Now})
	botPeer := &tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash}

	ownerFull, err := r.onUsersGetFullUser(WithUserID(context.Background(), owner.ID), botPeer)
	if err != nil {
		t.Fatalf("owner getFullUser: %v", err)
	}
	if !ownerFull.FullUser.GetCanViewRevenue() {
		t.Fatalf("owner userFull.can_view_revenue not set, want true")
	}

	otherFull, err := r.onUsersGetFullUser(WithUserID(context.Background(), other.ID), botPeer)
	if err != nil {
		t.Fatalf("other getFullUser: %v", err)
	}
	if otherFull.FullUser.GetCanViewRevenue() {
		t.Fatalf("other userFull.can_view_revenue set, want unset (приватность баланса бота)")
	}
}

// Раунд 50 (экран баланса бота всё ещё закрыт для чужих): privacy-модель
// раунда 48 сохраняется — сторонний аккаунт получает нейтральный ответ, а
// владелец после фикса кодирования снова видит числа и график.
func TestRound50BotRevenueStatsEncodeForOwnerAndNeutralForOther(t *testing.T) {
	ownerStore := memory.NewUserStore()
	users := appusers.NewService(ownerStore)
	botStore := memory.NewBotStore(ownerStore)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(ownerStore, botStore, messages)
	owner, err := ownerStore.Create(context.Background(), domain.User{AccessHash: 5052, Phone: "15550005003", FirstName: "Bot Owner 50b"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := ownerStore.Create(context.Background(), domain.User{AccessHash: 5053, Phone: "15550005004", FirstName: "Other Human 50b"})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := bots.CreateBot(context.Background(), owner.ID, "Round50 Bot B", "round50_secondbot")
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{
		Users: users,
		Stars: appstars.NewService(memory.NewStarsStore()),
		Bots:  bots,
	}, zaptest.NewLogger(t), fixedClock{now: round49Now})
	peer := &tg.InputPeerUser{UserID: bot.ID, AccessHash: bot.AccessHash}

	otherStats, err := r.onPaymentsGetStarsRevenueStats(WithUserID(context.Background(), other.ID), &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer})
	if err != nil {
		t.Fatalf("other bot revenue stats: %v", err)
	}
	round50StatsMustEncode(t, "other bot", otherStats)

	ownerStats, err := r.onPaymentsGetStarsRevenueStats(WithUserID(context.Background(), owner.ID), &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer})
	if err != nil {
		t.Fatalf("owner bot revenue stats: %v", err)
	}
	var buf bin.Buffer
	if err := ownerStats.Encode(&buf); err != nil {
		t.Fatalf("owner bot encode: %v", err)
	}
	if ownerStats.RevenueGraph == nil {
		t.Fatalf("owner bot revenue_graph is nil")
	}
}

// Раунд 50 (анти-мерцание, echo-push аватара): прямой текущесессионный
// echo-push больше не может затереть кэш пира объектом без значка —
// pushSelfPhotoUpdateWithUser штампует bot_verification_icon заранее.
func TestRound50SelfPhotoEchoPushCarriesVerificationIcon(t *testing.T) {
	const selfID, viewerID int64 = 1000000050, 1000000051
	ctx := WithUserID(context.Background(), viewerID)
	registry := newFakeUsernameRegistry()
	verifications := newFakeBotVerifications()
	verifications.marks = map[domain.Peer]domain.CustomVerification{
		{Type: domain.PeerTypeUser, ID: selfID}: {VerifierBotID: 1000000999, IconDocumentID: 555777},
	}
	r := New(Config{}, Deps{
		Usernames:        registry,
		BotVerifications: verifications,
	}, zaptest.NewLogger(t), fixedClock{now: round49Now})

	projected := r.tgSelfUserWithUsernames(ctx, domain.User{ID: selfID, FirstName: "Icon Self"})
	r.pushSelfPhotoUpdateWithUser(ctx, domain.User{ID: selfID, FirstName: "Icon Self"}, projected)
	if icon, ok := projected.GetBotVerificationIcon(); !ok || icon != 555777 {
		t.Fatalf("echo push user icon = %d, %v; want 555777, true", icon, ok)
	}
}

var _ = tgerr.Is // держим импорт tgerr для будущих ассертов ошибок
