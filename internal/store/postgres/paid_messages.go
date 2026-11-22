package postgres

import (
        "context"
        "errors"
        "fmt"

        "github.com/jackc/pgx/v5"

        "telesrv/internal/domain"
)

// paidMessageCommissionPermille — доля от платы за сообщение, которая доходит
// до получателя. Совпадает с моноканальной комиссией monoforum
// (paidMessageChannelCommissionPermille) и официальной
// stars_paid_message_commission_permille.
const paidMessageCommissionPermille = 850

// chargePaidMessageStarsTx списывает платное сообщение атомарно внутри транзакции
// отправки: дебет отправителя на stars, кредит получателя на net-выручку
// (stars × 850‰, комиссия не возвращается), обе проводки reason=paid_message.
// Достаточность баланса проверяется под FOR UPDATE; недостаток →
// domain.ErrStarsInsufficient. Вызывается только когда сообщение реально
// доставляется получателю (не self и не в блоклисте).
func chargePaidMessageStarsTx(ctx context.Context, tx pgx.Tx, senderUserID, recipientUserID, stars int64, date int) error {
        if stars <= 0 || senderUserID == recipientUserID {
                return nil
        }
        // Блокировка балансов в фиксированном порядке (по возрастанию id) исключает
        // AB-BA дедлок при встречных отправках с платой у обеих сторон.
        first, second := senderUserID, recipientUserID
        if second < first {
                first, second = second, first
        }
        for _, uid := range []int64{first, second} {
                if _, err := tx.Exec(ctx, `SELECT 1 FROM stars_balances WHERE user_id = $1 FOR UPDATE`, uid); err != nil {
                        return fmt.Errorf("lock paid-message balances: %w", err)
                }
        }
        var senderBalance int64
        if err := tx.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id = $1 FOR UPDATE`, senderUserID).Scan(&senderBalance); err != nil {
                if errors.Is(err, pgx.ErrNoRows) {
                        return domain.ErrStarsInsufficient
                }
                return fmt.Errorf("read paid-message sender balance: %w", err)
        }
        if senderBalance < stars {
                return domain.ErrStarsInsufficient
        }
        if err := tx.QueryRow(ctx, `
UPDATE stars_balances
SET balance = balance - $2, updated_at = now()
WHERE user_id = $1
RETURNING balance`, senderUserID, stars).Scan(&senderBalance); err != nil {
                return fmt.Errorf("debit paid-message sender balance: %w", err)
        }
        if err := insertStarsTxn(ctx, tx, senderUserID, -stars, domain.StarsReasonPaidMessage,
                domain.Peer{Type: domain.PeerTypeUser, ID: recipientUserID}, date, "", ""); err != nil {
                return err
        }
        net := stars * paidMessageCommissionPermille / 1000
        if net > 0 {
                if _, err := tx.Exec(ctx, `
INSERT INTO stars_balances (user_id, balance, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (user_id) DO UPDATE
SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()`, recipientUserID, net); err != nil {
                        return fmt.Errorf("credit paid-message recipient balance: %w", err)
                }
                if err := insertStarsTxn(ctx, tx, recipientUserID, net, domain.StarsReasonPaidMessage,
                        domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID}, date, "", ""); err != nil {
                        return err
                }
        }
        return nil
}

// RefundPaidMessages гасит все ещё не refund'нутые проводки платных сообщений
// payer'а в пользу owner'а (account.toggleNoPaidMessagesException с
// refund_charged=true). В одной транзакции:
//  1. суммируются брутто-платежи payer'а (amount<0, peer=owner, NOT refunded);
//  2. суммируется уже начисленная получателю выручка (amount>0, peer=payer);
//  3. обе исходные проводки помечаются refunded=true (повторный вызов вернёт 0);
//  4. баланс payer'а увеличивается на брутто; с получателя удерживается его
//     нетто-выручка с клампом до текущего остатка (CHECK balance>=0 не даёт
//     уйти в минус; недостача списывается как есть — «звёзды уже потрачены»).
func (s *StarsStore) RefundPaidMessages(ctx context.Context, ownerUserID, payerUserID int64, date int) (domain.PaidMessagesRefund, error) {
        if ownerUserID == 0 || payerUserID == 0 || ownerUserID == payerUserID {
                return domain.PaidMessagesRefund{}, domain.ErrStarsInvalidAmount
        }
        var out domain.PaidMessagesRefund
        err := withTx(ctx, s.db, "refund paid messages", func(tx pgx.Tx) error {
                first, second := ownerUserID, payerUserID
                if second < first {
                        first, second = second, first
                }
                for _, uid := range []int64{first, second} {
                        if _, err := tx.Exec(ctx, `SELECT 1 FROM stars_balances WHERE user_id = $1 FOR UPDATE`, uid); err != nil {
                                return fmt.Errorf("lock paid-message refund balances: %w", err)
                        }
                }
                var gross int64
                // FOR UPDATE к агрегату неприменим в Postgres; сериализацию пары обеспечивают
                // балансовые блокировки выше, поэтому агрегируем без блокировки строк.
                if err := tx.QueryRow(ctx, `
SELECT COALESCE(SUM(-amount), 0), COUNT(*) FROM stars_transactions
WHERE user_id = $1 AND reason = $2 AND amount < 0
  AND peer_type = $3 AND peer_id = $4 AND NOT refunded`, payerUserID, string(domain.StarsReasonPaidMessage), string(domain.PeerTypeUser), ownerUserID).
                        Scan(&gross, &out.Charges); err != nil {
                        return fmt.Errorf("sum paid-message charges: %w", err)
                }
                if gross <= 0 {
                        out = domain.PaidMessagesRefund{}
                        return nil
                }
                var received int64
                if err := tx.QueryRow(ctx, `
SELECT COALESCE(SUM(amount), 0) FROM stars_transactions
WHERE user_id = $1 AND reason = $2 AND amount > 0
  AND peer_type = $3 AND peer_id = $4 AND NOT refunded`, ownerUserID, string(domain.StarsReasonPaidMessage), string(domain.PeerTypeUser), payerUserID).
                        Scan(&received); err != nil {
                        return fmt.Errorf("sum paid-message revenue: %w", err)
                }
                if received < 0 {
                        received = 0
                }
                // Пометки исходных проводок — источник истины «что уже возвращено».
                if _, err := tx.Exec(ctx, `
UPDATE stars_transactions SET refunded = true
WHERE user_id = $1 AND reason = $2 AND amount < 0
  AND peer_type = $3 AND peer_id = $4 AND NOT refunded`, payerUserID, string(domain.StarsReasonPaidMessage), string(domain.PeerTypeUser), ownerUserID); err != nil {
                        return fmt.Errorf("mark paid-message charges refunded: %w", err)
                }
                if received > 0 {
                        if _, err := tx.Exec(ctx, `
UPDATE stars_transactions SET refunded = true
WHERE user_id = $1 AND reason = $2 AND amount > 0
  AND peer_type = $3 AND peer_id = $4 AND NOT refunded`, ownerUserID, string(domain.StarsReasonPaidMessage), string(domain.PeerTypeUser), payerUserID); err != nil {
                                return fmt.Errorf("mark paid-message revenue refunded: %w", err)
                        }
                }
                // Возврат плательщику: брутто (комиссия сервера возвращается платившему).
                if _, err := tx.Exec(ctx, `
INSERT INTO stars_balances (user_id, balance, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (user_id) DO UPDATE
SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()`, payerUserID, gross); err != nil {
                        return fmt.Errorf("credit paid-message refund: %w", err)
                }
                if _, err := insertStarsTxnFull(ctx, tx, payerUserID, gross, domain.StarsReasonPaidMessage,
                        domain.Peer{Type: domain.PeerTypeUser, ID: ownerUserID}, date, "", "",
                        domain.StarsLedgerFlags{Refunded: true}); err != nil {
                        return err
                }
                // Кламп-удержание с получателя:stars_balances.balance имеет CHECK >= 0.
                if received > 0 {
                        var ownerBalance int64
                        if err := tx.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id = $1 FOR UPDATE`, ownerUserID).Scan(&ownerBalance); err != nil {
                                if !errors.Is(err, pgx.ErrNoRows) {
                                        return fmt.Errorf("read paid-message owner balance: %w", err)
                                }
                        }
                        claw := received
                        if ownerBalance < claw {
                                claw = ownerBalance
                        }
                        if claw > 0 {
                                if _, err := tx.Exec(ctx, `
UPDATE stars_balances SET balance = balance - $2, updated_at = now()
WHERE user_id = $1`, ownerUserID, claw); err != nil {
                                        return fmt.Errorf("claw back paid-message revenue: %w", err)
                                }
                                if _, err := insertStarsTxnFull(ctx, tx, ownerUserID, -claw, domain.StarsReasonPaidMessage,
                                        domain.Peer{Type: domain.PeerTypeUser, ID: payerUserID}, date, "", "",
                                        domain.StarsLedgerFlags{Refunded: true}); err != nil {
                                        return err
                                }
                                out.ClawedBack = claw
                        }
                }
                out.Refunded = gross
                return nil
        })
        if err != nil {
                return domain.PaidMessagesRefund{}, err
        }
        return out, nil
}

// PaidMessagesRevenue возвращает сумму непогашенных (не refund'нутых) платежей
// payer'а за сообщения в адрес peer'а: для приватных платных сообщений peer —
// сам получатель (user), для monoforum — канал-родитель (charge писал peer=channel).
func (s *StarsStore) PaidMessagesRevenue(ctx context.Context, payerUserID int64, peer domain.Peer) (int64, error) {
        if payerUserID == 0 || peer.ID == 0 || (peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel) {
                return 0, nil
        }
        var total int64
        if err := s.db.QueryRow(ctx, `
SELECT COALESCE(SUM(-amount), 0) FROM stars_transactions
WHERE user_id = $1 AND reason = $2 AND amount < 0
  AND peer_type = $3 AND peer_id = $4 AND NOT refunded`,
                payerUserID, string(domain.StarsReasonPaidMessage), string(peer.Type), peer.ID).Scan(&total); err != nil {
                return 0, fmt.Errorf("paid messages revenue: %w", err)
        }
        return total, nil
}
