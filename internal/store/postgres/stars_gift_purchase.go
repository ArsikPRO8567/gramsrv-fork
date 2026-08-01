package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// StarsGiftPurchaseStore commits a fiat Stars gift as one aggregate with the
// private service message. No external provider is contacted by this local
// development checkout; form binding and settlement idempotency are still
// production-shaped so retries cannot mint twice.
type StarsGiftPurchaseStore struct {
	db       sqlcgen.DBTX
	messages *MessageStore
}

func NewStarsGiftPurchaseStore(db sqlcgen.DBTX, messages *MessageStore) *StarsGiftPurchaseStore {
	return &StarsGiftPurchaseStore{db: db, messages: messages}
}

func (s *StarsGiftPurchaseStore) IssueStarsGiftPurchaseForm(ctx context.Context, form domain.StarsGiftPurchaseForm) (domain.StarsGiftPurchaseForm, error) {
	if s == nil || s.db == nil || form.BuyerUserID <= 0 || form.RecipientUserID <= 0 ||
		form.BuyerUserID == form.RecipientUserID || form.Stars <= 0 || form.Amount <= 0 ||
		len(form.Currency) != 3 || form.IssuedAt <= 0 || form.ExpiresAt != form.IssuedAt+600 {
		return domain.StarsGiftPurchaseForm{}, domain.ErrStarsGiftFormInvalid
	}
	for attempt := 0; attempt < 8; attempt++ {
		formID, err := newStarsGiftFormID()
		if err != nil {
			return domain.StarsGiftPurchaseForm{}, fmt.Errorf("generate stars gift form id: %w", err)
		}
		tag, err := s.db.Exec(ctx, `
INSERT INTO stars_gift_purchase_forms
    (buyer_user_id,form_id,recipient_user_id,stars,currency,amount,issued_at,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT DO NOTHING`, form.BuyerUserID, formID, form.RecipientUserID, form.Stars,
			form.Currency, form.Amount, form.IssuedAt, form.ExpiresAt)
		if err != nil {
			return domain.StarsGiftPurchaseForm{}, fmt.Errorf("insert stars gift form: %w", err)
		}
		if tag.RowsAffected() == 1 {
			form.FormID = formID
			return form, nil
		}
	}
	return domain.StarsGiftPurchaseForm{}, domain.ErrStarsGiftUnavailable
}

var errStarsGiftPurchaseReplay = errors.New("stars gift purchase replay")

func (s *StarsGiftPurchaseStore) PurchaseStarsGift(ctx context.Context, req domain.StarsGiftPurchaseRequest) (domain.StarsGiftPurchaseResult, error) {
	if s == nil || s.db == nil || s.messages == nil || req.FormID == 0 ||
		req.BuyerUserID <= 0 || req.RecipientUserID <= 0 || req.BuyerUserID == req.RecipientUserID ||
		req.Stars <= 0 || req.Amount <= 0 || len(req.Currency) != 3 || req.Date <= 0 {
		return domain.StarsGiftPurchaseResult{}, domain.ErrStarsGiftFormInvalid
	}
	fingerprint := starsGiftPurchaseFingerprint(req)
	if replay, found, err := s.loadStarsGiftPurchaseReplay(ctx, req, fingerprint); err != nil || found {
		return replay, err
	}

	transactionID := fmt.Sprintf("stars-gift:%d:%d", req.BuyerUserID, req.FormID)
	randomID := lifecycleCommandRandomID("stars-fiat-gift", req.BuyerUserID, req.FormID)
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
		Kind: domain.MessageServiceActionGiftStars,
		GiftStars: &domain.MessageGiftStarsAction{Currency: req.Currency, Amount: req.Amount,
			Stars: req.Stars, TransactionID: transactionID},
	}}
	messageReq := domain.SendPrivateTextRequest{
		SenderUserID: req.BuyerUserID, RecipientUserID: req.RecipientUserID,
		RandomID: randomID, Date: req.Date, Media: media,
		OriginUserID: req.BuyerUserID, OriginAuthKeyID: req.OriginAuthKeyID,
		OriginSessionID: req.OriginSessionID, IdempotencyFingerprint: fingerprint[:],
	}
	result := domain.StarsGiftPurchaseResult{TransactionID: transactionID}
	hooks := privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, send *domain.SendPrivateTextRequest) error {
			if err := validateStarsGiftPurchaseForm(ctx, tx, req, true); err != nil {
				return err
			}
			if found, err := starsGiftPurchaseCommandExists(ctx, tx, req.BuyerUserID, req.FormID); err != nil {
				return err
			} else if found {
				return errStarsGiftPurchaseReplay
			}
			balance := domain.StarsBalance{UserID: req.RecipientUserID}
			if err := tx.QueryRow(ctx, `
INSERT INTO stars_balances (user_id,balance,updated_at) VALUES($1,$2,now())
ON CONFLICT (user_id) DO UPDATE
SET balance=stars_balances.balance+EXCLUDED.balance, updated_at=now()
RETURNING balance,granted`, req.RecipientUserID, req.Stars).Scan(&balance.Balance, &balance.Granted); err != nil {
				return fmt.Errorf("credit stars gift recipient: %w", err)
			}
			if err := insertStarsTxn(ctx, tx, req.RecipientUserID, req.Stars, domain.StarsReasonGift,
				domain.Peer{Type: domain.PeerTypeUser, ID: req.BuyerUserID}, req.Date,
				"Stars gift", fmt.Sprintf("%d Stars", req.Stars)); err != nil {
				return err
			}
			result.RecipientBalance = balance
			if send.Media == nil || send.Media.ServiceAction == nil || send.Media.ServiceAction.GiftStars == nil {
				return domain.ErrStarsGiftFormInvalid
			}
			send.Media.ServiceAction.GiftStars.BalanceAfter = balance.Balance
			return nil
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			_, err := tx.Exec(ctx, `
INSERT INTO stars_gift_purchase_commands
    (buyer_user_id,form_id,request_fingerprint,recipient_user_id,stars,currency,amount,
     recipient_balance_after,transaction_id,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, req.BuyerUserID, req.FormID, fingerprint[:],
				req.RecipientUserID, req.Stars, req.Currency, req.Amount,
				result.RecipientBalance.Balance, transactionID, req.Date)
			if err != nil {
				return fmt.Errorf("insert stars gift purchase command: %w", err)
			}
			result.Send = sent
			return nil
		},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		if errors.Is(err, errStarsGiftPurchaseReplay) {
			if replay, found, replayErr := s.loadStarsGiftPurchaseReplay(ctx, req, fingerprint); replayErr != nil || found {
				return replay, replayErr
			}
		}
		return domain.StarsGiftPurchaseResult{}, err
	}
	result.Send = sent
	return result, nil
}

func validateStarsGiftPurchaseForm(ctx context.Context, db sqlcgen.DBTX, req domain.StarsGiftPurchaseRequest, lock bool) error {
	query := `SELECT recipient_user_id,stars,currency,amount,issued_at,expires_at
FROM stars_gift_purchase_forms WHERE buyer_user_id=$1 AND form_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var recipientID, stars, amount int64
	var currency string
	var issuedAt, expiresAt int
	err := db.QueryRow(ctx, query, req.BuyerUserID, req.FormID).
		Scan(&recipientID, &stars, &currency, &amount, &issuedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrStarsGiftFormInvalid
	}
	if err != nil {
		return fmt.Errorf("load stars gift form: %w", err)
	}
	if req.Date >= expiresAt {
		return domain.ErrStarsGiftFormExpired
	}
	if issuedAt <= 0 || recipientID != req.RecipientUserID || stars != req.Stars ||
		currency != req.Currency || amount != req.Amount {
		return domain.ErrStarsGiftFormInvalid
	}
	return nil
}

func (s *StarsGiftPurchaseStore) loadStarsGiftPurchaseReplay(ctx context.Context, req domain.StarsGiftPurchaseRequest, fingerprint [32]byte) (domain.StarsGiftPurchaseResult, bool, error) {
	var recipientID, stars, amount, balance int64
	var currency, transactionID string
	var storedFingerprint []byte
	err := s.db.QueryRow(ctx, `
SELECT request_fingerprint,recipient_user_id,stars,currency,amount,recipient_balance_after,transaction_id
FROM stars_gift_purchase_commands WHERE buyer_user_id=$1 AND form_id=$2`, req.BuyerUserID, req.FormID).
		Scan(&storedFingerprint, &recipientID, &stars, &currency, &amount, &balance, &transactionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarsGiftPurchaseResult{}, false, nil
	}
	if err != nil {
		return domain.StarsGiftPurchaseResult{}, false, fmt.Errorf("load stars gift purchase replay: %w", err)
	}
	if !bytes.Equal(storedFingerprint, fingerprint[:]) || recipientID != req.RecipientUserID ||
		stars != req.Stars || currency != req.Currency || amount != req.Amount || transactionID == "" {
		return domain.StarsGiftPurchaseResult{}, false, domain.ErrStarsGiftFormInvalid
	}
	sent, found, err := s.messages.LookupPrivateSendReplay(ctx, domain.PrivateSendReplayRequest{
		SenderUserID: req.BuyerUserID, RecipientUserID: req.RecipientUserID,
		RandomID:               lifecycleCommandRandomID("stars-fiat-gift", req.BuyerUserID, req.FormID),
		IdempotencyFingerprint: fingerprint[:],
	})
	if err != nil {
		return domain.StarsGiftPurchaseResult{}, false, err
	}
	if !found {
		return domain.StarsGiftPurchaseResult{}, false, domain.ErrStarsGiftFormInvalid
	}
	return domain.StarsGiftPurchaseResult{
		RecipientBalance: domain.StarsBalance{UserID: req.RecipientUserID, Balance: balance},
		Send:             sent, TransactionID: transactionID, Duplicate: true,
	}, true, nil
}

func starsGiftPurchaseCommandExists(ctx context.Context, db sqlcgen.DBTX, buyerUserID, formID int64) (bool, error) {
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM stars_gift_purchase_commands WHERE buyer_user_id=$1 AND form_id=$2)`, buyerUserID, formID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check stars gift purchase command: %w", err)
	}
	return exists, nil
}

func starsGiftPurchaseFingerprint(req domain.StarsGiftPurchaseRequest) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("telesrv:stars-fiat-gift:v1:%d:%d:%d:%s:%d:%d",
		req.BuyerUserID, req.RecipientUserID, req.Stars, req.Currency, req.Amount, req.FormID)))
}

func newStarsGiftFormID() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	id := int64(binary.LittleEndian.Uint64(raw[:]) & 0x7fffffffffffffff)
	if id == 0 {
		return 1, nil
	}
	return id, nil
}

var _ store.StarsGiftPurchaseStore = (*StarsGiftPurchaseStore)(nil)
