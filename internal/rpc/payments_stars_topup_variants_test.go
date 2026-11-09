package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	appstars "telesrv/internal/app/stars"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// Раунд 31: варианты транзакций начисления звёзд. Официальные клиенты
// локализуют начисление по типу peer: StarsTransactionPeerFragment —
// «Покупка на Fragment», StarsTransactionPeerAppStore/PlayMarket — «Покупка
// в приложении», StarsTransactionPeerPremiumBot — «Бот для покупки звёзд».
func TestStarsTopupTransactionPeerVariants(t *testing.T) {
	cases := []struct {
		reason domain.StarsTransactionReason
		want   tg.StarsTransactionPeerClass
	}{
		{domain.StarsReasonTopup, &tg.StarsTransactionPeerFragment{}},
		{domain.StarsReasonGrant, &tg.StarsTransactionPeerFragment{}},
		{domain.StarsReasonTopupAppStore, &tg.StarsTransactionPeerAppStore{}},
		{domain.StarsReasonTopupPlayMarket, &tg.StarsTransactionPeerPlayMarket{}},
		{domain.StarsReasonTopupBot, &tg.StarsTransactionPeerPremiumBot{}},
		{domain.StarsReasonPremium, &tg.StarsTransactionPeerPremiumBot{}},
	}
	for _, tc := range cases {
		peer := tgStarsTransactionPeer(domain.StarsTransaction{Reason: tc.reason, Peer: domain.Peer{}})
		switch want := tc.want.(type) {
		case *tg.StarsTransactionPeerFragment:
			if _, ok := peer.(*tg.StarsTransactionPeerFragment); !ok {
				t.Fatalf("reason %s peer = %T, want Fragment", tc.reason, peer)
			}
		case *tg.StarsTransactionPeerAppStore:
			if _, ok := peer.(*tg.StarsTransactionPeerAppStore); !ok {
				t.Fatalf("reason %s peer = %T, want AppStore", tc.reason, peer)
			}
		case *tg.StarsTransactionPeerPlayMarket:
			if _, ok := peer.(*tg.StarsTransactionPeerPlayMarket); !ok {
				t.Fatalf("reason %s peer = %T, want PlayMarket", tc.reason, peer)
			}
		case *tg.StarsTransactionPeerPremiumBot:
			if _, ok := peer.(*tg.StarsTransactionPeerPremiumBot); !ok {
				t.Fatalf("reason %s peer = %T, want PremiumBot", tc.reason, peer)
			}
		default:
			t.Fatalf("unexpected want type %T", want)
		}
	}
}

// Канал приобретения -> reason транзакции начисления.
func TestStarsTopupAcquisitionReasonMapping(t *testing.T) {
	cases := map[domain.StarsTopupAcquisition]domain.StarsTransactionReason{
		domain.StarsTopupFragment:               domain.StarsReasonTopup,
		domain.StarsTopupAppStore:               domain.StarsReasonTopupAppStore,
		domain.StarsTopupPlayMarket:             domain.StarsReasonTopupPlayMarket,
		domain.StarsTopupBot:                    domain.StarsReasonTopupBot,
		domain.StarsTopupAcquisition("unknown"): domain.StarsReasonTopup,
	}
	for acquisition, want := range cases {
		if got := acquisition.StarsTopupReason(); got != want {
			t.Fatalf("acquisition %q reason = %s, want %s", acquisition, got, want)
		}
	}
}

// Выбор канала по платформе сессии покупателя: iOS -> App Store,
// Android -> Google Play, desktop/web/unknown -> Fragment.
func TestStarsTopupAcquisitionForClient(t *testing.T) {
	r := starsRouter(t, 1000)
	buildCtx := func(info ClientInfo) context.Context {
		ctx := WithUserID(context.Background(), 1000000001)
		return WithClientInfo(ctx, info)
	}
	cases := []struct {
		name string
		info ClientInfo
		want domain.StarsTopupAcquisition
	}{
		{"ios", ClientInfo{APIID: 8, DeviceModel: "iPhone 15", SystemVersion: "iOS 17.4", AppVersion: "10.2.1", LangPack: "ios", Type: ClientTypeIOS, typeResolved: true}, domain.StarsTopupAppStore},
		{"android", ClientInfo{APIID: 6, DeviceModel: "Pixel 8", SystemVersion: "SDK 34", AppVersion: "10.2.1", LangPack: "android", Type: ClientTypeAndroid, typeResolved: true}, domain.StarsTopupPlayMarket},
		{"tdesktop", ClientInfo{APIID: 2040, DeviceModel: "PC", SystemVersion: "Windows 11", AppVersion: "5.2.0", LangPack: "tdesktop", Type: ClientTypeTDesktop, typeResolved: true}, domain.StarsTopupFragment},
		{"unknown", ClientInfo{}, domain.StarsTopupFragment},
	}
	for _, tc := range cases {
		ctx := buildCtx(tc.info)
		if got := starsTopupAcquisitionForClient(r, ctx); got != tc.want {
			t.Fatalf("%s: acquisition = %q, want %q", tc.name, got, tc.want)
		}
	}
	// Без ClientInfo в ctx вообще — Fragment.
	if got := starsTopupAcquisitionForClient(r, WithUserID(context.Background(), 1000000001)); got != domain.StarsTopupFragment {
		t.Fatalf("no client info acquisition = %q, want Fragment", got)
	}
}

// Конец-в-конец через postgres-агрегат покупок: транзакция начисления несёт
// reason выбранного канала (App Store / Google Play / Fragment), а админское
// начисление оператора — topup_bot («Бот для покупки звёзд»).
func TestStarsTopupTransactionReasonEndToEndPG(t *testing.T) {
	pool := round28TestPool(t)
	ctx := context.Background()
	now := int(time.Now().Unix())
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	usersStore := postgres.NewUserStore(pool)
	buyer, err := usersStore.Create(ctx, domain.User{AccessHash: 7401, Phone: "+1555" + suffix[len(suffix)-8:] + "1", FirstName: "TopupBuyer"})
	if err != nil {
		t.Fatalf("create buyer: %v", err)
	}
	starsStore := postgres.NewStarsStore(pool)
	purchase := postgres.NewStarsPurchaseStore(pool, postgres.NewMessageStore(pool))
	svc := appstars.NewService(starsStore, appstars.WithStartingGrant(0), appstars.WithPurchaseStore(purchase))

	cases := []struct {
		name        string
		acquisition domain.StarsTopupAcquisition
		wantReason  domain.StarsTransactionReason
	}{
		{"fragment", domain.StarsTopupFragment, domain.StarsReasonTopup},
		{"appstore", domain.StarsTopupAppStore, domain.StarsReasonTopupAppStore},
		{"playmarket", domain.StarsTopupPlayMarket, domain.StarsReasonTopupPlayMarket},
	}
	for _, tc := range cases {
		form, err := svc.IssuePurchaseForm(ctx, domain.StarsPurchaseForm{
			Kind: domain.StarsPurchaseTopup, BuyerUserID: buyer.ID,
			Stars: 1000, Currency: "USD", Amount: 99,
			IssuedAt: now, ExpiresAt: now + 600,
			Acquisition: tc.acquisition,
		})
		if err != nil {
			t.Fatalf("%s: issue form: %v", tc.name, err)
		}
		if _, err := svc.Purchase(ctx, domain.StarsPurchaseRequest{
			StarsPurchaseForm: form,
			Date:              now + 10,
		}); err != nil {
			t.Fatalf("%s: purchase: %v", tc.name, err)
		}
		var reason string
		if err := pool.QueryRow(ctx, `SELECT reason FROM stars_transactions WHERE user_id=$1 AND amount=1000 AND reason<>'grant' ORDER BY id DESC LIMIT 1`, buyer.ID).Scan(&reason); err != nil {
			t.Fatalf("%s: load transaction: %v", tc.name, err)
		}
		if want := string(tc.wantReason); reason != want {
			t.Fatalf("%s: topup reason = %s, want %s", tc.name, reason, want)
		}
	}

	// Проекция peer по записанным reason: клиент получает официальный
	// counterpart для каждого канала.
	var reasons []string
	if err := pool.QueryRow(ctx, `SELECT array_agg(reason) FROM (SELECT DISTINCT reason FROM stars_transactions WHERE user_id=$1 AND amount=1000 ORDER BY reason) s`, buyer.ID).Scan(&reasons); err != nil {
		t.Fatalf("distinct reasons: %v", err)
	}
	want := map[string]bool{"topup": true, "topup_appstore": true, "topup_playmarket": true}
	for _, reason := range reasons {
		if !want[reason] {
			t.Fatalf("unexpected transaction reason %q (have %v)", reason, reasons)
		}
		peer := tgStarsTransactionPeer(domain.StarsTransaction{Reason: domain.StarsTransactionReason(reason)})
		switch reason {
		case "topup":
			if _, ok := peer.(*tg.StarsTransactionPeerFragment); !ok {
				t.Fatalf("reason %s peer = %T, want Fragment", reason, peer)
			}
		case "topup_appstore":
			if _, ok := peer.(*tg.StarsTransactionPeerAppStore); !ok {
				t.Fatalf("reason %s peer = %T, want AppStore", reason, peer)
			}
		case "topup_playmarket":
			if _, ok := peer.(*tg.StarsTransactionPeerPlayMarket); !ok {
				t.Fatalf("reason %s peer = %T, want PlayMarket", reason, peer)
			}
		}
	}
}
