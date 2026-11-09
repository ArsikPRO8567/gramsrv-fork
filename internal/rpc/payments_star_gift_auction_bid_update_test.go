package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// Раунд 31: официальные клиенты при увеличении ставки (update_bid=true)
// прикладывают к инвойсу peer/message/hide_name:
//   - Android GiftAuctionController.sendBid копирует AuctionBidSheet.Params
//     (AuctionWearingSheet всегда шлёт hide_name=true, SendGiftSheet —
//     peer+message);
//   - tdesktop PlaceAuctionBid передаёт peer+message, когда окно ставки
//     открыто из SendGiftBox (details != null).
//
// Официальный сервер такие поля принимает, поэтому сервер fork'а не должен
// отклонять их STARGIFT_INVALID: для АКТИВНОЙ ставки атрибуты сохраняются
// прежними («the value cannot be changed for existing bids»), для
// ВОЗВРАЩЁННОЙ (перебитой) — применяются из инвойса, как у новой ставки.
type auctionBidUpdateRPCService struct {
	GiftsService
	state     domain.StarGiftAuction
	bidReq    domain.StarGiftAuctionBidRequest
	bidCalls  int
	bidResult domain.StarGiftAuction
}

func (s *auctionBidUpdateRPCService) AuctionState(context.Context, int64, int64, string, int) (domain.StarGiftAuction, error) {
	return s.state, nil
}

func (s *auctionBidUpdateRPCService) BidAuction(_ context.Context, req domain.StarGiftAuctionBidRequest) (domain.StarGiftAuction, domain.StarsBalance, error) {
	s.bidCalls++
	s.bidReq = req
	return s.bidResult, domain.StarsBalance{UserID: req.UserID, Balance: 0}, nil
}

func auctionBidUpdateState(active, returned bool, oldAmount int64) domain.StarGiftAuction {
	state := domain.StarGiftAuction{
		Gift:         domain.StarGift{ID: 77},
		MinBidAmount: 501,
		Version:      5,
	}
	state.UserState = domain.StarGiftAuctionUserState{
		BidActive:    active,
		Returned:     returned,
		BidAmount:    oldAmount,
		BidDate:      1000,
		BidPeer:      domain.Peer{Type: domain.PeerTypeUser, ID: 42},
		MinBidAmount: 501,
	}
	if !active {
		state.UserState.MinBidAmount = 0
	}
	return state
}

func auctionBidUpdateInvoice(updateBid bool, withPeer, withMessage, hideName bool) *tg.InputInvoiceStarGiftAuctionBid {
	inv := &tg.InputInvoiceStarGiftAuctionBid{
		GiftID:    77,
		BidAmount: 700,
		UpdateBid: updateBid,
		HideName:  hideName,
	}
	if withPeer {
		inv.Peer = &tg.InputPeerUser{UserID: 9999, AccessHash: 1}
	}
	if withMessage {
		inv.Message = tg.TextWithEntities{Text: "go win!"}
	}
	// Официальный клиент сериализует инвойс с флагами битов (peer=3,
	// message=1, hide_name=0, update_bid=2): воспроизводим кодирование с
	// провода, иначе GetPeer/GetMessage не увидят поля.
	inv.SetFlags()
	return inv
}

// Увеличение АКТИВНОЙ ставки инвойсом с peer+message+hide_name (Android
// SendGiftSheet/AuctionWearingSheet, tdesktop SendGiftBox): форма выдаётся,
// к стору уходит прежний получатель и update_bid=true.
func TestStarGiftAuctionBidUpdateAcceptsInvoiceAttributes(t *testing.T) {
	service := &auctionBidUpdateRPCService{state: auctionBidUpdateState(true, false, 500)}
	r := New(Config{DC: 2}, Deps{Gifts: service}, zaptest.NewLogger(t), clock.System)
	ctx := context.Background()

	inv := auctionBidUpdateInvoice(true, true, true, true)
	form, err := r.starGiftAuctionBidPaymentForm(ctx, 42, inv)
	if err != nil {
		t.Fatalf("active bid increase with peer/message/hide_name rejected: %v", err)
	}
	starForm, ok := form.(*tg.PaymentsPaymentFormStarGift)
	if !ok {
		t.Fatalf("payment form type = %T", form)
	}
	invoice := starForm.Invoice
	if len(invoice.Prices) != 1 || invoice.Prices[0].Amount != 200 {
		t.Fatalf("increase delta = %+v, want 700-500=200", invoice.Prices)
	}

	result, err := r.sendStarGiftAuctionBidForm(ctx, 42, starForm.FormID, inv)
	if err != nil {
		t.Fatalf("sendStarsForm rejected bid increase: %v", err)
	}
	if _, ok := result.(*tg.PaymentsPaymentResult); !ok {
		t.Fatalf("payment result type = %T", result)
	}
	if service.bidCalls != 1 {
		t.Fatalf("BidAuction calls = %d, want 1", service.bidCalls)
	}
	req := service.bidReq
	if !req.UpdateBid || req.BidAmount != 700 {
		t.Fatalf("bid request update/amount = %v/%d", req.UpdateBid, req.BidAmount)
	}
	if req.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: 42}) {
		t.Fatalf("active bid keeps original recipient, got %+v", req.Peer)
	}
}

// Активная ставка: сумма ниже «своя ставка + 1» по-прежнему отклоняется.
func TestStarGiftAuctionBidUpdateStillRequiresHigherAmount(t *testing.T) {
	service := &auctionBidUpdateRPCService{state: auctionBidUpdateState(true, false, 500)}
	r := New(Config{DC: 2}, Deps{Gifts: service}, zaptest.NewLogger(t), clock.System)

	inv := auctionBidUpdateInvoice(true, false, false, false)
	inv.BidAmount = 500
	inv.SetFlags()
	if _, err := r.starGiftAuctionBidPaymentForm(context.Background(), 42, inv); err == nil {
		t.Fatalf("re-bid of own active amount must be rejected")
	}
}

// Возвращённая ставка (перебита): повторное участие с update_bid=true и НОВЫМ
// получателем из инвойса — новая ставка с атрибутами инвойса.
func TestStarGiftAuctionReturnedBidReentryAppliesInvoicePeer(t *testing.T) {
	service := &auctionBidUpdateRPCService{state: auctionBidUpdateState(false, true, 500)}
	r := New(Config{DC: 2}, Deps{Gifts: service}, zaptest.NewLogger(t), clock.System)
	ctx := context.Background()

	inv := auctionBidUpdateInvoice(true, true, true, true)
	form, err := r.starGiftAuctionBidPaymentForm(ctx, 42, inv)
	if err != nil {
		t.Fatalf("returned bid re-entry rejected: %v", err)
	}
	starForm := form.(*tg.PaymentsPaymentFormStarGift)
	if len(starForm.Invoice.Prices) != 1 || starForm.Invoice.Prices[0].Amount != 700 {
		t.Fatalf("returned re-entry charges full amount, got %+v", starForm.Invoice.Prices)
	}

	if _, err := r.sendStarGiftAuctionBidForm(ctx, 42, starForm.FormID, inv); err != nil {
		t.Fatalf("sendStarsForm rejected returned re-entry: %v", err)
	}
	req := service.bidReq
	if !req.UpdateBid || req.BidAmount != 700 {
		t.Fatalf("bid request update/amount = %v/%d", req.UpdateBid, req.BidAmount)
	}
	if req.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: 9999}) {
		t.Fatalf("returned re-entry must apply invoice peer, got %+v", req.Peer)
	}
	if req.HideName != true || req.Message != "go win!" {
		t.Fatalf("returned re-entry must apply invoice attributes: hide=%v msg=%q", req.HideName, req.Message)
	}
}

// Возвращённая ставка без peer в инвойсе (tdesktop bid box): прежний
// получатель сохраняется.
func TestStarGiftAuctionReturnedBidReentryKeepsPeerWithoutInvoicePeer(t *testing.T) {
	service := &auctionBidUpdateRPCService{state: auctionBidUpdateState(false, true, 500)}
	r := New(Config{DC: 2}, Deps{Gifts: service}, zaptest.NewLogger(t), clock.System)

	if _, err := r.starGiftAuctionBidPaymentForm(context.Background(), 42,
		auctionBidUpdateInvoice(true, false, false, false)); err != nil {
		t.Fatalf("clean returned re-entry rejected: %v", err)
	}
}
