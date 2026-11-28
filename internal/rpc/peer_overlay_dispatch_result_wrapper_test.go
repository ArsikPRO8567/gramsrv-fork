package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// Раунд 52 (краш сервера при подключении клиента): DispatchAdmitted передаёт
// в collectResponsePeerSlices обёртку tlprofile.Result. Её поля
// prepared/call/value неэкспортируемы; reflect помечает все значения под
// ними флагом flagRO, и первый же Users/Chats канонического tg-ответа
// ронял сервер паникой «reflect.Value.Interface: cannot return value
// obtained from unexported field or method» (reflect/value.go:1484).
//
// Тесты фиксируют контракт walker'а:
//  1. обёртка tlprofile.Result разворачивается в каноническое значение, и
//     пиры из него собираются (иначе проекция раунда 51 на пути
//     DispatchAdmitted стала бы тихим no-op);
//  2. неэкспортируемые поля структур пропускаются целиком, без паники;
//  3. экспортируемые поля (в том числе вложенные envelope-структуры)
//     собираются как раньше — поверхностный список пиров раундов 48–51
//     не теряется.
type round52ResultWrapper struct {
	wrappedValue any
}

func (f round52ResultWrapper) Encode(b *bin.Buffer) error {
	return nil
}

func (f round52ResultWrapper) Prepared() tlprofile.PreparedCall {
	return tlprofile.PreparedCall{}
}

func (f round52ResultWrapper) WireInvariant() bool {
	return false
}

func (f round52ResultWrapper) CanonicalValue() any {
	return f.wrappedValue
}

type round52Envelope struct {
	Users []tg.UserClass
	Chats []tg.ChatClass
	// hiddenUsers — неэкспортируемое поле: до раунда 52 walker заходил в
	// него, получал значение с flagRO и паниковал на Interface().
	hiddenUsers []tg.UserClass
}

type round52NestedInner struct {
	Users []tg.UserClass
}

type round52NestedOuter struct {
	// inner — неэкспортируемое поле-структура: flagRO распространяется на
	// всё поддерево, включая экспортируемые поля вложенной структуры.
	inner round52NestedInner
	OK    []tg.UserClass
}

func TestRound52ResultWrapperUnwrapsCanonicalValue(t *testing.T) {
	self := &tg.User{ID: 1, AccessHash: 1, FirstName: "Self"}
	peer := &tg.User{ID: 2, AccessHash: 2, FirstName: "Peer"}
	channel := &tg.Channel{ID: 100, AccessHash: 3, Title: "C"}
	resp := round52ResultWrapper{wrappedValue: &tg.Updates{
		Users: []tg.UserClass{self, peer},
		Chats: []tg.ChatClass{channel},
	}}
	users, chats := collectResponsePeerSlices(resp)
	if len(users) != 2 || len(chats) != 1 {
		t.Fatalf("expected 2 users and 1 chat from canonical value, got %d users and %d chats", len(users), len(chats))
	}
	if users[0].(*tg.User).ID != 1 || users[1].(*tg.User).ID != 2 {
		t.Fatalf("wrong users collected from canonical value")
	}
	if chats[0].(*tg.Channel).ID != 100 {
		t.Fatalf("wrong chat collected from canonical value")
	}
}

func TestRound52WalkerSkipsUnexportedFields(t *testing.T) {
	seen := &tg.User{ID: 42, AccessHash: 4, FirstName: "Seen"}
	hidden := &tg.User{ID: 777, AccessHash: 5, FirstName: "Hidden"}
	envelope := round52Envelope{
		Users:       []tg.UserClass{seen},
		hiddenUsers: []tg.UserClass{hidden},
	}
	users, chats := collectResponsePeerSlices(&envelope)
	if len(chats) != 0 {
		t.Fatalf("expected no chats, got %d", len(chats))
	}
	if len(users) != 1 || users[0].(*tg.User).ID != 42 {
		t.Fatalf("expected only exported user 42, got %v", users)
	}
}

func TestRound52WalkerSkipsUnexportedNestedSubtrees(t *testing.T) {
	outer := round52NestedOuter{
		inner: round52NestedInner{Users: []tg.UserClass{&tg.User{ID: 1000}}},
		OK:    []tg.UserClass{&tg.User{ID: 7, AccessHash: 7, FirstName: "OK"}},
	}
	users, chats := collectResponsePeerSlices(&outer)
	if len(chats) != 0 {
		t.Fatalf("expected no chats, got %d", len(chats))
	}
	if len(users) != 1 || users[0].(*tg.User).ID != 7 {
		t.Fatalf("expected only exported user 7, got %v", users)
	}
}

func TestRound52WalkerStillCollectsNestedEnvelopes(t *testing.T) {
	user := &tg.User{ID: 42, AccessHash: 1, FirstName: "A"}
	deep := &tg.UpdatesDifference{
		NewMessages: []tg.MessageClass{
			&tg.Message{ID: 1, FromID: &tg.PeerUser{UserID: 42}},
		},
		Users: []tg.UserClass{user},
	}
	// Вложенные envelope-структуры обрабатываются как раньше (раунд 48):
	// экспортируемые поля собираются на любой глубине.
	wrapped := round52ResultWrapper{wrappedValue: deep}
	users, _ := collectResponsePeerSlices(wrapped)
	if len(users) != 1 || users[0].(*tg.User).ID != 42 {
		t.Fatalf("expected nested envelope user 42, got %v", users)
	}
}
