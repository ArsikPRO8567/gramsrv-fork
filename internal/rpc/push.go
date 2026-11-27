package rpc

import (
	"context"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// applyPushPeerOverlays — граница доставки прямых пушей (pushUserUpdates и
// одноадресные рассылки мимо outbox-конвейера). Раунд 49 (анти-мерцание):
// оверлеи уже применялись на границе RPC-ответов (applyResponsePeerOverlays) и
// на клеймах outbox-работника (BuildOutboxUpdates → applyOutboxPeerOverlays),
// но ~70 прямых push-сайтов собирали tg.User/tg.Channel объекты «как есть» —
// со скалярным username без вектора, без bot_verification_icon и без
// bot_can_edit. Официальный Android-клиент затирает кэш пира каждым таким
// пушем (MessagesController.putUsers), поэтому значки и коллекционные
// юзернеймы «мигали» между ответом и ближайшим пушем.
//
// Три операции те же и идемпотентны: полный username-вектор из реестра,
// bot_verification_icon и детерминированный owner-вид (bot_can_edit/профильные
// флаги) строго для получателя. Пуш без peer-объектов — пустой обход (ноль
// запросов), так что typing/presence не получает регрессии цены.
//
// Реестровые/верификационные чтения для пира пропускаются, если собранный
// объект УЖЕ несёт полное значение (username-вектор / icon): так работают
// посвящённые notify-пути (NotifyPeerUsernamesChanged, moderation/verification
// fan-out), которые держат контракт «один peer-wide read на фан-аут» и не
// должны платить второй запрос на границе доставки. Для username-чтений
// действует ещё и короткий TTL-кэш отрицательных вердиктов
// (usernameNoCollectible): его пишет КАЖДОЕ чтение реестра
// (usernameRegistryMap), включая fan-out prefetch, поэтому пуш после prefetch
// не платит повторный батч, а серия пушей одного события — не платит повторных
// чтений вовсе. Ответная граница кэш не читает и всегда авторитетна.
func (r *Router) applyPushPeerOverlays(ctx context.Context, viewerUserID int64, msg tg.UpdatesClass) {
	if r == nil || viewerUserID == 0 || msg == nil {
		return
	}
	updates, ok := msg.(*tg.Updates)
	if !ok || updates == nil {
		return
	}
	if len(updates.Users)+len(updates.Chats) == 0 {
		return
	}
	usersNoVector, chatsNoVector := peerObjectsWithoutUsernameVector(updates.Users, updates.Chats)
	if len(usersNoVector)+len(chatsNoVector) > 0 {
		// TTL-кэш отрицательных вердиктов: пиры, для которых недавно чтение
		// реестра уже сказало «коллекционных строк нет», не перечитываются.
		peers := appendUsernameProjectionPeers(nil, make(map[domain.Peer]struct{}, 8), usersNoVector, chatsNoVector)
		pending := r.usernameNegativesPending(peers)
		if len(pending) < len(peers) {
			usersNoVector, chatsNoVector = peerObjectsForPeers(usersNoVector, chatsNoVector, pending)
		}
		if len(usersNoVector)+len(chatsNoVector) > 0 {
			r.applyUsernamesToPeerObjects(ctx, usersNoVector, chatsNoVector)
		}
	}
	usersNoIcon, chatsNoIcon := peerObjectsWithoutVerificationIcon(updates.Users, updates.Chats)
	if len(usersNoIcon)+len(chatsNoIcon) > 0 {
		r.applyBotVerificationIconsToPeerObjects(ctx, usersNoIcon, chatsNoIcon)
	}
	r.applyBotOwnerViewToUsers(ctx, viewerUserID, updates.Users)
}

// peerObjectsForPeers оставляет только объекты, чей peer входит в pending.
func peerObjectsForPeers(users []tg.UserClass, chats []tg.ChatClass, pending []domain.Peer) ([]tg.UserClass, []tg.ChatClass) {
	if len(pending) == 0 {
		return nil, nil
	}
	want := make(map[domain.Peer]struct{}, len(pending))
	for _, peer := range pending {
		want[peer] = struct{}{}
	}
	var filteredUsers []tg.UserClass
	var filteredChats []tg.ChatClass
	for _, item := range users {
		if u, ok := item.(*tg.User); ok && u != nil {
			if _, ok := want[domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}]; ok {
				filteredUsers = append(filteredUsers, u)
			}
		}
	}
	for _, item := range chats {
		if ch, ok := item.(*tg.Channel); ok && ch != nil {
			if _, ok := want[domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}]; ok {
				filteredChats = append(filteredChats, ch)
			}
		}
	}
	return filteredUsers, filteredChats
}

// peerObjectsWithoutUsernameVector отбирает peer-объекты, у которых флаг
// usernames не выставлен (вектор не проецировался), а флаг scalar username
// есть или пуст — иными словами всё, что ещё не прошло username-оверлей.
func peerObjectsWithoutUsernameVector(users []tg.UserClass, chats []tg.ChatClass) ([]tg.UserClass, []tg.ChatClass) {
	var filteredUsers []tg.UserClass
	var filteredChats []tg.ChatClass
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil {
			continue
		}
		if _, hasVector := u.GetUsernames(); hasVector {
			continue
		}
		filteredUsers = append(filteredUsers, u)
	}
	for _, item := range chats {
		ch, ok := item.(*tg.Channel)
		if !ok || ch == nil {
			continue
		}
		if _, hasVector := ch.GetUsernames(); hasVector {
			continue
		}
		filteredChats = append(filteredChats, ch)
	}
	return filteredUsers, filteredChats
}

// peerObjectsWithoutVerificationIcon отбирает peer-объекты без уже
// спроецированного bot_verification_icon: их значок подлежит одному батч-
// чтению на границе доставки. Объекты с установленным флагом уже несут
// свежее значение от своего notify-пути и не перечитываются.
func peerObjectsWithoutVerificationIcon(users []tg.UserClass, chats []tg.ChatClass) ([]tg.UserClass, []tg.ChatClass) {
	var filteredUsers []tg.UserClass
	var filteredChats []tg.ChatClass
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil {
			continue
		}
		if _, hasIcon := u.GetBotVerificationIcon(); hasIcon {
			continue
		}
		filteredUsers = append(filteredUsers, u)
	}
	for _, item := range chats {
		ch, ok := item.(*tg.Channel)
		if !ok || ch == nil {
			continue
		}
		if _, hasIcon := ch.GetBotVerificationIcon(); hasIcon {
			continue
		}
		filteredChats = append(filteredChats, ch)
	}
	return filteredUsers, filteredChats
}

func (r *Router) pushUserMessage(ctx context.Context, userID int64, logMessage string, msg tg.UpdatesClass) int {
	if r.deps.Sessions == nil || userID == 0 || msg == nil {
		return 0
	}
	r.applyPushPeerOverlays(ctx, userID, msg)
	sessionID, _ := SessionIDFrom(ctx)
	if timeout := r.cfg.OutboundPushTimeout; timeout > 0 {
		authKeyID := rawAuthKeyIDForOrigin(ctx)
		if bestEffort, ok := r.deps.Sessions.(BestEffortSessionBinder); ok {
			if sent, err := bestEffort.PushToUserExceptAuthKeySessionBestEffort(ctx, userID, authKeyID, sessionID, proto.MessageFromServer, msg, timeout); err != nil {
				r.log.Debug(logMessage, zap.Int64("user_id", userID), zap.Int("sent", sent), zap.Duration("timeout", timeout), zap.Error(err))
				return sent
			} else {
				return sent
			}
		}
	}
	authKeyID := rawAuthKeyIDForOrigin(ctx)
	if sent, err := r.deps.Sessions.PushToUserExceptAuthKeySession(ctx, userID, authKeyID, sessionID, proto.MessageFromServer, msg); err != nil {
		r.log.Debug(logMessage, zap.Int64("user_id", userID), zap.Int("sent", sent), zap.Error(err))
		return sent
	} else {
		return sent
	}
}

// pushUserMessageTransient 推送 transient（typing/presence）update：未就绪的 session 直接
// 跳过、不进 pending。实现未提供 TransientSessionBinder 能力时回退到普通 pushUserMessage
// （退化为旧行为：会进 pending，但仍不影响 durable 正确性）。
func (r *Router) pushUserMessageTransient(ctx context.Context, userID int64, logMessage string, msg tg.UpdatesClass) int {
	if r.deps.Sessions == nil || userID == 0 || msg == nil {
		return 0
	}
	if transient, ok := r.deps.Sessions.(TransientSessionBinder); ok {
		r.applyPushPeerOverlays(ctx, userID, msg)
		sessionID, _ := SessionIDFrom(ctx)
		authKeyID := rawAuthKeyIDForOrigin(ctx)
		sent, err := transient.PushToUserTransientExceptAuthKeySession(ctx, userID, authKeyID, sessionID, proto.MessageFromServer, msg, r.cfg.OutboundPushTimeout)
		if err != nil {
			r.log.Debug(logMessage, zap.Int64("user_id", userID), zap.Int("sent", sent), zap.Error(err))
		}
		return sent
	}
	return r.pushUserMessage(ctx, userID, logMessage, msg)
}

func (r *Router) pushCurrentSessionMessage(ctx context.Context, logMessage string, msg tg.UpdatesClass) {
	if r.deps.Sessions == nil || msg == nil {
		return
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	// Раунд 49 (анти-мерцание): та же граница оверлеев для односеансовой
	// доставки (viewer — владелец текущей авторизации).
	if userID, hasUser := UserIDFrom(ctx); hasUser {
		r.applyPushPeerOverlays(ctx, userID, msg)
	}
	if err := r.deps.Sessions.PushToSessionForAuthKey(ctx, rawAuthKeyIDForOrigin(ctx), sessionID, proto.MessageFromServer, msg); err != nil {
		r.log.Debug(logMessage, zap.Int64("session_id", sessionID), zap.Error(err))
	}
}
