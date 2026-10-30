package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appdialogs "telesrv/internal/app/dialogs"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// chatThemeGiftService — узкий double GiftsService для сценариев темы чата на
// базе коллекционного подарка (UniqueBySlug + SetUniqueGiftThemePeer).
type chatThemeGiftService struct {
	GiftsService
	gifts map[int64]domain.UniqueStarGift
	// bySlug хранит подарок под НОРМАЛИЗОВАННЫМ (нижний регистр) slug —
	// chatThemeFromInput нормализует ввод так же.
	bySlug    map[string]domain.UniqueStarGift
	themePeer map[int64]domain.Peer
}

func (s *chatThemeGiftService) UniqueBySlug(_ context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	gift, ok := s.bySlug[strings.ToLower(slug)]
	return gift, ok, nil
}

func (s *chatThemeGiftService) UniqueByID(_ context.Context, id int64) (domain.UniqueStarGift, bool, error) {
	gift, ok := s.gifts[id]
	return gift, ok, nil
}

// CountSaved нужен проекции userFull (stargifts_count) — тестовые сценарии
// тем чата не добавляют подарков в профиль.
func (s *chatThemeGiftService) CountSaved(_ context.Context, _ domain.Peer) (int, error) {
	return 0, nil
}

func (s *chatThemeGiftService) SetUniqueGiftThemePeer(_ context.Context, uniqueGiftID int64, peer domain.Peer) error {
	if s.themePeer == nil {
		s.themePeer = map[int64]domain.Peer{}
	}
	if uniqueGiftID <= 0 {
		for id, current := range s.themePeer {
			if current == peer {
				delete(s.themePeer, id)
				if gift, ok := s.gifts[id]; ok {
					gift.ThemePeer = domain.Peer{}
					s.gifts[id] = gift
				}
			}
		}
		return nil
	}
	// Обновляем снапшот в both-картах: боевой store перечитывает
	// unique_star_gifts.theme_peer при каждом UniqueBySlug/UniqueByID.
	if gift, ok := s.gifts[uniqueGiftID]; ok {
		gift.ThemePeer = peer
		s.gifts[uniqueGiftID] = gift
	}
	for slug, gift := range s.bySlug {
		if gift.ID == uniqueGiftID {
			gift.ThemePeer = peer
			s.bySlug[slug] = gift
		}
	}
	s.themePeer[uniqueGiftID] = peer
	return nil
}

func chatThemeGiftFixture(ownerID int64) domain.UniqueStarGift {
	return domain.UniqueStarGift{
		ID: 9101, GiftID: 5415586798250229762, Title: "Crystal Ball", Slug: "CrystalBall-801", Num: 801,
		Owner:          domain.Peer{Type: domain.PeerTypeUser, ID: ownerID},
		ThemeAvailable: true,
		Model:          domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7101}},
		Pattern:        domain.StarGiftCollectibleAttribute{Document: &domain.Document{ID: 7201}},
		Backdrop: domain.StarGiftCollectibleAttribute{
			CenterColor: 0x5c7215, EdgeColor: 0x3540c7, PatternColor: 0x030a68, TextColor: 0x111111,
		},
	}
}

func assertDialogGiftTheme(t *testing.T, dialogs *memory.DialogStore, ownerID, peerID int64, wantSlug string) {
	t.Helper()
	list, err := dialogs.ListByPeers(context.Background(), ownerID, []domain.Peer{{Type: domain.PeerTypeUser, ID: peerID}})
	if err != nil {
		t.Fatalf("list dialogs for %d/%d: %v", ownerID, peerID, err)
	}
	if len(list.Dialogs) != 1 {
		t.Fatalf("dialogs for %d/%d = %d, want 1", ownerID, peerID, len(list.Dialogs))
	}
	if got := list.Dialogs[0].ThemeGiftSlug; got != wantSlug {
		t.Fatalf("dialog gift theme for %d/%d = %q, want %q", ownerID, peerID, got, wantSlug)
	}
}

// TestMessagesSetChatThemeGiftMovesThemeBetweenChats фиксирует официальное
// правило «тема коллекционного подарка живёт ровно в одном чате»
// (core.telegram.org/api/themes): установка в новый чат молча снимает её из
// прежнего — оба направления диалога, без сервисного сообщения в старом чате.
func TestMessagesSetChatThemeGiftMovesThemeBetweenChats(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 3001, Phone: "15550003301", FirstName: "Owner"})
	first, _ := userStore.Create(ctx, domain.User{AccessHash: 3002, Phone: "15550003302", FirstName: "First"})
	second, _ := userStore.Create(ctx, domain.User{AccessHash: 3003, Phone: "15550003303", FirstName: "Second"})
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	gift := chatThemeGiftFixture(owner.ID)
	gifts := &chatThemeGiftService{
		gifts:  map[int64]domain.UniqueStarGift{gift.ID: gift},
		bySlug: map[string]domain.UniqueStarGift{strings.ToLower(gift.Slug): gift},
	}
	router := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Dialogs:  appdialogs.NewService(dialogStore),
		Messages: appmessages.NewService(messageStore, dialogStore),
		Gifts:    gifts,
	}, zaptest.NewLogger(t), clock.System)

	// Установка в первый чат: оба направления получают тему.
	if _, err := router.onMessagesSetChatTheme(WithUserID(ctx, owner.ID), &tg.MessagesSetChatThemeRequest{
		Peer:  &tg.InputPeerUser{UserID: first.ID, AccessHash: first.AccessHash},
		Theme: &tg.InputChatThemeUniqueGift{Slug: gift.Slug},
	}); err != nil {
		t.Fatalf("set gift theme in first chat: %v", err)
	}
	assertDialogGiftTheme(t, dialogStore, owner.ID, first.ID, gift.Slug)
	assertDialogGiftTheme(t, dialogStore, first.ID, owner.ID, gift.Slug)
	if got := gifts.themePeer[gift.ID]; got != (domain.Peer{Type: domain.PeerTypeUser, ID: first.ID}) {
		t.Fatalf("theme_peer after first install = %v, want first chat", got)
	}

	// Перенос во второй чат: прежний чат очищается молча (оба направления),
	// новый получает тему и сервисное сообщение.
	updates, err := router.onMessagesSetChatTheme(WithUserID(ctx, owner.ID), &tg.MessagesSetChatThemeRequest{
		Peer:  &tg.InputPeerUser{UserID: second.ID, AccessHash: second.AccessHash},
		Theme: &tg.InputChatThemeUniqueGift{Slug: gift.Slug},
	})
	if err != nil {
		t.Fatalf("set gift theme in second chat: %v", err)
	}
	if _, ok := chatThemeActionFromUpdates(t, updates).Theme.(*tg.ChatThemeUniqueGift); !ok {
		t.Fatalf("service action theme = %T, want ChatThemeUniqueGift", chatThemeActionFromUpdates(t, updates).Theme)
	}
	assertDialogGiftTheme(t, dialogStore, owner.ID, second.ID, gift.Slug)
	assertDialogGiftTheme(t, dialogStore, second.ID, owner.ID, gift.Slug)
	assertDialogGiftTheme(t, dialogStore, owner.ID, first.ID, "")
	assertDialogGiftTheme(t, dialogStore, first.ID, owner.ID, "")
	if got := gifts.themePeer[gift.ID]; got != (domain.Peer{Type: domain.PeerTypeUser, ID: second.ID}) {
		t.Fatalf("theme_peer after move = %v, want second chat", got)
	}

	// userFull: прежний чат больше не несёт тему, новый несёт chatThemeUniqueGift.
	oldFull, err := router.onUsersGetFullUser(WithUserID(ctx, owner.ID), &tg.InputUser{UserID: first.ID, AccessHash: first.AccessHash})
	if err != nil {
		t.Fatalf("get full user for old chat: %v", err)
	}
	if theme, ok := oldFull.FullUser.GetTheme(); ok {
		t.Fatalf("old chat userFull.theme = %#v, want absent after move", theme)
	}
	newFull, err := router.onUsersGetFullUser(WithUserID(ctx, owner.ID), &tg.InputUser{UserID: second.ID, AccessHash: second.AccessHash})
	if err != nil {
		t.Fatalf("get full user for new chat: %v", err)
	}
	theme, ok := newFull.FullUser.GetTheme()
	if !ok {
		t.Fatal("new chat userFull.theme missing after move")
	}
	giftTheme, ok := theme.(*tg.ChatThemeUniqueGift)
	if !ok {
		t.Fatalf("new chat userFull.theme = %T, want ChatThemeUniqueGift", theme)
	}
	if got := giftTheme.Gift.(*tg.StarGiftUnique).Slug; got != gift.Slug {
		t.Fatalf("userFull theme gift slug = %q, want %q", got, gift.Slug)
	}
	themePeer, hasPeer := giftTheme.Gift.(*tg.StarGiftUnique).GetThemePeer()
	if !hasPeer || themePeer.(*tg.PeerUser).UserID != second.ID {
		t.Fatalf("userFull theme gift theme_peer = %v (ok=%v), want second chat", themePeer, hasPeer)
	}
}

// TestMessagesSetChatThemeGiftReinstallSameChatCleaningLegacyDouble проверяет,
// что повторная установка в тот же чат чистит накопленные двойные установки
// (диалоги, оставшиеся со slug от прежних багов), даже если changed=false.
func TestMessagesSetChatThemeGiftReinstallSameChatCleaningLegacyDouble(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 3011, Phone: "15550003311", FirstName: "Owner"})
	peer, _ := userStore.Create(ctx, domain.User{AccessHash: 3012, Phone: "15550003312", FirstName: "Peer"})
	legacy, _ := userStore.Create(ctx, domain.User{AccessHash: 3013, Phone: "15550003313", FirstName: "Legacy"})
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)

	// Legacy-мусор: диалог third-пользователя несёт тот же slug (прошлая
	// двойная установка), theme_peer в снапшоте подарка смотрит на peer.
	gift := chatThemeGiftFixture(owner.ID)
	gift.ThemePeer = domain.Peer{Type: domain.PeerTypeUser, ID: peer.ID}
	gifts := &chatThemeGiftService{
		gifts:  map[int64]domain.UniqueStarGift{gift.ID: gift},
		bySlug: map[string]domain.UniqueStarGift{strings.ToLower(gift.Slug): gift},
	}
	if _, err := dialogStore.SetChatThemeGift(ctx, legacy.ID, domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, gift.Slug); err != nil {
		t.Fatalf("seed legacy dialog: %v", err)
	}
	router := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Dialogs:  appdialogs.NewService(dialogStore),
		Messages: appmessages.NewService(messageStore, dialogStore),
		Gifts:    gifts,
	}, zaptest.NewLogger(t), clock.System)

	// Повторная установка в тот же чат (theme уже стоит) — changed=false,
	// но legacy-диалог обязан быть почищен.
	if _, err := router.onMessagesSetChatTheme(WithUserID(ctx, owner.ID), &tg.MessagesSetChatThemeRequest{
		Peer:  &tg.InputPeerUser{UserID: peer.ID, AccessHash: peer.AccessHash},
		Theme: &tg.InputChatThemeUniqueGift{Slug: gift.Slug},
	}); err != nil {
		t.Fatalf("re-set gift theme: %v", err)
	}
	assertDialogGiftTheme(t, dialogStore, legacy.ID, owner.ID, "")
	assertDialogGiftTheme(t, dialogStore, owner.ID, legacy.ID, "")
	assertDialogGiftTheme(t, dialogStore, owner.ID, peer.ID, gift.Slug)
	assertDialogGiftTheme(t, dialogStore, peer.ID, owner.ID, gift.Slug)
}
