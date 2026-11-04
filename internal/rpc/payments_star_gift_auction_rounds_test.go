package rpc

import (
        "testing"

        "github.com/iamxvbaba/td/tg"

        "telesrv/internal/domain"
)

// Раунд 20/21: rounds-вектор состояния аукциона передаётся клиенту в
// компактном официальном формате. Каждый элемент покрывает отрезок
// последовательных раундов с одинаковыми настройками (num..next.num-1):
// клиент показывает «Раунд X: {время} + {окно} для топ-N» для настроенных
// раундов (starGiftAuctionRoundExtendable) и «Раунды X-N: {время} каждый»
// для остальных.
func TestTgStarGiftAuctionStateRoundsVector(t *testing.T) {
        state := domain.StarGiftAuction{
                Version: 7, StartDate: 1000, EndDate: 20000, MinBidAmount: 125,
                NextRoundAt: 1300, CurrentRound: 2, TotalRounds: 4, RoundDuration: 3600,
                RoundDurations: map[int]int{1: 120, 2: 300, 4: 1000},
                TopBidAmount:   900,
        }
        projected, ok := tgStarGiftAuctionState(state).(*tg.StarGiftAuctionState)
        if !ok {
                t.Fatalf("active auction must project to starGiftAuctionState")
        }
        if len(projected.Rounds) != 4 {
                t.Fatalf("rounds vector length = %d, want 4", len(projected.Rounds))
        }
        want := []int{120, 300, 3600, 1000}
        for i, round := range projected.Rounds {
                concrete, ok := round.(*tg.StarGiftAuctionRound)
                if !ok {
                        t.Fatalf("round %d is %T, want *tg.StarGiftAuctionRound", i+1, round)
                }
                if concrete.Num != i+1 || concrete.Duration != want[i] {
                        t.Fatalf("round %d = {num:%d duration:%d}, want {%d %d}", i+1, concrete.Num, concrete.Duration, i+1, want[i])
                }
        }
        // NULL-jsonb: все раунды одинаковые — свёртываются в одну запись,
        // покрывающую весь аукцион (num..next.num-1, последний — до total_rounds).
        state.RoundDurations = nil
        projected, ok = tgStarGiftAuctionState(state).(*tg.StarGiftAuctionState)
        if !ok {
                t.Fatalf("nil durations must still project to starGiftAuctionState")
        }
        if len(projected.Rounds) != 1 {
                t.Fatalf("nil durations rounds vector length = %d, want 1 (collapsed)", len(projected.Rounds))
        }
        if round := projected.Rounds[0].(*tg.StarGiftAuctionRound); round.Num != 1 || round.Duration != 3600 {
                t.Fatalf("collapsed round = {num:%d duration:%d}, want {1 3600}", round.Num, round.Duration)
        }
        // Раунд 21: настроенный раунд с продлением проектируется
        // starGiftAuctionRoundExtendable и разрывает свёртку одинаковых
        // длительностей; соседние обычные раунды остаются свёрнутыми.
        state = domain.StarGiftAuction{
                Version: 8, StartDate: 1000, EndDate: 20000, MinBidAmount: 125,
                NextRoundAt: 1300, CurrentRound: 1, TotalRounds: 6, RoundDuration: 300,
                RoundExtends: map[int]domain.StarGiftAuctionRoundExtend{
                        3: {WindowSeconds: 30, TopBidders: 3, ThresholdSeconds: 60},
                },
        }
        projected, ok = tgStarGiftAuctionState(state).(*tg.StarGiftAuctionState)
        if !ok {
                t.Fatalf("extendable auction must project to starGiftAuctionState")
        }
        if len(projected.Rounds) != 3 {
                t.Fatalf("extendable rounds vector length = %d, want 3", len(projected.Rounds))
        }
        first, ok := projected.Rounds[0].(*tg.StarGiftAuctionRound)
        if !ok || first.Num != 1 || first.Duration != 300 {
                t.Fatalf("rounds 1-2 = %+v, want collapsed {1 300}", projected.Rounds[0])
        }
        second, ok := projected.Rounds[1].(*tg.StarGiftAuctionRoundExtendable)
        if !ok || second.Num != 3 || second.Duration != 300 || second.ExtendTop != 3 || second.ExtendWindow != 30 {
                t.Fatalf("round 3 = %+v, want extendable {3 300 top:3 window:30}", projected.Rounds[1])
        }
        third, ok := projected.Rounds[2].(*tg.StarGiftAuctionRound)
        if !ok || third.Num != 4 || third.Duration != 300 {
                t.Fatalf("rounds 4-6 = %+v, want collapsed {4 300}", projected.Rounds[2])
        }
        // Свёртка соседних одинаково настроенных расширяемых раундов:
        // [1 обычный], [2-3 extendable], [4-6 обычные].
        state.RoundExtends = map[int]domain.StarGiftAuctionRoundExtend{
                2: {WindowSeconds: 30, TopBidders: 2},
                3: {WindowSeconds: 30, TopBidders: 2},
        }
        projected, _ = tgStarGiftAuctionState(state).(*tg.StarGiftAuctionState)
        if len(projected.Rounds) != 3 {
                t.Fatalf("twin extendable rounds vector length = %d, want 3", len(projected.Rounds))
        }
        if _, ok := projected.Rounds[0].(*tg.StarGiftAuctionRound); !ok {
                t.Fatalf("round 1 must stay plain")
        }
        twin, ok := projected.Rounds[1].(*tg.StarGiftAuctionRoundExtendable)
        if !ok || twin.Num != 2 || twin.ExtendTop != 2 || twin.ExtendWindow != 30 {
                t.Fatalf("rounds 2-3 = %+v, want collapsed extendable {2 top:2 window:30}", projected.Rounds[1])
        }
        if tail, ok := projected.Rounds[2].(*tg.StarGiftAuctionRound); !ok || tail.Num != 4 {
                t.Fatalf("rounds 4-6 = %+v, want collapsed plain {4 300}", projected.Rounds[2])
        }
}

// Раунд 20, проблема 1a: владелец не видит offer_min_stars (кнопку «Предложить
// сделку») ни на одном экране своего подарка, включая messageActionStarGiftUnique
// (анимация улучшения, покупки, передачи); остальным зрителям флаг отдаётся.
func TestTgMessageActionStarGiftUniqueOfferFlagByViewer(t *testing.T) {
        unique := domain.UniqueStarGift{
                ID: 5101, GiftID: 8002, Title: "Durov Cap", Slug: "DurovCap-1", Num: 1,
                Owner:         domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
                OfferMinStars: 500,
        }
        ownerAction, ok := tgMessageActionStarGiftUnique(&domain.MessageStarGiftUniqueAction{
                Gift: unique, Peer: unique.Owner, Saved: true, Upgrade: true,
        }, 1001).(*tg.MessageActionStarGiftUnique)
        if !ok {
                t.Fatalf("owner action conversion failed")
        }
        ownerGift, ok := ownerAction.Gift.(*tg.StarGiftUnique)
        if !ok {
                t.Fatalf("owner action gift is %T", ownerAction.Gift)
        }
        if _, present := ownerGift.GetOfferMinStars(); present {
                t.Fatalf("owner must not see offer_min_stars in the upgrade animation")
        }

        otherAction := tgMessageActionStarGiftUnique(&domain.MessageStarGiftUniqueAction{
                Gift: unique, Peer: unique.Owner, Saved: true, Upgrade: true,
        }, 2002).(*tg.MessageActionStarGiftUnique)
        otherGift := otherAction.Gift.(*tg.StarGiftUnique)
        if minStars, present := otherGift.GetOfferMinStars(); !present || minStars != 500 {
                t.Fatalf("non-owner offer_min_stars = %d,%v want 500,true", minStars, present)
        }
}

// Раунд 20, проблема 3: после решения продавца исходное сообщение-оффер
// получает Accepted/Declined вместо обратного отсчёта.
func TestMessageActionStarGiftPurchaseOfferResolutionFlags(t *testing.T) {
        unique := domain.UniqueStarGift{
                ID: 5102, GiftID: 8002, Title: "Durov Cap", Slug: "DurovCap-2", Num: 2,
                Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
        }
        action := &domain.MessageStarGiftOfferAction{
                Gift: unique, Price: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 300},
                ExpiresAt: 1900,
        }
        media := func() *domain.MessageMedia {
                return &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
                        Kind: domain.MessageServiceActionStarGiftOffer, StarGiftOffer: action,
                }}
        }

        msg := domain.Message{OwnerUserID: 1001, Media: media()}
        projected, ok := tgMessageServiceAction(msg).(*tg.MessageActionStarGiftPurchaseOffer)
        if !ok {
                t.Fatalf("pending offer conversion failed")
        }
        if projected.Accepted || projected.Declined {
                t.Fatalf("pending offer must not carry resolution flags")
        }

        action.Accepted = true
        accepted := tgMessageServiceAction(msg).(*tg.MessageActionStarGiftPurchaseOffer)
        if !accepted.Accepted || accepted.Declined {
                t.Fatalf("accepted offer flags = %v,%v want true,false", accepted.Accepted, accepted.Declined)
        }

        action.Accepted, action.Declined = false, true
        declined := tgMessageServiceAction(msg).(*tg.MessageActionStarGiftPurchaseOffer)
        if declined.Accepted || !declined.Declined {
                t.Fatalf("declined offer flags = %v,%v want false,true", declined.Accepted, declined.Declined)
        }
}
