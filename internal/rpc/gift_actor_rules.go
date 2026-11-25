package rpc

import (
	"context"

	"telesrv/internal/domain"
)

// Раунд 45: правила «кому нельзя дарить» и требование знакомства бота.
//
// Официальный сервер запрещает отправлять подарки служебным аккаунтам и ботам:
// попытка обычного аккаунта выполнить подарочное действие в адрес такого пира
// завершается USER_GIFT_UNAVAILABLE (аккаунт) или PEER_ID_INVALID (бот).
// Существующий флаг support на ограничение не влияет. Аккаунты и боты с флагом
// service_account сами лишены этих ограничений: они свободно отправляют
// подарки другим служебным аккаунтам и даже ботам.
//
// Требование знакомства: бот, который ещё «не знаком» с пользователем (не
// имеет и никогда не имел чата с ним), не может отправлять подарки и выполнять
// другие подарочные действия в отношении этого пользователя и его подарков.
//
// Охватываемые действия: отправка подарка (catalog), предоплата улучшения
// чужого подарка (адресат — владелец подарка), передача коллекционного подарка,
// покупка на маркете, предложение о покупке, подарочная Premium-подписка.

// GiftActorRulesStore — границы store.UserStore (миграция 0200), нужные
// правилам дарения. Реализуют postgres.UserStore и memory.UserStore.
type GiftActorRulesStore interface {
	// ServiceAccount читает флаг service_account пользователя.
	ServiceAccount(ctx context.Context, userID int64) (bool, error)
	// HasPrivateChatEver сообщает, существовал ли когда-либо (сейчас или
	// раньше, включая удалённые переписки) приватный чат между двумя
	// пользователями.
	HasPrivateChatEver(ctx context.Context, a, b int64) (bool, error)
}

// userGiftUnavailableErr объявлена в payments_star_gifts.go:
// tgerr.New(400, "USER_GIFT_UNAVAILABLE") — официальный ответ при попытке
// подарочного действия в адрес служебного аккаунта (или незнакомого боту
// пользователя).

// actorIsServiceAccount сообщает, помечен ли отправитель действия флагом
// service_account. Ошибки чтения трактуются как «не служебный» (fail-open:
// ограничение не должно блокировать обычных пользователей из-за сбоя чтения
// флага).
func (r *Router) actorIsServiceAccount(ctx context.Context, store GiftActorRulesStore, userID int64) bool {
	if store == nil || userID <= 0 {
		return false
	}
	service, err := store.ServiceAccount(ctx, userID)
	return err == nil && service
}

// enforceGiftRecipientRules применяет раунд-45 правила к подарочному действию
// actorUserID → target (пользователь). Возвращает nil, когда действие
// разрешено. Канальные получатели не ограничиваются.
func (r *Router) enforceGiftRecipientRules(ctx context.Context, actorUserID int64, target domain.Peer) error {
	if target.Type != domain.PeerTypeUser || target.ID <= 0 {
		return nil
	}
	store := r.deps.GiftActorRules
	if store == nil {
		return nil
	}
	if r.actorIsServiceAccount(ctx, store, actorUserID) {
		// Служебные аккаунты и боты дарят свободно — в том числе другим
		// служебным аккаунтам и ботам (требование раунда).
		return nil
	}
	targetService, err := store.ServiceAccount(ctx, target.ID)
	if err != nil {
		targetService = false
	}
	targetBot := false
	if r.deps.Users != nil {
		if u, found, err := r.deps.Users.ByID(ctx, actorUserID, target.ID); err == nil && found {
			targetBot = u.Bot
		}
	}
	// Бот-получатель: официальный сервер отвечает PEER_ID_INVALID независимо
	// от того, служебный это бот или обычный.
	if targetBot {
		return peerIDInvalidErr()
	}
	if targetService {
		return userGiftUnavailableErr()
	}
	// Знакомство: бот, никогда не имевший чата с пользователем, не может
	// выполнять подарочные действия в его отношении.
	if actor, found, err := r.deps.Users.ByID(ctx, actorUserID, actorUserID); err == nil && found && actor.Bot {
		chatted, err := store.HasPrivateChatEver(ctx, actorUserID, target.ID)
		if err != nil {
			chatted = true
		}
		if !chatted {
			return userGiftUnavailableErr()
		}
	}
	return nil
}
