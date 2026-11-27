package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// BuildOutboxUpdates 为在线 outbox worker 构造按接收者视角补全后的 updates。
func (r *Router) BuildOutboxUpdates(ctx context.Context, requests []OutboxUpdateRequest) []*tg.Updates {
	out := make([]*tg.Updates, len(requests))
	if len(requests) == 0 {
		return out
	}
	cache := newViewerPeerCache(r)
	groups := make(map[int64][]outboxUpdateBuildItem)
	for i, req := range requests {
		viewerUserID := req.TargetUserID
		if viewerUserID == 0 {
			viewerUserID = req.Event.UserID
		}
		event := req.Event
		if event.UserID == 0 {
			event.UserID = viewerUserID
		}
		groups[viewerUserID] = append(groups[viewerUserID], outboxUpdateBuildItem{index: i, event: event})
	}
	for viewerUserID, items := range groups {
		events := make([]domain.UpdateEvent, len(items))
		for i, item := range items {
			events[i] = item.event
		}
		events = r.enrichUpdateEventsWithPeerCache(ctx, viewerUserID, events, cache)
		viewerUpdates := make([]*tg.Updates, 0, len(items))
		for i, item := range items {
			update := tgUpdateForOutboxEventForViewer(events[i], viewerUserID)
			if peers := storyUpdateEventPeers(events[i]); len(peers) > 0 {
				update = r.withStoryUpdatePeerObjectsForOutbox(ctx, viewerUserID, update, peers...)
			}
			out[item.index] = update
			viewerUpdates = append(viewerUpdates, update)
		}
		r.applyBotCanEditToOutboxUpdates(ctx, viewerUserID, viewerUpdates)
	}
	// Username rows are viewer-independent. Project the union once after every
	// viewer-specific update has been built so one outbox claim never turns into
	// a registry query per event/session.
	r.applyUsernamesToUpdatesBatch(ctx, out)
	return out
}

// applyBotCanEditToOutboxUpdates помечает боты одного зрителя флагом
// bot_can_edit во всех его исходящих updates. Клиент перезаписывает кэш
// пользователя объектом из каждого update (в том числе из updateNewMessage,
// где бот — автор сообщения), поэтому пропущенный флаг гасит «карандаш»
// редактирования бота до следующего users.getUsers. Решение о владении
// кэшируется на один батч зрителя — события идут пачками, а один и тот же
// пользователь-бот повторяется в большинстве из них.
func (r *Router) applyBotCanEditToOutboxUpdates(ctx context.Context, viewerUserID int64, updates []*tg.Updates) {
	if r.deps.Bots == nil || viewerUserID == 0 || len(updates) == 0 {
		return
	}
	owned := make(map[int64]bool)
	for _, update := range updates {
		if update == nil {
			continue
		}
		users, _ := collectResponsePeerSlices(update)
		for _, item := range users {
			u, ok := item.(*tg.User)
			if !ok || u == nil || !u.Bot || u.ID == viewerUserID {
				continue
			}
			owns, known := owned[u.ID]
			if !known {
				owns, _ = r.deps.Bots.OwnsBot(ctx, viewerUserID, u.ID)
				owned[u.ID] = owns
			}
			if owns {
				u.SetBotCanEdit(true)
			}
		}
	}
}

type outboxUpdateBuildItem struct {
	index int
	event domain.UpdateEvent
}
