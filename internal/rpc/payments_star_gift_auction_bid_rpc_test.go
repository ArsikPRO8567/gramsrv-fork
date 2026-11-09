package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/app/messages"
	appstargifts "telesrv/internal/app/stargifts"
	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// PG-интеграция: полный клиентский цикл ставки аукциона через RPC —
// payments.getPaymentForm(inputInvoiceStarGiftAuctionBid) →
// payments.sendStarsForm. Репродукция «пользователь не может увеличить ставку
// в действующем раунде: сервер вернёт STARGIFT_INVALID».
func TestStarGiftAuctionBidIncreaseRPC(t *testing.T) {
	pool := round28TestPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	now := int(time.Now().Unix())

	usersStore := postgres.NewUserStore(pool)
	dialogs := postgres.NewDialogStore(pool)
	msgStore := postgres.NewMessageStore(pool)
	alpha, err := usersStore.Create(ctx, domain.User{AccessHash: 7301, Phone: "+1555" + suffix[len(suffix)-8:] + "01", FirstName: "BidAlpha"})
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	starsStore := postgres.NewStarsStore(pool)
	if _, _, err := starsStore.EnsureGrant(ctx, alpha.ID, 1_000_000, now); err != nil {
		t.Fatalf("grant stars: %v", err)
	}

	gifts := postgres.NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "BidIncRPC " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 2, AvailabilityRemains: 2, GiftsPerRound: 1, AuctionStartDate: now - 10,
		AuctionSlug: "bidincrpc-" + suffix,
		Document:    round28TestDocument(baseDocumentID, "bidincrpc.tgs"),
		Blob:        *round28TestBlob(baseDocumentID),
		Animation:   *round28TestAnimationPtr("bidincrpc.tgs"),
		Actor:       "bidincrpc", CommandID: "bidincrpc-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create auction catalog: %v", err)
	}

	lifecycle := postgres.NewStarGiftLifecycleStore(pool, msgStore, 1_000_000)
	giftsService := appstargifts.NewService(gifts, nil, 2,
		appstargifts.WithLifecycleStore(lifecycle))
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2399, PublicBaseURL: "https://links.example.test"}, Deps{
		Users:    appusers.NewService(usersStore),
		Messages: messages.NewService(msgStore, dialogs),
		Stars:    appstars.NewService(starsStore, appstars.WithStartingGrant(0)),
		Gifts:    giftsService,
	}, zaptest.NewLogger(t), clock.System)

	alphaCtx := WithUserID(ctx, alpha.ID)
	bidInvoice := func(bid int64, update, withPeer bool) *tg.InputInvoiceStarGiftAuctionBid {
		inv := &tg.InputInvoiceStarGiftAuctionBid{GiftID: entry.Gift.ID, BidAmount: bid}
		if update {
			inv.SetUpdateBid(true)
		}
		if withPeer {
			// Реальные клиенты включают получателя в каждый счёт, в том
			// числе при повышении ставки (peer при update_bid не запрещён).
			inv.SetPeer(&tg.InputPeerSelf{})
		}
		return inv
	}

	// 1. Первая ставка в действующем раунде: getPaymentForm → sendStarsForm.
	form, err := r.onPaymentsGetPaymentForm(alphaCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: bidInvoice(100, false, false)})
	if err != nil {
		t.Fatalf("getPaymentForm first bid: %v", err)
	}
	formStarGift, ok := form.(*tg.PaymentsPaymentFormStarGift)
	if !ok {
		t.Fatalf("payment form = %T, want PaymentsPaymentFormStarGift", form)
	}
	if _, err := r.onPaymentsSendStarsForm(alphaCtx, &tg.PaymentsSendStarsFormRequest{
		FormID: formStarGift.FormID, Invoice: bidInvoice(100, false, false),
	}); err != nil {
		t.Fatalf("sendStarsForm first bid: %v", err)
	}

	// 2. Увеличение АКТИВНОЙ ставки в том же раунде (update_bid=true), счёт
	// несёт peer — как у реальных клиентов.
	form2, err := r.onPaymentsGetPaymentForm(alphaCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: bidInvoice(150, true, true)})
	if err != nil {
		t.Fatalf("getPaymentForm increase bid with peer: %v", err)
	}
	formStarGift2, ok := form2.(*tg.PaymentsPaymentFormStarGift)
	if !ok {
		t.Fatalf("increase payment form = %T", form2)
	}
	if _, err := r.onPaymentsSendStarsForm(alphaCtx, &tg.PaymentsSendStarsFormRequest{
		FormID: formStarGift2.FormID, Invoice: bidInvoice(150, true, true),
	}); err != nil {
		t.Fatalf("sendStarsForm increase bid with peer: %v", err)
	}

	// 3. Ставка записана, активна, сумма 150.
	state, err := r.deps.Gifts.AuctionState(ctx, alpha.ID, entry.Gift.ID, "", now+5)
	if err != nil {
		t.Fatalf("auction state: %v", err)
	}
	if state.UserState.BidAmount != 150 || !state.UserState.BidActive {
		t.Fatalf("user state after increase = %+v, want amount 150 active", state.UserState)
	}
	var balance int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, alpha.ID).Scan(&balance); err != nil {
		t.Fatalf("load balance: %v", err)
	}
	if balance != 1_000_000-150 {
		t.Fatalf("balance after increase = %d, want %d (only delta debited)", balance, 1_000_000-150)
	}
}
