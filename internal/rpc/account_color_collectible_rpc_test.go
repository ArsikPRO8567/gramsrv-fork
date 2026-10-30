package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestAccountUpdateColorCollectibleEmitsUserColor фиксирует приём цвета
// ссылок и цитат: после account.updateColor с inputPeerColorCollectible
// пользовательская проекция (users.getUsers / getFullUser / updateUser)
// обязана нести user.color = peerColorCollectible. Ранее снапшот отбрасывался
// в tgUserPeerColor (проверка color.Empty() шла первой), клиент не видел
// палитру ни у владельца, ни у других пользователей.
func TestAccountUpdateColorCollectibleEmitsUserColor(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 4001, Phone: "15550003401", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	gift := domain.UniqueStarGift{
		ID: 9201, GiftID: 5415586798250229762, Title: "Crystal Ball", Slug: "CrystalBall-801",
		Owner:          domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ThemeAvailable: true,
		Model:          domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7101}},
		Pattern:        domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7201}},
		Backdrop: domain.StarGiftCollectibleAttribute{
			CenterColor: 0x5c7215, EdgeColor: 0x3540c7, PatternColor: 0x030a68, TextColor: 0xf6f1e7,
		},
	}
	gifts := &collectibleEmojiGiftService{gifts: map[int64]domain.UniqueStarGift{gift.ID: gift}}
	sessions := &captureSessions{}
	router := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Gifts:    gifts,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	req := &tg.AccountUpdateColorRequest{}
	req.SetColor(&tg.InputPeerColorCollectible{CollectibleID: gift.ID})
	ok, err := router.onAccountUpdateColor(WithUserID(ctx, owner.ID), req)
	if err != nil || !ok {
		t.Fatalf("update collectible color = ok %v err %v, want true/nil", ok, err)
	}

	saved, found, err := userStore.ByID(ctx, owner.ID)
	if err != nil || !found {
		t.Fatalf("load saved user found=%v err=%v", found, err)
	}
	if saved.Color.Collectible.Empty() {
		t.Fatalf("stored color collectible empty: %+v", saved.Color)
	}
	if !saved.Color.Collectible.Valid() {
		t.Fatalf("stored color collectible invalid: %+v", saved.Color.Collectible)
	}

	// Self-проекция: user.color обязана присутствовать и быть
	// peerColorCollectible с полным снапшотом (акцент, три цвета, тёмный
	// акцент, gift/background emoji).
	selfColor, ok := tgSelfUser(saved).GetColor()
	if !ok {
		t.Fatal("self user missing color after collectible update")
	}
	collectible, ok := selfColor.(*tg.PeerColorCollectible)
	if !ok {
		t.Fatalf("self user color = %T, want *tg.PeerColorCollectible", selfColor)
	}
	assertCollectiblePalette(t, collectible, gift)

	// Проекция для другого пользователя — та же палитра.
	otherColor, ok := tgUser(saved).GetColor()
	if !ok {
		t.Fatal("other-view user missing color after collectible update")
	}
	otherCollectible, ok := otherColor.(*tg.PeerColorCollectible)
	if !ok {
		t.Fatalf("other-view user color = %T, want *tg.PeerColorCollectible", otherColor)
	}
	assertCollectiblePalette(t, otherCollectible, gift)

	// Пушнутый updateUser несёт ту же палитру.
	snap := sessions.snapshot()
	updates, ok := snap.message.(*tg.Updates)
	if !ok || len(updates.Users) != 1 {
		t.Fatalf("pushed updates = %#v, want single self user", snap.message)
	}
	pushed, ok := updates.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("pushed user = %T, want *tg.User", updates.Users[0])
	}
	pushedColor, ok := pushed.GetColor()
	if !ok {
		t.Fatal("pushed user missing color")
	}
	pushedCollectible, ok := pushedColor.(*tg.PeerColorCollectible)
	if !ok {
		t.Fatalf("pushed user color = %T, want *tg.PeerColorCollectible", pushedColor)
	}
	assertCollectiblePalette(t, pushedCollectible, gift)
}

// TestAccountUpdateColorCollectibleClearViaPlainColor проверяет обратный
// переход: установка обычной числовой палитры стирает снапшот коллекции.
func TestAccountUpdateColorCollectibleClearViaPlainColor(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 4011, Phone: "15550003411", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	gift := domain.UniqueStarGift{
		ID: 9211, GiftID: 5415586798250229762, Title: "Crystal Ball", Slug: "CrystalBall-801",
		Owner:          domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ThemeAvailable: true,
		Model:          domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7101}},
		Pattern:        domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7201}},
		Backdrop: domain.StarGiftCollectibleAttribute{
			CenterColor: 0x5c7215, EdgeColor: 0x3540c7, PatternColor: 0x030a68, TextColor: 0xf6f1e7,
		},
	}
	gifts := &collectibleEmojiGiftService{gifts: map[int64]domain.UniqueStarGift{gift.ID: gift}}
	router := New(Config{}, Deps{
		Users: appusers.NewService(userStore),
		Gifts: gifts,
	}, zaptest.NewLogger(t), clock.System)

	collectible := &tg.AccountUpdateColorRequest{}
	collectible.SetColor(&tg.InputPeerColorCollectible{CollectibleID: gift.ID})
	if ok, err := router.onAccountUpdateColor(WithUserID(ctx, owner.ID), collectible); err != nil || !ok {
		t.Fatalf("set collectible color = ok %v err %v, want true/nil", ok, err)
	}

	plain := &tg.AccountUpdateColorRequest{}
	plainColor := &tg.PeerColor{}
	plainColor.SetColor(2)
	plain.SetColor(plainColor)
	if ok, err := router.onAccountUpdateColor(WithUserID(ctx, owner.ID), plain); err != nil || !ok {
		t.Fatalf("set plain color = ok %v err %v, want true/nil", ok, err)
	}

	saved, _, _ := userStore.ByID(ctx, owner.ID)
	if !saved.Color.Collectible.Empty() {
		t.Fatalf("collectible snapshot after plain color = %+v, want cleared", saved.Color.Collectible)
	}
	if color, ok := tgSelfUser(saved).GetColor(); !ok {
		t.Fatal("plain color missing after switch back")
	} else if _, isCollectible := color.(*tg.PeerColorCollectible); isCollectible {
		t.Fatalf("color after plain switch = %T, want plain PeerColor", color)
	}
}

func assertCollectiblePalette(t *testing.T, got *tg.PeerColorCollectible, gift domain.UniqueStarGift) {
	t.Helper()
	if got.CollectibleID != gift.ID {
		t.Fatalf("collectible_id = %d, want %d", got.CollectibleID, gift.ID)
	}
	if got.GiftEmojiID != gift.Model.Document.ID {
		t.Fatalf("gift_emoji_id = %d, want %d", got.GiftEmojiID, gift.Model.Document.ID)
	}
	if got.BackgroundEmojiID != gift.Pattern.Document.ID {
		t.Fatalf("background_emoji_id = %d, want %d", got.BackgroundEmojiID, gift.Pattern.Document.ID)
	}
	if got.AccentColor <= 0 || got.AccentColor > 0xffffff {
		t.Fatalf("accent_color = %d, want plausible RGB24", got.AccentColor)
	}
	if len(got.Colors) != 3 {
		t.Fatalf("colors = %v, want exactly 3 (official dump shape)", got.Colors)
	}
	if _, ok := got.GetDarkAccentColor(); !ok {
		t.Fatal("dark_accent_color missing (official dump sets flag.0)")
	}
}
