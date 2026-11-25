package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// Раунд 45: правила «кому нельзя дарить» и требование знакомства бота.
// Заглушки дают детерминированный исход для каждого сценария; правила
// применяются в enforceGiftRecipientRules до выдачи формы/списания звёзд.

type fakeGiftActorRulesStore struct {
	serviceAccounts map[int64]bool
	chats           map[[2]int64]bool
}

func (f fakeGiftActorRulesStore) ServiceAccount(_ context.Context, userID int64) (bool, error) {
	return f.serviceAccounts[userID], nil
}

func (f fakeGiftActorRulesStore) HasPrivateChatEver(_ context.Context, a, b int64) (bool, error) {
	return f.chats[[2]int64{a, b}] || f.chats[[2]int64{b, a}], nil
}

func giftActorTestRouter(t *testing.T, users map[int64]domain.User, store GiftActorRulesStore) *Router {
	t.Helper()
	return New(Config{}, Deps{
		Users:          mapUsersService{users: users},
		GiftActorRules: store,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})
}

// giftErrIs проверяет код официальной ошибки (USER_GIFT_UNAVAILABLE и т.п.).
func giftErrIs(err error, msg string) bool {
	if err == nil {
		return false
	}
	if te, ok := err.(*tgerr.Error); ok {
		return te.Message == msg
	}
	return strings.Contains(err.Error(), msg)
}

func TestEnforceGiftRecipientRulesServiceAccountsAndBots(t *testing.T) {
	ctx := context.Background()
	const (
		plainUserID   = int64(2001)
		serviceUserID = int64(2002)
		botUserID     = int64(2003)
		actorUserID   = int64(2004)
		actorBotID    = int64(2005)
		serviceBotID  = int64(2006)
	)
	users := map[int64]domain.User{
		plainUserID:   {ID: plainUserID},
		serviceUserID: {ID: serviceUserID},
		botUserID:     {ID: botUserID, Bot: true},
		actorUserID:   {ID: actorUserID},
		actorBotID:    {ID: actorBotID, Bot: true},
		serviceBotID:  {ID: serviceBotID, Bot: true},
	}
	target := func(id int64) domain.Peer { return domain.Peer{Type: domain.PeerTypeUser, ID: id} }

	// 1. Обычный аккаунт → служебный аккаунт: USER_GIFT_UNAVAILABLE.
	store := fakeGiftActorRulesStore{
		serviceAccounts: map[int64]bool{serviceUserID: true, serviceBotID: true},
		chats:           map[[2]int64]bool{{actorBotID, plainUserID}: true},
	}
	r := giftActorTestRouter(t, users, store)
	if err := r.enforceGiftRecipientRules(ctx, actorUserID, target(serviceUserID)); !giftErrIs(err, "USER_GIFT_UNAVAILABLE") {
		t.Fatalf("regular→service_account error = %v, want USER_GIFT_UNAVAILABLE", err)
	}
	// 2. Обычный аккаунт → бот: PEER_ID_INVALID.
	if err := r.enforceGiftRecipientRules(ctx, actorUserID, target(botUserID)); !giftErrIs(err, "PEER_ID_INVALID") {
		t.Fatalf("regular→bot error = %v, want PEER_ID_INVALID", err)
	}
	// 3. Обычный аккаунт → обычный аккаунт: разрешено.
	if err := r.enforceGiftRecipientRules(ctx, actorUserID, target(plainUserID)); err != nil {
		t.Fatalf("regular→regular must be allowed, got %v", err)
	}
	// 4. Служебный аккаунт дарит свободно: служебному боту и обычному боту.
	if err := r.enforceGiftRecipientRules(ctx, serviceUserID, target(serviceBotID)); err != nil {
		t.Fatalf("service account→service bot must be allowed, got %v", err)
	}
	if err := r.enforceGiftRecipientRules(ctx, serviceUserID, target(botUserID)); err != nil {
		t.Fatalf("service account→bot must be allowed, got %v", err)
	}
	// 5. Служебный бот (бот с флагом service_account) тоже дарит свободно.
	if err := r.enforceGiftRecipientRules(ctx, serviceBotID, target(botUserID)); err != nil {
		t.Fatalf("service bot→bot must be allowed, got %v", err)
	}
	// 6. Бот → служебный аккаунт: USER_GIFT_UNAVAILABLE.
	if err := r.enforceGiftRecipientRules(ctx, actorBotID, target(serviceUserID)); !giftErrIs(err, "USER_GIFT_UNAVAILABLE") {
		t.Fatalf("bot→service account error = %v, want USER_GIFT_UNAVAILABLE", err)
	}
	// 6a. Бот → незнакомый пользователь (ни разу не было чата): USER_GIFT_UNAVAILABLE.
	unacquainted := fakeGiftActorRulesStore{serviceAccounts: store.serviceAccounts}
	r = giftActorTestRouter(t, users, unacquainted)
	if err := r.enforceGiftRecipientRules(ctx, actorBotID, target(plainUserID)); !giftErrIs(err, "USER_GIFT_UNAVAILABLE") {
		t.Fatalf("bot→unknown user error = %v, want USER_GIFT_UNAVAILABLE", err)
	}
	// 7. Бот → знакомый пользователь (чат был или есть): разрешено.
	acquainted := fakeGiftActorRulesStore{chats: map[[2]int64]bool{{actorBotID, plainUserID}: true}}
	r = giftActorTestRouter(t, users, acquainted)
	if err := r.enforceGiftRecipientRules(ctx, actorBotID, target(plainUserID)); err != nil {
		t.Fatalf("bot→acquainted user must be allowed, got %v", err)
	}
	// 8. Канальная цель не ограничивается.
	if err := r.enforceGiftRecipientRules(ctx, actorUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}); err != nil {
		t.Fatalf("channel target must be unrestricted, got %v", err)
	}
	// 9. nil-стора: ограничения не применяются (тестовые подделки deps).
	r = New(Config{}, Deps{Users: mapUsersService{users: users}}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})
	if err := r.enforceGiftRecipientRules(ctx, actorUserID, target(serviceUserID)); err != nil {
		t.Fatalf("nil store must fail-open, got %v", err)
	}
}

// Раунд 45 (документация Telegram): транзакция подаренной Premium-подписки —
// peer = получатель подарка (клиент рисует «Gifted Premium to {peer}»),
// само-покупка через @premiumbot — peer = starsTransactionPeerPremiumBot.
func TestStarsTransactionPeerPremiumGiftFollowsDocs(t *testing.T) {
	gift := domain.StarsTransaction{
		Reason:          domain.StarsReasonPremium,
		RecipientUserID: 7401,
		Peer:            domain.Peer{Type: domain.PeerTypeUser, ID: 1250000015},
		PremiumMonths:   3,
	}
	peer, ok := tgStarsTransactionPeer(gift).(*tg.StarsTransactionPeer)
	user, _ := peer.GetPeer().(*tg.PeerUser)
	if !ok || user == nil || user.UserID != 7401 {
		t.Fatalf("gifted premium peer = %#v, want recipient user peer", tgStarsTransactionPeer(gift))
	}
	self := domain.StarsTransaction{
		Reason:        domain.StarsReasonPremium,
		PremiumMonths: 3,
		Peer:          domain.Peer{Type: domain.PeerTypeUser, ID: 1250000015},
	}
	if _, ok := tgStarsTransactionPeer(self).(*tg.StarsTransactionPeerPremiumBot); !ok {
		t.Fatalf("self premium peer = %#v, want starsTransactionPeerPremiumBot", tgStarsTransactionPeer(self))
	}
}
