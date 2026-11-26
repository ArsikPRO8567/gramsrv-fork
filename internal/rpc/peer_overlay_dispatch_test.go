package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// Раунд 48 (анти-мерцание): collectResponsePeerSlices находит слайсы пиров в
// вложенных envelope-типах ответа, а applyBotOwnerViewToUsers ставит
// bot_can_edit только владельцу бота. Вместе это гарантирует: любой ответ,
// содержащий user-объект бота, несёт bot_can_edit для владельца — значок
// редактирования в Android-клиенте перестаёт мерцать.
func TestRound48CollectResponsePeerSlices(t *testing.T) {
	user := &tg.User{ID: 42, AccessHash: 1, FirstName: "A"}
	channel := &tg.Channel{ID: 100, AccessHash: 2, Title: "C"}
	resp := &tg.UpdatesDifference{
		Users: []tg.UserClass{user},
		Chats: []tg.ChatClass{channel},
		NewMessages: []tg.MessageClass{
			&tg.Message{
				ID: 1,
				// Вложенный ответ с собственными Users/Chats.
			},
		},
	}
	users, chats := collectResponsePeerSlices(resp)
	if len(users) != 1 || users[0].(*tg.User).ID != 42 {
		t.Fatalf("users = %+v, want user 42", users)
	}
	if len(chats) != 1 || chats[0].(*tg.Channel).ID != 100 {
		t.Fatalf("chats = %+v, want channel 100", chats)
	}

	// userFull-подобный envelope: Users + Chats на разных уровнях.
	full := &tg.UsersUserFull{
		FullUser: tg.UserFull{ID: 42},
		Users:    []tg.UserClass{user},
		Chats:    []tg.ChatClass{channel},
	}
	u2, c2 := collectResponsePeerSlices(full)
	if len(u2) != 1 || len(c2) != 1 {
		t.Fatalf("userFull envelope: users=%d chats=%d, want 1/1", len(u2), len(c2))
	}
}

func TestRound48BotOwnerViewOverlay(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(users, botStore, messages)
	owner, err := users.Create(ctx, domain.User{AccessHash: 7101, Phone: "15550007101", FirstName: "Overlay Owner"})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := bots.CreateBot(ctx, owner.ID, "Overlay Bot", "overlay_round48_bot")
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(ctx, domain.User{AccessHash: 7102, Phone: "15550007102", FirstName: "Other Human"})
	if err != nil {
		t.Fatal(err)
	}

	r := New(Config{}, Deps{
		Users: appusers.NewService(users),
		Bots:  bots,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})

	// 1) Ответ для владельца: bot_can_edit выставлен.
	ownerResp := &tg.UsersUserFull{Users: []tg.UserClass{&tg.User{ID: bot.ID, Bot: true}}}
	r.applyResponsePeerOverlays(WithUserID(ctx, owner.ID), ownerResp)
	botUser, _ := ownerResp.Users[0].(*tg.User)
	if !botUser.BotCanEdit {
		t.Fatalf("owner response: bot_can_edit = false, want true")
	}

	// 2) Тот же объект в ответе для чужого аккаунта: bot_can_edit снят
	// (овераеи идемпотентны и пересчитываются от viewer).
	otherResp := &tg.UsersUserFull{Users: []tg.UserClass{botUser}}
	r.applyResponsePeerOverlays(WithUserID(ctx, other.ID), otherResp)
	if botUser.BotCanEdit {
		t.Fatalf("other response: bot_can_edit = true, want false")
	}

	// 3) Обычный пользователь не трогается вовсе.
	human := &tg.User{ID: other.ID}
	r.applyResponsePeerOverlays(WithUserID(ctx, owner.ID), &tg.UsersUserFull{Users: []tg.UserClass{human}})
	if human.BotCanEdit {
		t.Fatalf("human user: bot_can_edit = true, want false")
	}
}
