package rpc

import (
	"context"
	"reflect"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

// Раунд 48 (анти-мерцание peer-флагов): официальный Android-клиент кэширует
// объекты user/chat целиком (MessagesController.putUsers/putChats) и каждый
// ответ без нужных полей ЗАТИРАЕТ кэшированное значение. До раунда 48 оверлеи
// (реестр юзернеймов, кастомная верификация, bot_can_edit) применялись только
// на части границ ответов, поэтому значок редактирования бота у владельца,
// кастомная верификация канала/чата/бота/пользователя и коллекционные
// юзернеймы «мерцали»: появлялись после users.getFullUser и исчезали при
// следующем ответе без оверлея (dialogs/updates/users.getUsers/...).
//
// applyResponsePeerOverlays — единая точка в dispatch(): после успешного
// обработчика ответ проходит рефлексией, из него собираются все слайсы
// []tg.UserClass / []tg.ChatClass и к ним применяются батч-овераи:
//   - applyUsernamesToPeerObjects — полные username#b4073647 вектора
//     (editable/active из реестра; коллекционные юзернеймы перестают
//     мигать между скаляром и вектором);
//   - applyBotVerificationIconsToPeerObjects — user/channel
//     bot_verification_icon (кастомная верификация на всех экранах);
//   - applyBotOwnerViewToUsers — bot-флаги профиля и bot_can_edit
//     (flags2.1) строго для владельца бота.
//
// Все три операции идемпотентны, поэтому ответы, чей обработчик уже применил
// оверлеи через applyPeerReadModels, получают те же значения повторно; цена —
// один батч-запрос на ответ с пир-объектами, зато ни одна граница не может
// «забыть» оверлей. Ошибки чтения сервисов оверлеи трактуют как «нет данных»
// и молча оставляют ответ без изменений (как и applyPeerReadModels).
const peerOverlayMaxDepth = 6

var (
	userClassSliceType = reflect.TypeOf((*[]tg.UserClass)(nil)).Elem()
	chatClassSliceType = reflect.TypeOf((*[]tg.ChatClass)(nil)).Elem()
)

// applyResponsePeerOverlays применяет peer-овераи к одному RPC-ответу.
func (r *Router) applyResponsePeerOverlays(ctx context.Context, resp any) {
	if r == nil || resp == nil {
		return
	}
	userID, ok := UserIDFrom(ctx)
	if !ok || userID == 0 {
		return
	}
	users, chats := collectResponsePeerSlices(resp)
	if len(users) == 0 && len(chats) == 0 {
		return
	}
	r.applyUsernamesToPeerObjects(ctx, users, chats)
	r.applyBotVerificationIconsToPeerObjects(ctx, users, chats)
	r.applyBotOwnerViewToUsers(ctx, userID, users)
}

// collectResponsePeerSlices собирает все слайсы []tg.UserClass и []tg.ChatClass
// из тела ответа (обход в глубину с ограничением и защитой от циклов по
// указателям). Собираются поля любого имени: важен тип, а не соглашение,
// потому что вложенные envelope-типы (users.userFull, payments.starsStatus,
// updates.difference и т.п.) несут Users/Chats под разными ветками.
//
// Раунд 52 (краш сервера при подключении клиента): DispatchAdmitted передаёт
// сюда обёртку tlprofile.Result, чьи поля prepared/call/value неэкспортируемы.
// Обход заходил в них — reflect помечает такие значения флагом flagRO, и
// первый же Users/Chats канонического tg-ответа под value ронял сервер
// паникой «Interface: cannot return value obtained from unexported field»
// (reflect/value.go:1484). Путь dispatch() в router.go разворачивает обёртку
// сам через CanonicalValue, поэтому там дефекта не было; здесь разворачивание
// делается до обхода, для всех вызывающих сторон операция идемпотентна:
// tg-типы и []tg.UserClass интерфейс Result не реализуют.
//
// Неэкспортируемые поля структур пропускаются целиком, вместе с поддеревом:
// любые значения под ними несут flagRO и Interface() для них запрещён, а
// пиры tg-типы всегда несут в экспортируемых полях. CanInterface() —
// страховка от будущих источников flagRO: функция в hot-path RPC не должна
// уметь паниковать.
func collectResponsePeerSlices(resp any) ([]tg.UserClass, []tg.ChatClass) {
	if wrapper, ok := resp.(tlprofile.Result); ok {
		if canonical := wrapper.CanonicalValue(); canonical != nil {
			resp = canonical
		}
	}
	var users []tg.UserClass
	var chats []tg.ChatClass
	visited := make(map[uintptr]struct{}, 8)
	root := reflect.ValueOf(resp)
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if depth > peerOverlayMaxDepth {
			return
		}
		switch v.Kind() {
		case reflect.Interface, reflect.Ptr:
			if v.IsNil() {
				return
			}
			if v.Kind() == reflect.Ptr {
				key := v.Pointer()
				if _, done := visited[key]; done {
					return
				}
				visited[key] = struct{}{}
			}
			walk(v.Elem(), depth+1)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				field := v.Field(i)
				if !field.CanInterface() {
					// Значение получено через неэкспортируемое поле
					// (flagRO): Interface() на нём паникует. Пропускаем
					// поле целиком, вместе с поддеревом.
					continue
				}
				switch field.Kind() {
				case reflect.Slice:
					if field.Type() == userClassSliceType {
						users = append(users, field.Interface().([]tg.UserClass)...)
					} else if field.Type() == chatClassSliceType {
						chats = append(chats, field.Interface().([]tg.ChatClass)...)
					} else if field.Type().Elem().Kind() == reflect.Interface || field.Type().Elem().Kind() == reflect.Ptr || field.Type().Elem().Kind() == reflect.Struct {
						// Векторы envelope-объектов (сообщения, диалоги) могут
						// нести вложенные пиров — обходим их тоже.
						for j := 0; j < field.Len(); j++ {
							walk(field.Index(j), depth+1)
						}
					}
				case reflect.Interface, reflect.Ptr, reflect.Struct:
					walk(field, depth+1)
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), depth+1)
			}
		}
	}
	walk(root, 0)
	return users, chats
}

// applyBotOwnerViewToUsers ставит bot_can_edit (flags2.1) и флаги профиля бота
// на user-объекты ответа. bot_can_edit — viewer-scoped: true только когда
// viewer является владельцем бота (тот же предикат, что у Bots.OwnsBot:
// profile.OwnerUserID == viewer и бот не BotFather). Батч BotInfos даёт и
// флаги, и владельца одним запросом; для ботов без профиля — точечный OwnsBot.
func (r *Router) applyBotOwnerViewToUsers(ctx context.Context, viewerUserID int64, users []tg.UserClass) {
	if r.deps.Bots == nil || viewerUserID == 0 || len(users) == 0 {
		return
	}
	byID := make(map[int64][]*tg.User, 2)
	ids := make([]int64, 0, 2)
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil || !u.Bot || u.ID == 0 {
			continue
		}
		if _, seen := byID[u.ID]; !seen {
			ids = append(ids, u.ID)
		}
		byID[u.ID] = append(byID[u.ID], u)
	}
	if len(ids) == 0 {
		return
	}
	// Раунд 49 (анти-мерцание): профили ботов идут через короткий TTL-кэш,
	// чтобы пуш-фан-аут с упоминавшим ботом не платил BotInfos-батч на
	// каждого получателя, а повторные оверлеи одного события стали бесплатными.
	profiles := map[int64]domain.BotProfile{}
	misses := make([]int64, 0, len(ids))
	for _, id := range ids {
		if profile, found, ok := r.botOwnerViewLookup(id); ok {
			if found {
				profiles[id] = profile
			}
			continue
		}
		misses = append(misses, id)
	}
	if len(misses) > 0 {
		loaded := map[int64]domain.BotProfile{}
		loadedOK := false
		if batch, ok := r.deps.Bots.(botProfileBatchResolver); ok {
			if loadedByID, err := batch.BotInfos(ctx, misses); err == nil {
				loaded = loadedByID
				loadedOK = true
			}
		} else {
			loadedOK = true
			for _, id := range misses {
				if profile, found, err := r.deps.Bots.BotInfo(ctx, id); err == nil && found {
					loaded[id] = profile
				}
			}
		}
		if loadedOK {
			prime := make(map[int64]domain.BotProfile, len(loaded))
			for id, profile := range loaded {
				profiles[id] = profile
				prime[id] = profile
			}
			r.botOwnerViewPrime(prime, misses)
		}
	}
	for id, items := range byID {
		profile, found := profiles[id]
		if !found {
			// Профиля нет — владелец неизвестен: флаг детерминированно снимается,
			// чтобы чужой ответ не мог протащить owner-вид из чужого кэша клиента.
			owns, ownsErr := r.deps.Bots.OwnsBot(ctx, viewerUserID, id)
			for _, u := range items {
				u.SetBotCanEdit(ownsErr == nil && owns)
			}
			continue
		}
		isOwner := profile.OwnerUserID == viewerUserID && id != domain.BotFatherUserID
		for _, u := range items {
			applyBotProfileFlags(u, profile)
			// Раунд 48 (анти-мерцание): значение ставится ДЕТЕРМИНИРОВАННО —
			// true только владельцу, иначе снимается. Клиент затирает кэш
			// user-объекта целиком каждым ответом, поэтому «оставить как было»
			// нельзя: не-owner ответ обязан нести can_edit=false.
			u.SetBotCanEdit(isOwner)
		}
	}
}

// applyOutboxPeerOverlays применяет те же оверлеи к пушу outbox-работника
// (BuildOutboxUpdates). Верификация peer-scoped — один батч на весь клейм;
// bot-флаги и bot_can_edit — viewer-scoped, поэтому считаются по группе
// получателя. Юзернеймы в клейм уже проецирует applyUsernamesToUpdatesBatch.
func (r *Router) applyOutboxPeerOverlays(ctx context.Context, updates []*tg.Updates, viewers []int64) {
	if r == nil || len(updates) == 0 || len(updates) != len(viewers) {
		return
	}
	users := make([]tg.UserClass, 0, 4)
	chats := make([]tg.ChatClass, 0, 4)
	for _, update := range updates {
		if update == nil {
			continue
		}
		users = append(users, update.Users...)
		chats = append(chats, update.Chats...)
	}
	if len(users)+len(chats) == 0 {
		return
	}
	r.applyBotVerificationIconsToPeerObjects(ctx, users, chats)
	byViewer := make(map[int64][]tg.UserClass, 2)
	for i, viewer := range viewers {
		if i >= len(updates) || updates[i] == nil {
			continue
		}
		byViewer[viewer] = append(byViewer[viewer], updates[i].Users...)
	}
	for viewer, viewerUsers := range byViewer {
		if viewer == 0 || len(viewerUsers) == 0 {
			continue
		}
		r.applyBotOwnerViewToUsers(ctx, viewer, viewerUsers)
	}
}
