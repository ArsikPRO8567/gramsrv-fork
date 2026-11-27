package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/session"
		"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"

	appaccount "telesrv/internal/app/account"
		"go.uber.org/zap/zaptest"

	appauth "telesrv/internal/app/auth"
	appchannels "telesrv/internal/app/channels"
appcontacts "telesrv/internal/app/contacts"
	appdialogs "telesrv/internal/app/dialogs"
	aphelp "telesrv/internal/app/help"
	applangpack "telesrv/internal/app/langpack"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	appupdates "telesrv/internal/app/updates"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

// Round52: клиент сообщает, что ответа на stats.getBroadcastStats не приходит.
// Тест гоняет реальный wire-путь (TCP + MTProto) и проверяет доставку ответа.
func TestRound52BroadcastStatsE2E(t *testing.T) {
	const (
		dc   = 2
		code = "12345"
	)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)

	userStore := memory.NewUserStore()
	authzStore := memory.NewAuthorizationStore()
	authKeyStore := memory.NewAuthKeyStore()
	helpStore := memory.NewHelpStore()
	langPackStore := memory.NewLangPackStore()
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	channelStore := memory.NewChannelStore()
	activeSessions := NewSessionManager(zaptest.NewLogger(t))
	deps := rpc.Deps{
		Auth: appauth.NewService(userStore, authzStore, memory.NewCodeStore(), authKeyStore,
			memory.NewTempAuthKeyBindingStore(authKeyStore), code),
		Account:  appaccount.NewService(memory.NewPasswordStore()),
		Help:     aphelp.NewService(helpStore, helpStore),
		Users:    appusers.NewService(userStore),
		Updates:  appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Contacts: appcontacts.NewService(memory.NewContactStore()),
		Dialogs:  appdialogs.NewService(dialogStore),
		Messages: appmessages.NewService(messageStore, dialogStore),
		Channels: appchannels.NewService(channelStore),
		LangPack: applangpack.NewService(langPackStore),
		Sessions: activeSessions,
	}
	router := rpc.New(rpc.Config{DC: dc, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, LayerRPC: router, ActiveSessions: activeSessions})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	opts := telegram.Options{
		PublicKeys:     []exchange.PublicKey{{RSA: &rsaKey.PublicKey}},
		Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: tcpAddr.IP.String(), Port: tcpAddr.Port, Static: true}}},
		UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
		SessionStorage: &session.StorageMemory{},
	}
	client := telegram.NewClient(1, "hash", opts)
	wait := make(chan struct{})
	if err := client.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(client)
		sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{PhoneNumber: "+15550003001", APIID: 1, APIHash: "hash", Settings: tg.CodeSettings{}})
		if err != nil {
			return err
		}
		hash := sent.(*tg.AuthSentCode).PhoneCodeHash
		signRes, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: "+15550003001", PhoneCodeHash: hash, PhoneCode: code})
		if err != nil {
			return err
		}
		if _, need := signRes.(*tg.AuthAuthorizationSignUpRequired); need {
			t.Logf("signUp required, signing up")
			signUp, suErr := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{PhoneNumber: "+15550003001", PhoneCodeHash: hash, FirstName: "Owner"})
			if suErr != nil {
				return suErr
			}
			if authz, ok := signUp.(*tg.AuthAuthorization); ok {
				if u, ok := authz.User.(*tg.User); ok {
					t.Logf("signed up user id=%d bot=%v", u.ID, u.Bot)
				}
			}
		}
		created, err := raw.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Title: "Stats", About: "test", Broadcast: true})
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}
		chats := created.(*tg.Updates).Chats
		if len(chats) == 0 {
			t.Fatal("no chats in create channel response")
		}
		channel := chats[0].(*tg.Channel)

		// Основной вызов: статистика канала тем же клиентом.
		stats, statsErr := raw.StatsGetBroadcastStats(ctx, &tg.StatsGetBroadcastStatsRequest{Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}})
		if statsErr != nil {
			t.Logf("stats.getBroadcastStats error: %v", statsErr)
		} else {
			bs := stats
			t.Logf("stats.getBroadcastStats OK: period=%v growth=%T", bs.Period, bs.GrowthGraph)
		}

		// Второй вызов: revenue stats для сравнения ( round49 фиксы).
		rev, revErr := raw.PaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}})
		if revErr != nil {
			t.Logf("getStarsRevenueStats error: %v", revErr)
		} else {
			t.Logf("getStarsRevenueStats OK: %T", rev)
		}
		close(wait)
		return nil
	}); err != nil && ctx.Err() == nil {
		t.Fatalf("client run: %v", err)
	}
	select {
	case <-wait:
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for client flow")
	}
}
