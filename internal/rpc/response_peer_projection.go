package rpc

import (
	"context"
	"reflect"

	"github.com/iamxvbaba/td/tg"
)

// Центральная проекция peer-объектов на границе ответа RPC
// (projectResponsePeerObjects, вызывается из DispatchAdmitted).
//
// Один факт о peer (коллекционные юзернеймы, значок кастомной верификации,
// право owner'а редактировать бота) разъезжается по десяткам ответов —
// users.getUsers, users.getFullUser, dialogs, history, contacts, chats — и
// клиент каждый раз целиком перезаписывает свой кэш пользователя/канала тем
// объектом, что пришёл последним. Если хоть один путь отдаёт объект без
// usernames/bot_verification_icon/bot_can_edit, значок или имя «мигает»:
// профиль открыт — есть, открыт чат — пропало, снова профиль — появилось.
// Локальные оверлеи (applyUsernamesToPeerObjects и т.п.) покрывали лишь часть
// точек, поэтому для устойчивости проекция теперь применяется ко ВСЕМ
// успешным RPC-ответам в одном месте, после диспетчеризации.
//
// Каждый слой оверлея идемпотентен и сам решает, читать ли реестр: nil-сервис
// или пустой набор peer'ов — тихий no-op, форма ответа не меняется.

var (
	tgUserClassSliceType = reflect.TypeOf([]tg.UserClass(nil))
	tgChatClassSliceType = reflect.TypeOf([]tg.ChatClass(nil))
)

func (r *Router) projectResponsePeerObjects(ctx context.Context, result any) {
	if r == nil || result == nil {
		return
	}
	users, chats := collectResponsePeerSlices(result)
	if len(users)+len(chats) == 0 {
		return
	}
	r.applyUsernamesToPeerObjects(ctx, users, chats)
	r.applyBotVerificationIconsToPeerObjects(ctx, users, chats)
	if viewer, authorized := UserIDFrom(ctx); authorized && viewer != 0 {
		r.applyBotCanEditToResponseUsers(ctx, viewer, users)
	}
}

// applyBotCanEditToResponseUsers ставит bot_can_edit (flags2.1) на объекты
// ботов, которыми владеет зритель. DrKLO/TDesktop показывают «карандаш»
// редактирования бота только по этому биту; ставка выполняется на каждом
// ответе, содержащем пользователя-бота, чтобы кэш клиента не терял флаг между
// открытиями профиля и чата.
func (r *Router) applyBotCanEditToResponseUsers(ctx context.Context, viewerUserID int64, users []tg.UserClass) {
	if r.deps.Bots == nil {
		return
	}
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil || !u.Bot || u.ID == viewerUserID {
			continue
		}
		owns, err := r.deps.Bots.OwnsBot(ctx, viewerUserID, u.ID)
		if err != nil {
			continue
		}
		if owns {
			u.SetBotCanEdit(true)
		}
	}
}

// collectResponsePeerSlices собирает Users/Chats с верхнего уровня результата.
// Все массовые конструкторы (tg.Updates, MessagesMessages*, MessagesDialogs*,
// UsersUserFull, ContactsFound, MessagesChatFull, ChannelsSendAsPeers, …)
// несут векторы ровно этими полями; users.getUsers возвращает срез напрямую.
// Вложенность внутрь подструктур не обходится — там, где это нужно, остаются
// локальные оверлеи.
func collectResponsePeerSlices(result any) ([]tg.UserClass, []tg.ChatClass) {
	value := reflect.ValueOf(result)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Slice:
		if value.Type() == tgUserClassSliceType {
			users, _ := value.Interface().([]tg.UserClass)
			return users, nil
		}
		if value.Type() == tgChatClassSliceType {
			chats, _ := value.Interface().([]tg.ChatClass)
			return nil, chats
		}
		return nil, nil
	case reflect.Struct:
		var (
			users []tg.UserClass
			chats []tg.ChatClass
		)
		t := value.Type()
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			fieldValue := value.Field(i)
			if fieldValue.Kind() != reflect.Slice || fieldValue.IsNil() {
				continue
			}
			switch field.Name {
			case "Users":
				if fieldValue.Type() == tgUserClassSliceType {
					users, _ = fieldValue.Interface().([]tg.UserClass)
				}
			case "Chats":
				if fieldValue.Type() == tgChatClassSliceType {
					chats, _ = fieldValue.Interface().([]tg.ChatClass)
				}
			}
		}
		return users, chats
	default:
		return nil, nil
	}
}
