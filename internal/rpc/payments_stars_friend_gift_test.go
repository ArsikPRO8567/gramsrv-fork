package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type starsFriendGiftRPCStore struct {
	*memory.StarsStore
	issued    domain.StarsGiftPurchaseForm
	purchased domain.StarsGiftPurchaseRequest
	purchases int
}

func (s *starsFriendGiftRPCStore) IssueStarsGiftPurchaseForm(_ context.Context, form domain.StarsGiftPurchaseForm) (domain.StarsGiftPurchaseForm, error) {
	form.FormID = 70001
	s.issued = form
	return form, nil
}

func (s *starsFriendGiftRPCStore) PurchaseStarsGift(_ context.Context, req domain.StarsGiftPurchaseRequest) (domain.StarsGiftPurchaseResult, error) {
	s.purchased = req
	s.purchases++
	action := &domain.MessageServiceAction{Kind: domain.MessageServiceActionGiftStars, GiftStars: &domain.MessageGiftStarsAction{
		Currency: req.Currency, Amount: req.Amount, Stars: req.Stars,
		TransactionID: "stars-gift-test", BalanceAfter: 4321,
	}}
	sender := domain.Message{ID: 11, OwnerUserID: req.BuyerUserID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
		From: domain.Peer{Type: domain.PeerTypeUser, ID: req.BuyerUserID}, Out: true, Date: req.Date,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: action}}
	recipient := sender
	recipient.ID, recipient.OwnerUserID, recipient.Peer, recipient.Out = 12, req.RecipientUserID,
		domain.Peer{Type: domain.PeerTypeUser, ID: req.BuyerUserID}, false
	return domain.StarsGiftPurchaseResult{
		RecipientBalance: domain.StarsBalance{UserID: req.RecipientUserID, Balance: 4321},
		TransactionID:    "stars-gift-test",
		Send: domain.SendPrivateTextResult{
			SenderMessage: sender, RecipientMessage: recipient,
			SenderEvent:    domain.UpdateEvent{UserID: req.BuyerUserID, Type: domain.UpdateEventNewMessage, Pts: 5, PtsCount: 1, Date: req.Date, Message: sender},
			RecipientEvent: domain.UpdateEvent{UserID: req.RecipientUserID, Type: domain.UpdateEventNewMessage, Pts: 9, PtsCount: 1, Date: req.Date, Message: recipient},
		},
	}, nil
}

func starsFriendGiftTestRouter(t *testing.T) (*Router, *starsFriendGiftRPCStore, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	buyer, err := users.Create(ctx, domain.User{AccessHash: 8101, Phone: "+15558101", FirstName: "Buyer"})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 8102, Phone: "+15558102", FirstName: "Recipient"})
	if err != nil {
		t.Fatal(err)
	}
	st := &starsFriendGiftRPCStore{StarsStore: memory.NewStarsStore()}
	r := New(Config{DC: 2}, Deps{
		Users: appusers.NewService(users),
		Stars: appstars.NewService(st, appstars.WithStartingGrant(0), appstars.WithGiftPurchaseStore(st)),
	}, zaptest.NewLogger(t), clock.System)
	return r, st, buyer, recipient
}

func TestStarsFriendGiftOptionsFormAndBothSettlementMethods(t *testing.T) {
	r, st, buyer, recipient := starsFriendGiftTestRouter(t)
	ctx := WithUserID(context.Background(), buyer.ID)

	generic, err := r.onPaymentsGetStarsGiftOptions(ctx, &tg.PaymentsGetStarsGiftOptionsRequest{})
	if err != nil || len(generic) != 3 || generic[0].Stars != 1000 || generic[0].Currency != "USD" || generic[0].Amount != 99 {
		t.Fatalf("generic gift options = %+v err=%v", generic, err)
	}
	input := &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}
	personalReq := &tg.PaymentsGetStarsGiftOptionsRequest{}
	personalReq.SetUserID(input)
	personal, err := r.onPaymentsGetStarsGiftOptions(ctx, personalReq)
	if err != nil || len(personal) != len(generic) {
		t.Fatalf("personal gift options = %+v err=%v", personal, err)
	}

	purpose := &tg.InputStorePaymentStarsGift{UserID: input, Stars: 2500, Currency: "USD", Amount: 199}
	invoice := &tg.InputInvoiceStars{Purpose: purpose}
	formClass, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: invoice})
	if err != nil {
		t.Fatalf("get gift payment form: %v", err)
	}
	form, ok := formClass.(*tg.PaymentsPaymentFormStars)
	if !ok || form.FormID != 70001 || form.Invoice.Currency != "XTR" || len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != 2500 {
		t.Fatalf("gift payment form = %T %+v", formClass, formClass)
	}
	if st.issued.BuyerUserID != buyer.ID || st.issued.RecipientUserID != recipient.ID || st.issued.Stars != 2500 || st.issued.ExpiresAt != st.issued.IssuedAt+600 {
		t.Fatalf("issued form = %+v", st.issued)
	}

	resultClass, err := r.onPaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: form.FormID, Invoice: invoice})
	if err != nil {
		t.Fatalf("sendStarsForm gift: %v", err)
	}
	result, ok := resultClass.(*tg.PaymentsPaymentResult)
	if !ok {
		t.Fatalf("sendStarsForm result = %T", resultClass)
	}
	updates, ok := result.Updates.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("sender updates = %T %+v", result.Updates, result.Updates)
	}
	newMessage, ok := updates.Updates[0].(*tg.UpdateNewMessage)
	if !ok || newMessage.Pts != 5 || newMessage.PtsCount != 1 {
		t.Fatalf("sender new message = %T %+v", updates.Updates[0], updates.Updates[0])
	}
	serviceMessage, ok := newMessage.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("gift message = %T", newMessage.Message)
	}
	action, ok := serviceMessage.Action.(*tg.MessageActionGiftStars)
	if !ok || action.Stars != 2500 || action.Currency != "USD" || action.Amount != 199 || action.TransactionID != "" {
		t.Fatalf("sender gift action = %T %+v", serviceMessage.Action, serviceMessage.Action)
	}
	if st.purchased.FormID != form.FormID || st.purchased.RecipientUserID != recipient.ID || st.purchases != 1 {
		t.Fatalf("purchase request = %+v count=%d", st.purchased, st.purchases)
	}

	credentials := &tg.InputPaymentCredentials{Data: tg.DataJSON{Data: "{}"}}
	if _, err := r.onPaymentsSendPaymentForm(ctx, &tg.PaymentsSendPaymentFormRequest{
		FormID: form.FormID, Invoice: invoice, Credentials: credentials,
	}); err != nil {
		t.Fatalf("sendPaymentForm gift: %v", err)
	}
	if st.purchases != 2 {
		t.Fatalf("settlement method count = %d, want 2 fake invocations", st.purchases)
	}
}

func TestStarsFriendGiftRejectsInvalidRecipientAndPackageBeforeStore(t *testing.T) {
	r, st, buyer, recipient := starsFriendGiftTestRouter(t)
	ctx := WithUserID(context.Background(), buyer.ID)

	badRecipientReq := &tg.PaymentsGetStarsGiftOptionsRequest{}
	badRecipientReq.SetUserID(&tg.InputUser{UserID: recipient.ID + 99999})
	if _, err := r.onPaymentsGetStarsGiftOptions(ctx, badRecipientReq); !tgerr.Is(err, "USER_ID_INVALID") {
		t.Fatalf("bad recipient err = %v", err)
	}
	bad := &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsGift{
		UserID: &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Stars:  2500, Currency: "USD", Amount: 200,
	}}
	if _, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: bad}); !tgerr.Is(err, "STARS_FORM_AMOUNT_MISMATCH") {
		t.Fatalf("tampered package form err = %v", err)
	}
	if _, err := r.onPaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: 70001, Invoice: bad}); !tgerr.Is(err, "STARS_FORM_AMOUNT_MISMATCH") {
		t.Fatalf("tampered package settle err = %v", err)
	}
	if st.issued.FormID != 0 || st.purchases != 0 {
		t.Fatalf("invalid request reached store: issued=%+v purchases=%d", st.issued, st.purchases)
	}
}

func TestGiftStarsRecipientProjectionCarriesBalanceOnlineAndDifference(t *testing.T) {
	action := &domain.MessageServiceAction{Kind: domain.MessageServiceActionGiftStars, GiftStars: &domain.MessageGiftStarsAction{
		Currency: "USD", Amount: 99, Stars: 1000, TransactionID: "txn-1", BalanceAfter: 3100,
	}}
	msg := domain.Message{ID: 4, OwnerUserID: 2, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1},
		From: domain.Peer{Type: domain.PeerTypeUser, ID: 1}, Date: 1700000000,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: action}}
	event := domain.UpdateEvent{UserID: 2, Type: domain.UpdateEventNewMessage, Pts: 8, PtsCount: 1, Date: msg.Date, Message: msg}
	online := tgPrivateMessageUpdates(event, msg, 0, false, nil, nil)
	if len(online.Updates) != 2 {
		t.Fatalf("online updates = %+v", online.Updates)
	}
	balance, ok := online.Updates[1].(*tg.UpdateStarsBalance)
	if !ok || balance.Balance.(*tg.StarsAmount).Amount != 3100 {
		t.Fatalf("online balance = %T %+v", online.Updates[1], online.Updates[1])
	}
	diff := tgUpdatesDifference(2, domain.UpdateDifference{Events: []domain.UpdateEvent{event}, State: domain.UpdateState{Pts: 8}})
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok || len(full.NewMessages) != 1 || len(full.OtherUpdates) != 1 {
		t.Fatalf("difference = %T %+v", diff, diff)
	}
	if _, ok := full.OtherUpdates[0].(*tg.UpdateStarsBalance); !ok {
		t.Fatalf("difference balance = %T", full.OtherUpdates[0])
	}
}
