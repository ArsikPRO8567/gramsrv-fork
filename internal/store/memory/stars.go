package memory

import (
	"context"
	"sync"

	"telesrv/internal/domain"
)

// StarsStore 是 store.StarsStore 的内存实现，复刻 postgres 版的原子语义
// （在单个互斥锁下完成读-检查-写，等价于 SELECT ... FOR UPDATE）。
type StarsStore struct {
	mu     sync.Mutex
	states map[int64]*starsState
	nextID int64
}

type starsState struct {
	balance int64
	granted bool
	txns    []domain.StarsTransaction // 追加序，读时倒序
}

// NewStarsStore 创建内存 StarsStore。
func NewStarsStore() *StarsStore {
	return &StarsStore{states: make(map[int64]*starsState)}
}

func (s *StarsStore) GetBalance(_ context.Context, userID int64) (domain.StarsBalance, error) {
	if userID == 0 {
		return domain.StarsBalance{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[userID]
	if st == nil {
		return domain.StarsBalance{UserID: userID}, nil
	}
	return domain.StarsBalance{UserID: userID, Balance: st.balance, Granted: st.granted}, nil
}

func (s *StarsStore) EnsureGrant(_ context.Context, userID, amount int64, date int) (domain.StarsBalance, bool, error) {
	if userID == 0 {
		return domain.StarsBalance{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[userID]
	if st == nil {
		st = &starsState{}
		s.states[userID] = st
	}
	if amount <= 0 {
		return domain.StarsBalance{UserID: userID, Balance: st.balance, Granted: st.granted}, false, nil
	}
	if st.granted {
		return domain.StarsBalance{UserID: userID, Balance: st.balance, Granted: true}, false, nil
	}
	st.balance += amount
	st.granted = true
	s.appendTxn(st, userID, amount, domain.StarsReasonGrant, domain.Peer{}, date, "", "")
	return domain.StarsBalance{UserID: userID, Balance: st.balance, Granted: true}, true, nil
}

func (s *StarsStore) Credit(_ context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) (domain.StarsBalance, error) {
	return s.CreditFlags(context.Background(), userID, amount, reason, peer, date, title, desc, domain.StarsLedgerFlags{})
}

// CreditFlags 贷记并记录官方投影属性（与 postgres 版语义一致）。
func (s *StarsStore) CreditFlags(_ context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string, flags domain.StarsLedgerFlags) (domain.StarsBalance, error) {
	if userID == 0 || amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[userID]
	if st == nil {
		st = &starsState{}
		s.states[userID] = st
	}
	st.balance += amount
	s.appendTxnFlags(st, userID, amount, reason, peer, date, title, desc, flags)
	return domain.StarsBalance{UserID: userID, Balance: st.balance, Granted: st.granted}, nil
}

func (s *StarsStore) Debit(_ context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) (domain.StarsBalance, error) {
	return s.DebitFlags(context.Background(), userID, amount, reason, peer, date, title, desc, domain.StarsLedgerFlags{})
}

// DebitFlags 借记并记录官方投影属性（与 postgres 版语义一致）。
func (s *StarsStore) DebitFlags(_ context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string, flags domain.StarsLedgerFlags) (domain.StarsBalance, error) {
	if userID == 0 || amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[userID]
	if st == nil || st.balance < amount {
		return domain.StarsBalance{}, domain.ErrStarsInsufficient
	}
	st.balance -= amount
	s.appendTxnFlags(st, userID, -amount, reason, peer, date, title, desc, flags)
	return domain.StarsBalance{UserID: userID, Balance: st.balance, Granted: st.granted}, nil
}

func (s *StarsStore) ListTransactions(_ context.Context, userID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error) {
	if userID == 0 {
		return domain.StarsTransactionPage{}, nil
	}
	query, err := domain.NormalizeStarsTransactionQuery(query)
	if err != nil {
		return domain.StarsTransactionPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[userID]
	if st == nil {
		return domain.StarsTransactionPage{}, nil
	}
	page := domain.StarsTransactionPage{Balance: st.balance}
	cursor, hasCursor := domain.DecodeStarsCursor(query.Offset)
	out := make([]domain.StarsTransaction, 0, query.Limit+1)
	if query.Ascending {
		for i := 0; i < len(st.txns) && len(out) <= query.Limit; i++ {
			if starsTxnMatchesCursor(st.txns[i], cursor, hasCursor, query) {
				out = append(out, st.txns[i])
			}
		}
	} else {
		for i := len(st.txns) - 1; i >= 0 && len(out) <= query.Limit; i-- {
			if starsTxnMatchesCursor(st.txns[i], cursor, hasCursor, query) {
				out = append(out, st.txns[i])
			}
		}
	}
	if len(out) > query.Limit {
		out = out[:query.Limit]
		page.NextOffset = domain.EncodeStarsCursor(out[len(out)-1].ID)
	}
	page.Transactions = out
	return page, nil
}

// starsTxnMatchesCursor 复刻原 appendMatch 谓词：游标与方向过滤。
func starsTxnMatchesCursor(t domain.StarsTransaction, cursor int64, hasCursor bool, query domain.StarsTransactionQuery) bool {
	if hasCursor {
		if query.Ascending && t.ID <= cursor {
			return false
		}
		if !query.Ascending && t.ID >= cursor {
			return false
		}
	}
	return query.Direction.IncludesAmount(t.Amount)
}

// ListTransactionsByID 按公开交易 ID 过滤本人流水（内存实现）。
func (s *StarsStore) ListTransactionsByID(_ context.Context, userID int64, ids []string) ([]domain.StarsTransaction, error) {
	if userID == 0 || len(ids) == 0 {
		return nil, nil
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[userID]
	if st == nil {
		return nil, nil
	}
	out := make([]domain.StarsTransaction, 0, len(ids))
	for _, t := range st.txns {
		if _, ok := wanted[t.PublicID()]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *StarsStore) appendTxn(st *starsState, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) {
	s.appendTxnFlags(st, userID, amount, reason, peer, date, title, desc, domain.StarsLedgerFlags{})
}

func (s *StarsStore) appendTxnFlags(st *starsState, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string, flags domain.StarsLedgerFlags) {
	s.nextID++
	txn := domain.StarsTransaction{
		ID:           s.nextID,
		UserID:       userID,
		Peer:         peer,
		Amount:       amount,
		Date:         date,
		Reason:       reason,
		Title:        title,
		Description:  desc,
		Refunded:     flags.Refunded,
		GiftID:       flags.GiftID,
		GiftIsUnique: flags.GiftIsUnique,
	}
	// 共享公开 ID（问题 C）：配对转账两侧行写入同一个 stars_… 标识。
	if flags.TransactionID != "" {
		txn.TransactionID = flags.TransactionID
	} else {
		txn.TransactionID = domain.NewStarsTransactionID(false)
	}
	st.txns = append(st.txns, txn)
}

// TransferToUser 在单个互斥锁段内执行账户对账户的 Stars 转账（与 postgres 版
// 语义一致）：借记发送方、贷记接收方，两条流水共享同一个公开交易 ID。余额
// 不足返回 domain.ErrStarsInsufficient。
func (s *StarsStore) TransferToUser(_ context.Context, req domain.StarsPeerTransferRequest) (domain.StarsPeerTransferResult, error) {
	if req.SenderUserID <= 0 || req.RecipientUserID <= 0 || req.Amount <= 0 || req.Reason == "" || req.Date <= 0 ||
		req.SenderUserID == req.RecipientUserID {
		return domain.StarsPeerTransferResult{}, domain.ErrStarsInvalidAmount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sender := s.states[req.SenderUserID]
	if sender == nil || sender.balance < req.Amount {
		return domain.StarsPeerTransferResult{}, domain.ErrStarsInsufficient
	}
	recipient := s.states[req.RecipientUserID]
	if recipient == nil {
		recipient = &starsState{}
		s.states[req.RecipientUserID] = recipient
	}
	sender.balance -= req.Amount
	recipient.balance += req.Amount
	shared := domain.NewStarsTransactionID(false)
	s.appendTxnFlags(sender, req.SenderUserID, -req.Amount, req.Reason,
		domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID}, req.Date, "", "",
		domain.StarsLedgerFlags{TransactionID: shared})
	s.appendTxnFlags(recipient, req.RecipientUserID, req.Amount, req.Reason,
		domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}, req.Date, "", "",
		domain.StarsLedgerFlags{TransactionID: shared})
	return domain.StarsPeerTransferResult{TransactionID: shared,
		SenderBalance: sender.balance, RecipientBalance: recipient.balance}, nil
}

// RefundPeerTransfer 原路退回一笔账户对账户转账（两侧行携带 refund 标志，
// 共享一个新的公开 ID）。语义与 postgres 版一致。
func (s *StarsStore) RefundPeerTransfer(_ context.Context, req domain.StarsPeerTransferRequest) (domain.StarsPeerTransferResult, error) {
	if req.SenderUserID <= 0 || req.RecipientUserID <= 0 || req.Amount <= 0 || req.Reason == "" || req.Date <= 0 ||
		req.SenderUserID == req.RecipientUserID {
		return domain.StarsPeerTransferResult{}, domain.ErrStarsInvalidAmount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	recipient := s.states[req.RecipientUserID]
	if recipient == nil || recipient.balance < req.Amount {
		return domain.StarsPeerTransferResult{}, domain.ErrStarsInsufficient
	}
	sender := s.states[req.SenderUserID]
	if sender == nil {
		sender = &starsState{}
		s.states[req.SenderUserID] = sender
	}
	recipient.balance -= req.Amount
	sender.balance += req.Amount
	shared := domain.NewStarsTransactionID(false)
	flags := domain.StarsLedgerFlags{TransactionID: shared, Refunded: true}
	s.appendTxnFlags(recipient, req.RecipientUserID, -req.Amount, req.Reason,
		domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}, req.Date, "", "", flags)
	s.appendTxnFlags(sender, req.SenderUserID, req.Amount, req.Reason,
		domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID}, req.Date, "", "", flags)
	return domain.StarsPeerTransferResult{TransactionID: shared,
		SenderBalance: sender.balance, RecipientBalance: recipient.balance}, nil
}
