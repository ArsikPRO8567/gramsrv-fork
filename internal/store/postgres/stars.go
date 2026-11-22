package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// StarsStore 用 PostgreSQL 实现 store.StarsStore（Stars 本地账本）。
// 借记/贷记/授予各自在单事务内完成：余额与流水永不漂移。
type StarsStore struct {
	db sqlcgen.DBTX
}

// NewStarsStore 基于 pgx 连接池（或事务）创建 StarsStore。
func NewStarsStore(db sqlcgen.DBTX) *StarsStore {
	return &StarsStore{db: db}
}

func (s *StarsStore) GetBalance(ctx context.Context, userID int64) (domain.StarsBalance, error) {
	if userID == 0 {
		return domain.StarsBalance{}, nil
	}
	bal := domain.StarsBalance{UserID: userID}
	err := s.db.QueryRow(ctx, `SELECT balance, granted FROM stars_balances WHERE user_id = $1`, userID).
		Scan(&bal.Balance, &bal.Granted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StarsBalance{UserID: userID}, nil
		}
		return domain.StarsBalance{}, fmt.Errorf("get stars balance: %w", err)
	}
	return bal, nil
}

func (s *StarsStore) EnsureGrant(ctx context.Context, userID, amount int64, date int) (domain.StarsBalance, bool, error) {
	if userID == 0 {
		return domain.StarsBalance{}, false, nil
	}
	if amount <= 0 {
		// 授予额为 0 时只确保有一行且 granted=true（幂等关闭授予）。
		bal, err := s.GetBalance(ctx, userID)
		return bal, false, err
	}
	out := domain.StarsBalance{UserID: userID}
	applied := false
	err := withTx(ctx, s.db, "ensure stars grant", func(tx pgx.Tx) error {
		var balance int64
		var granted bool
		err := tx.QueryRow(ctx, `SELECT balance, granted FROM stars_balances WHERE user_id = $1 FOR UPDATE`, userID).
			Scan(&balance, &granted)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := tx.Exec(ctx, `INSERT INTO stars_balances (user_id, balance, granted, updated_at) VALUES ($1, $2, true, now())`,
				userID, amount); err != nil {
				return fmt.Errorf("insert stars balance grant: %w", err)
			}
			out.Balance, out.Granted, applied = amount, true, true
		case err != nil:
			return fmt.Errorf("select stars balance for grant: %w", err)
		case granted:
			out.Balance, out.Granted = balance, true
			return nil
		default:
			if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance = balance + $2, granted = true, updated_at = now() WHERE user_id = $1 RETURNING balance`,
				userID, amount).Scan(&out.Balance); err != nil {
				return fmt.Errorf("update stars balance grant: %w", err)
			}
			out.Granted, applied = true, true
		}
		if _, err := insertStarsTxnFull(ctx, tx, userID, amount, domain.StarsReasonGrant, domain.Peer{}, date, "", "", domain.StarsLedgerFlags{}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return domain.StarsBalance{}, false, err
	}
	return out, applied, nil
}

func (s *StarsStore) Credit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) (domain.StarsBalance, error) {
	if userID == 0 || amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	out := domain.StarsBalance{UserID: userID}
	err := withTx(ctx, s.db, "credit stars", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
INSERT INTO stars_balances (user_id, balance, updated_at) VALUES ($1, $2, now())
ON CONFLICT (user_id) DO UPDATE SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()
RETURNING balance, granted`, userID, amount).Scan(&out.Balance, &out.Granted); err != nil {
			return fmt.Errorf("credit stars balance: %w", err)
		}
		_, err := insertStarsTxnFull(ctx, tx, userID, amount, reason, peer, date, title, desc, domain.StarsLedgerFlags{})
		return err
	})
	if err != nil {
		return domain.StarsBalance{}, err
	}
	return out, nil
}

// CreditFlags 贷记并写入官方投影属性（礼物载荷/退款标志）。amount>0；
// 其余语义同 Credit。
func (s *StarsStore) CreditFlags(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string, flags domain.StarsLedgerFlags) (domain.StarsBalance, error) {
	if userID == 0 || amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	out := domain.StarsBalance{UserID: userID}
	err := withTx(ctx, s.db, "credit stars with flags", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
INSERT INTO stars_balances (user_id, balance, updated_at) VALUES ($1, $2, now())
ON CONFLICT (user_id) DO UPDATE SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()
RETURNING balance, granted`, userID, amount).Scan(&out.Balance, &out.Granted); err != nil {
			return fmt.Errorf("credit stars balance: %w", err)
		}
		_, err := insertStarsTxnFull(ctx, tx, userID, amount, reason, peer, date, title, desc, flags)
		return err
	})
	if err != nil {
		return domain.StarsBalance{}, err
	}
	return out, nil
}

func (s *StarsStore) Debit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) (domain.StarsBalance, error) {
	return s.DebitFlags(ctx, userID, amount, reason, peer, date, title, desc, domain.StarsLedgerFlags{})
}

// DebitFlags 借记并写入官方投影属性（礼物载荷/退款标志）。余额不足返回
// domain.ErrStarsInsufficient；其余语义同 Debit。
func (s *StarsStore) DebitFlags(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string, flags domain.StarsLedgerFlags) (domain.StarsBalance, error) {
	if userID == 0 || amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	out := domain.StarsBalance{UserID: userID, Granted: true}
	err := withTx(ctx, s.db, "debit stars", func(tx pgx.Tx) error {
		var balance int64
		var granted bool
		err := tx.QueryRow(ctx, `SELECT balance, granted FROM stars_balances WHERE user_id = $1 FOR UPDATE`, userID).
			Scan(&balance, &granted)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && balance < amount) {
			return domain.ErrStarsInsufficient
		}
		if err != nil {
			return fmt.Errorf("select stars balance for debit: %w", err)
		}
		if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance = balance - $2, updated_at = now() WHERE user_id = $1 RETURNING balance`,
			userID, amount).Scan(&out.Balance); err != nil {
			return fmt.Errorf("update stars balance debit: %w", err)
		}
		out.Granted = granted
		if _, err := insertStarsTxnFull(ctx, tx, userID, -amount, reason, peer, date, title, desc, flags); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return domain.StarsBalance{}, err
	}
	return out, nil
}

func (s *StarsStore) ListTransactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error) {
	if userID == 0 {
		return domain.StarsTransactionPage{}, nil
	}
	query, err := domain.NormalizeStarsTransactionQuery(query)
	if err != nil {
		return domain.StarsTransactionPage{}, err
	}
	bal, err := s.GetBalance(ctx, userID)
	if err != nil {
		return domain.StarsTransactionPage{}, err
	}
	page := domain.StarsTransactionPage{Balance: bal.Balance}

	// keyset：方向过滤先于 LIMIT，多取一条以探测同一视图是否还有下一页。
	where, order, args := starsTransactionQueryParts("user_id", "amount", userID, query)
	rows, err := s.db.Query(ctx, `
SELECT id, COALESCE(transaction_id, ''), peer_type, peer_id, amount, reason, title, description, date,
       COALESCE(premium_payment_intent_id, 0), COALESCE(premium_recipient_user_id, 0),
       COALESCE(premium_months, 0), refunded, COALESCE(gift_id, 0), gift_is_unique
FROM stars_transactions
WHERE `+where+`
ORDER BY id `+order+`
LIMIT $2`, args...)
	if err != nil {
		return domain.StarsTransactionPage{}, fmt.Errorf("list stars transactions: %w", err)
	}
	defer rows.Close()
	txns := make([]domain.StarsTransaction, 0, query.Limit+1)
	for rows.Next() {
		var (
			t        domain.StarsTransaction
			peerType string
			peerID   int64
			reason   string
		)
		if err := rows.Scan(&t.ID, &t.TransactionID, &peerType, &peerID, &t.Amount, &reason, &t.Title, &t.Description, &t.Date,
			&t.PaymentID, &t.RecipientUserID, &t.PremiumMonths, &t.Refunded, &t.GiftID, &t.GiftIsUnique); err != nil {
			return domain.StarsTransactionPage{}, fmt.Errorf("scan stars transaction: %w", err)
		}
		t.UserID = userID
		t.Reason = domain.StarsTransactionReason(reason)
		if peerType != "" {
			t.Peer = domain.Peer{Type: domain.PeerType(peerType), ID: peerID}
		}
		txns = append(txns, t)
	}
	if err := rows.Err(); err != nil {
		return domain.StarsTransactionPage{}, fmt.Errorf("iterate stars transactions: %w", err)
	}
	if len(txns) > query.Limit {
		txns = txns[:query.Limit]
		page.NextOffset = domain.EncodeStarsCursor(txns[len(txns)-1].ID)
	}
	page.Transactions = txns
	return page, nil
}

// starsTransactionQueryParts centralizes the sign predicate and keyset
// direction for personal/channel Stars and TON ledgers. Column names are only
// package-owned constants; client values remain bind parameters.
func starsTransactionQueryParts(ownerColumn, amountColumn string, ownerID int64, query domain.StarsTransactionQuery) (string, string, []any) {
	where := ownerColumn + "=$1"
	switch query.Direction {
	case domain.StarsTransactionDirectionIncoming:
		where += " AND " + amountColumn + ">0"
	case domain.StarsTransactionDirectionOutgoing:
		where += " AND " + amountColumn + "<0"
	}
	order, comparator := "DESC", "<"
	if query.Ascending {
		order, comparator = "ASC", ">"
	}
	args := []any{ownerID, query.Limit + 1}
	if cursor, ok := domain.DecodeStarsCursor(query.Offset); ok {
		where += " AND id" + comparator + "$3"
		args = append(args, cursor)
	}
	return where, order, args
}

// insertStarsTxn 在事务内写一条流水（amount 带符号），无礼物载荷/退款标志。
func insertStarsTxn(ctx context.Context, tx pgx.Tx, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) error {
	_, err := insertStarsTxnFull(ctx, tx, userID, amount, reason, peer, date, title, desc, domain.StarsLedgerFlags{})
	return err
}

// insertStarsTxnFull 在事务内写一条带官方投影属性的流水，返回账本行 id
// （礼物在写入点尚未定型时，调用方可随后 UPDATE gift_id/gift_is_unique —
// 升级流就是这样把目录礼物替换为产出的唯一礼物的）。公开交易 ID 在此生成。
func insertStarsTxnFull(ctx context.Context, tx pgx.Tx, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string, flags domain.StarsLedgerFlags) (int64, error) {
	var giftID any
	if flags.GiftID > 0 {
		giftID = flags.GiftID
	}
	var rowID int64
	if err := tx.QueryRow(ctx, `
INSERT INTO stars_transactions (user_id, peer_type, peer_id, amount, reason, title, description, date,
        transaction_id, refunded, gift_id, gift_is_unique)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id`,
		userID, string(peer.Type), peer.ID, amount, string(reason), title, desc, date,
		domain.NewStarsTransactionID(false), flags.Refunded, giftID, flags.GiftIsUnique).Scan(&rowID); err != nil {
		return 0, fmt.Errorf("insert stars transaction: %w", err)
	}
	return rowID, nil
}

// ListTransactionsByID 按公开交易 ID 批量读取本人账本流水（
// payments.getStarsTransactionsByID）。未知 ID 静默跳过：官方服务器对批量
// 查询返回已找到的子集；畸形 ID 在 rpc 层拒绝。
func (s *StarsStore) ListTransactionsByID(ctx context.Context, userID int64, ids []string) ([]domain.StarsTransaction, error) {
	if userID == 0 || len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT id, COALESCE(transaction_id, ''), peer_type, peer_id, amount, reason, title, description, date,
       COALESCE(premium_payment_intent_id, 0), COALESCE(premium_recipient_user_id, 0),
       COALESCE(premium_months, 0), refunded, COALESCE(gift_id, 0), gift_is_unique
FROM stars_transactions
WHERE user_id = $1 AND transaction_id = ANY($2)`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("list stars transactions by id: %w", err)
	}
	defer rows.Close()
	byID := make(map[string]domain.StarsTransaction, len(ids))
	for rows.Next() {
		var (
			t        domain.StarsTransaction
			peerType string
			peerID   int64
			reason   string
		)
		if err := rows.Scan(&t.ID, &t.TransactionID, &peerType, &peerID, &t.Amount, &reason, &t.Title, &t.Description, &t.Date,
			&t.PaymentID, &t.RecipientUserID, &t.PremiumMonths, &t.Refunded, &t.GiftID, &t.GiftIsUnique); err != nil {
			return nil, fmt.Errorf("scan stars transaction: %w", err)
		}
		t.UserID = userID
		t.Reason = domain.StarsTransactionReason(reason)
		if peerType != "" {
			t.Peer = domain.Peer{Type: domain.PeerType(peerType), ID: peerID}
		}
		byID[t.TransactionID] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stars transactions by id: %w", err)
	}
	out := make([]domain.StarsTransaction, 0, len(ids))
	for _, id := range ids {
		if t, ok := byID[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}
