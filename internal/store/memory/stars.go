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

// TotalReceived — «Всего поступлений» (раунд 47): сумма кредитных проводок
// (amount>0) личного леджера; семантика совпадает с postgres-версией.
func (s *StarsStore) TotalReceived(_ context.Context, userID int64) (int64, error) {
        if userID == 0 {
                return 0, nil
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        st := s.states[userID]
        if st == nil {
                return 0, nil
        }
        var total int64
        for _, t := range st.txns {
                if t.Amount > 0 {
                        total += t.Amount
                }
        }
        return total, nil
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
                Processing:   flags.Processing,
                Failed:       flags.Failed,
        }
        txn.TransactionID = domain.NewStarsTransactionID(false)
        st.txns = append(st.txns, txn)
}

// RefundPaidMessages гасит непогашенные проводки платных сообщений payer'а в
// пользу owner'а (memory-зеркало postgres-семантики): брутто возвращается
// плательщику, полученная выручка удерживается с клампом к остатку, исходные
// проводки помечаются refunded=true (защита от повторного возврата).
func (s *StarsStore) RefundPaidMessages(_ context.Context, ownerUserID, payerUserID int64, date int) (domain.PaidMessagesRefund, error) {
        if ownerUserID == 0 || payerUserID == 0 || ownerUserID == payerUserID {
                return domain.PaidMessagesRefund{}, domain.ErrStarsInvalidAmount
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        payer := s.states[payerUserID]
        var gross int64
        var charges int
        if payer != nil {
                for _, t := range payer.txns {
                        if t.Reason == domain.StarsReasonPaidMessage && t.Amount < 0 &&
                                t.Peer.Type == domain.PeerTypeUser && t.Peer.ID == ownerUserID && !t.Refunded {
                                gross += -t.Amount
                                charges++
                        }
                }
        }
        if gross <= 0 {
                return domain.PaidMessagesRefund{}, nil
        }
        var received int64
        if owner := s.states[ownerUserID]; owner != nil {
                for _, t := range owner.txns {
                        if t.Reason == domain.StarsReasonPaidMessage && t.Amount > 0 &&
                                t.Peer.Type == domain.PeerTypeUser && t.Peer.ID == payerUserID && !t.Refunded {
                                received += t.Amount
                        }
                }
        }
        for i := range payer.txns {
                t := &payer.txns[i]
                if t.Reason == domain.StarsReasonPaidMessage && t.Amount < 0 &&
                        t.Peer.Type == domain.PeerTypeUser && t.Peer.ID == ownerUserID && !t.Refunded {
                        t.Refunded = true
                }
        }
        if payer != nil {
                payer.balance += gross
                s.appendTxnFlags(payer, payerUserID, gross, domain.StarsReasonPaidMessage,
                        domain.Peer{Type: domain.PeerTypeUser, ID: ownerUserID}, date, "", "",
                        domain.StarsLedgerFlags{Refunded: true})
        }
        claw := int64(0)
        if received > 0 {
                for i := range s.states[ownerUserID].txns {
                        t := &s.states[ownerUserID].txns[i]
                        if t.Reason == domain.StarsReasonPaidMessage && t.Amount > 0 &&
                                t.Peer.Type == domain.PeerTypeUser && t.Peer.ID == payerUserID && !t.Refunded {
                                t.Refunded = true
                        }
                }
                owner := s.states[ownerUserID]
                claw = received
                if owner != nil && owner.balance < claw {
                        claw = owner.balance
                }
                if owner != nil && claw > 0 {
                        owner.balance -= claw
                        s.appendTxnFlags(owner, ownerUserID, -claw, domain.StarsReasonPaidMessage,
                                domain.Peer{Type: domain.PeerTypeUser, ID: payerUserID}, date, "", "",
                                domain.StarsLedgerFlags{Refunded: true})
                }
        }
        return domain.PaidMessagesRefund{Refunded: gross, ClawedBack: claw, Charges: charges}, nil
}

// PaidMessagesRevenue возвращает непогашенную сумму платежей payer'а за
// сообщения в адрес peer'а (memory-зеркало postgres-семантики).
func (s *StarsStore) PaidMessagesRevenue(_ context.Context, payerUserID int64, peer domain.Peer) (int64, error) {
        if payerUserID == 0 || peer.ID == 0 {
                return 0, nil
        }
        s.mu.Lock()
        defer s.mu.Unlock()
        st := s.states[payerUserID]
        if st == nil {
                return 0, nil
        }
        var total int64
        for _, t := range st.txns {
                if t.Reason == domain.StarsReasonPaidMessage && t.Amount < 0 &&
                        t.Peer.Type == peer.Type && t.Peer.ID == peer.ID && !t.Refunded {
                        total += -t.Amount
                }
        }
        return total, nil
}
