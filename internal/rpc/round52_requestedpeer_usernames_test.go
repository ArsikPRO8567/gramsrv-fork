package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Round52 fix 1: обе стороны приватного диалога получают универсальный
// messageActionRequestedPeer#31518e9b. Исходящая копия больше не кодируется
// конструктором messageActionRequestedPeerSentMe#93b31848, которого нет в схеме
// клиента OwpenGram: TLParseException ронял разбор всей пачки
// updates.getDifference / messages.getHistory (клиентский лог 28_11_2026).
func TestRequestedPeerServiceActionUsesUniversalConstructorForBothSides(t *testing.T) {
	action := &domain.MessageRequestedPeerAction{
		ButtonID: 7,
		Peers: []domain.Peer{
			{Type: domain.PeerTypeUser, ID: 501},
			{Type: domain.PeerTypeChannel, ID: 602},
		},
	}
	for _, out := range []bool{false, true} {
		msg := domain.Message{
			Out:        out,
			OwnerUserID: 1780243200,
			Media: &domain.MessageMedia{
				Kind: domain.MessageMediaKindService,
				ServiceAction: &domain.MessageServiceAction{
					Kind:          domain.MessageServiceActionRequestedPeer,
					RequestedPeer: action,
				},
			},
		}
		projected := tgMessageServiceAction(msg)
		requested, ok := projected.(*tg.MessageActionRequestedPeer)
		if !ok {
			t.Fatalf("out=%v service action = %T, want *tg.MessageActionRequestedPeer", out, projected)
		}
		if requested.ButtonID != 7 || len(requested.Peers) != 2 {
			t.Fatalf("out=%v requested peer action = %+v", out, requested)
		}
		if _, isUser := requested.Peers[0].(*tg.PeerUser); !isUser {
			t.Fatalf("out=%v peers[0] = %T, want *tg.PeerUser", out, requested.Peers[0])
		}
		if _, isChannel := requested.Peers[1].(*tg.PeerChannel); !isChannel {
			t.Fatalf("out=%v peers[1] = %T, want *tg.PeerChannel", out, requested.Peers[1])
		}
	}
}

// Round52 fix 2: editable-слот вектора usernames восстанавливается из
// авторитетного скаляра, когда в реестре есть коллекционные имена, но нет
// editable-строки (легаси-аккаунты). Иначе оверлей очищал скаляр, а вектор
// терял основное имя — настройки показывали только коллекционные имена.
func TestTgUsernamesFromRegistryRestoresEditableRowFromScalar(t *testing.T) {
	vector := tgUsernamesFromRegistry([]domain.Username{
		{Username: "legacy_collectible", Active: true, SortOrder: 1, CollectibleID: 42},
	}, "main_username")
	if len(vector) != 2 {
		t.Fatalf("vector = %+v, want 2 entries (editable + collectible)", vector)
	}
	if !vector[0].Editable || !vector[0].Active || vector[0].Username != "main_username" {
		t.Fatalf("vector[0] = %+v, want editable+active main_username", vector[0])
	}
	if vector[1].Editable || !vector[1].Active || vector[1].Username != "legacy_collectible" {
		t.Fatalf("vector[1] = %+v, want non-editable active legacy_collectible", vector[1])
	}

	// Editable-строка с устаревшим именем приводится к скаляру.
	vector = tgUsernamesFromRegistry([]domain.Username{
		{Username: "stale_slot", Editable: true, Active: true},
		{Username: "legacy_collectible", Active: true, SortOrder: 1, CollectibleID: 42},
	}, "fresh_username")
	foundFresh, foundStale := false, false
	for _, item := range vector {
		switch item.Username {
		case "fresh_username":
			if !item.Editable {
				t.Fatalf("fresh_username = %+v, want editable", item)
			}
			foundFresh = true
		case "stale_slot":
			foundStale = true
		}
	}
	if !foundFresh || foundStale {
		t.Fatalf("vector = %+v, want stale_slot renamed to fresh_username", vector)
	}

	// Скаляр совпадает с коллекционной строкой: дубля editable нет.
	vector = tgUsernamesFromRegistry([]domain.Username{
		{Username: "purchased", Active: true, SortOrder: 0, CollectibleID: 9},
	}, "purchased")
	if len(vector) != 1 || vector[0].Editable {
		t.Fatalf("vector = %+v, want single non-editable purchased row", vector)
	}

	// Полный реестр без scalar'а (снимок пользователя старше реестра):
	// editable-строка сохраняется.
	vector = tgUsernamesFromRegistry([]domain.Username{
		{Username: "registry_slot", Editable: true, Active: true, SortOrder: 0},
		{Username: "legacy_collectible", Active: true, SortOrder: 1, CollectibleID: 42},
	}, "")
	if len(vector) != 2 || vector[0].Username != "registry_slot" || !vector[0].Editable {
		t.Fatalf("vector = %+v, want registry_slot editable first", vector)
	}

	// Входные срезы не мутируются (batch-кэш переиспользует слайсы).
	list := []domain.Username{
		{Username: "stale_slot", Editable: true, Active: true},
	}
	_ = tgUsernamesFromRegistry(list, "fresh_username")
	if list[0].Username != "stale_slot" {
		t.Fatalf("input list mutated in place: %+v", list)
	}
}
