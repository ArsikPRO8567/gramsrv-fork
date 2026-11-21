package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// chargePrivatePaidMessageTx 在私聊发送事务内完成付费消息（paid messages）的
// 资金交割。官方语义：收件人为"非联系人消息"设置了 Stars 价格（PrivacyKey
// NoPaidMessages / NoncontactPeersPaidStars），发送方必须支付后消息才落地；
// 收件人获得与支付额相等的 Stars（官方对私聊付费消息不抽成）。
//
// 资金两侧写进同一事务：发送方借记 + 收件人贷记，两条流水共用同一个公开
// 交易 ID（账户间转账的 ID 必须一致，客户端据此把两端展示为同一笔操作）。
// 事务随消息插入一起提交/回滚——重放请求在事务外由幂等指纹短路，不会重复
// 扣款；发送失败时整个事务回滚，余额无损。
func chargePrivatePaidMessageTx(ctx context.Context, tx pgx.Tx, senderID, recipientID, stars int64, date int) error {
	if stars <= 0 || senderID <= 0 || recipientID <= 0 || senderID == recipientID {
		return nil
	}
	var balance int64
	var granted bool
	if err := tx.QueryRow(ctx, `SELECT balance,granted FROM stars_balances WHERE user_id=$1 FOR UPDATE`, senderID).
		Scan(&balance, &granted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrStarsInsufficient
		}
		return fmt.Errorf("lock paid-message sender balance: %w", err)
	}
	if balance < stars {
		return domain.ErrStarsInsufficient
	}
	if _, err := tx.Exec(ctx, `
UPDATE stars_balances SET balance=balance-$2,updated_at=now() WHERE user_id=$1`, senderID, stars); err != nil {
		return fmt.Errorf("debit paid-message sender balance: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO stars_balances(user_id,balance,updated_at) VALUES($1,$2,now())
ON CONFLICT(user_id) DO UPDATE SET balance=stars_balances.balance+EXCLUDED.balance,updated_at=now()`, recipientID, stars); err != nil {
		return fmt.Errorf("credit paid-message recipient balance: %w", err)
	}
	transactionID := domain.NewStarsTransactionID(false)
	senderPeer := domain.Peer{Type: domain.PeerTypeUser, ID: recipientID}
	recipientPeer := domain.Peer{Type: domain.PeerTypeUser, ID: senderID}
	if err := insertStarsTxnWithID(ctx, tx, senderID, -stars, domain.StarsReasonPaidMessage, senderPeer, date, transactionID); err != nil {
		return err
	}
	return insertStarsTxnWithID(ctx, tx, recipientID, stars, domain.StarsReasonPaidMessage, recipientPeer, date, transactionID)
}
