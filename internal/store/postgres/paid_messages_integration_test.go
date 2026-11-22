package postgres

import (
        "context"
        "testing"
        "time"

        "telesrv/internal/domain"
)

// Интеграционный тест приватных платных сообщений: атомарное списание в
// транзакции отправки (chargePaidMessageStarsTx), выручка и возврат через
// RefundPaidMessages. Требует TELESRV_TEST_POSTGRES_DSN.
func TestPaidMessagesChargeAndRefundPostgres(t *testing.T) {
        pool := testPool(t)
        ctx := context.Background()
        suffix := randomSuffix(t)
        now := int(time.Now().Unix())
        users := NewUserStore(pool)
        payer := createTestUser(t, ctx, users, "+1990"+suffix+"01", "FeePayer", "")
        owner := createTestUser(t, ctx, users, "+1990"+suffix+"02", "FeeOwner", "")

        stars := NewStarsStore(pool)
        messages := NewMessageStore(pool)

        // Стартовые балансы: грант плательщику 1000, получатель 0 (нет гранта —
        // кредитуется только выручкой).
        if _, _, err := stars.EnsureGrant(ctx, payer.ID, 1000, now); err != nil {
                t.Fatalf("grant payer: %v", err)
        }

        // Отправка платного сообщения (ставка 10): списание атомарно с сообщением.
        res, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
                SenderUserID:     payer.ID,
                RecipientUserID:  owner.ID,
                RandomID:         77101,
                Message:          "paid hello",
                Date:             now,
                PaidMessageStars: 10,
        })
        if err != nil {
                t.Fatalf("paid send: %v", err)
        }
        if res.Duplicate || res.SenderMessage.ID == 0 {
                t.Fatalf("paid send result = %+v", res)
        }
        payerBal, err := stars.GetBalance(ctx, payer.ID)
        if err != nil || payerBal.Balance != 990 {
                t.Fatalf("payer balance = %d err %v, want 990", payerBal.Balance, err)
        }
        ownerBal, err := stars.GetBalance(ctx, owner.ID)
        if err != nil || ownerBal.Balance != 10*850/1000 {
                t.Fatalf("owner balance = %d err %v, want 8 (850 permille)", ownerBal.Balance, err)
        }

        // Недостаток средств: опустошаем плательщика и ждём ErrStarsInsufficient.
        if _, err := stars.Debit(ctx, payer.ID, 990, domain.StarsReasonAdjust, domain.Peer{}, now, "", ""); err != nil {
                t.Fatalf("drain payer: %v", err)
        }
        if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
                SenderUserID:     payer.ID,
                RecipientUserID:  owner.ID,
                RandomID:         77102,
                Message:          "unpaid",
                Date:             now + 1,
                PaidMessageStars: 10,
        }); err != domain.ErrStarsInsufficient {
                t.Fatalf("insufficient send err = %v, want ErrStarsInsufficient", err)
        }
        // Сообщение не должно было создаться (откат транзакции).
        if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
                SenderUserID:     payer.ID,
                RecipientUserID:  owner.ID,
                RandomID:         77102,
                Message:          "retry",
                Date:             now + 2,
        }); err != nil {
                t.Fatalf("retry send after failed charge: %v", err)
        }

        // Выручка: брутто потраченное — 10.
        revenue, err := stars.PaidMessagesRevenue(ctx, payer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID})
        if err != nil || revenue != 10 {
                t.Fatalf("paid messages revenue = %d err %v, want 10", revenue, err)
        }

        // Повторная отправка того же random_id (replay) не списывает повторно.
        if _, err := stars.Credit(ctx, payer.ID, 1000, domain.StarsReasonAdjust, domain.Peer{}, now, "", ""); err != nil {
                t.Fatalf("refill payer: %v", err)
        }
        before := mustBalance(t, stars, payer.ID)
        replay, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
                SenderUserID:     payer.ID,
                RecipientUserID:  owner.ID,
                RandomID:         77101,
                Message:          "paid hello",
                Date:             now,
                PaidMessageStars: 10,
        })
        if err != nil || !replay.Duplicate {
                t.Fatalf("replay send = %+v err %v, want duplicate", replay, err)
        }
        if after := mustBalance(t, stars, payer.ID); after != before {
                t.Fatalf("replay charged again: before=%d after=%d", before, after)
        }

        // Возврат через RefundPaidMessages: брутто одной успешной оплаты — 10
        // (вторая попытка откатилась вместе с транзакцией — проводок нет).
        refund, err := stars.RefundPaidMessages(ctx, owner.ID, payer.ID, now+3)
        if err != nil {
                t.Fatalf("refund paid messages: %v", err)
        }
        if refund.Refunded != 10 || refund.Charges != 1 {
                t.Fatalf("refund = %+v, want refunded 10 charges 1", refund)
        }
        if got := mustBalance(t, stars, payer.ID); got != before+10 {
                t.Fatalf("payer balance after refund = %d, want %d", got, before+10)
        }
        // Получатель теряет свою нетто-выручку (8), баланс уходит в ноль.
        if got := mustBalance(t, stars, owner.ID); got != 0 {
                t.Fatalf("owner balance after claw back = %d, want 0", got)
        }
        if revenue, err := stars.PaidMessagesRevenue(ctx, payer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}); err != nil || revenue != 0 {
                t.Fatalf("revenue after refund = %d err %v, want 0", revenue, err)
        }

        // Повторный возврат — пустой (исходные проводки помечены refunded).
        again, err := stars.RefundPaidMessages(ctx, owner.ID, payer.ID, now+4)
        if err != nil || again.Refunded != 0 || again.Charges != 0 {
                t.Fatalf("second refund = %+v err %v, want empty", again, err)
        }
}

func mustBalance(t *testing.T, stars *StarsStore, userID int64) int64 {
        t.Helper()
        bal, err := stars.GetBalance(context.Background(), userID)
        if err != nil {
                t.Fatalf("balance %d: %v", userID, err)
        }
        return bal.Balance
}
