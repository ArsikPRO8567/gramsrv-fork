package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/app/peerview"
	"telesrv/internal/domain"
)

// viewerPeerCache 是一次 outbox/fanout 构建内的短生命周期缓存。
// 用户资料含联系人、隐私、头像和在线状态，必须按 viewerUserID 隔离，不能跨视角复用。
type viewerPeerCache struct {
	r *Router

	users        *peerview.BatchCache
	channels     map[int64]map[int64]domain.Channel
	missingChats map[int64]map[int64]struct{}
	// Раунд 49 (анти-мерцание): job-область верификационных иконок, см.
	// primeBotVerificationIcons.
	verificationIcons map[domain.Peer]int64
}

func newViewerPeerCache(r *Router) *viewerPeerCache {
	var users peerview.UserResolver
	if r != nil {
		users = r.deps.Users
	}
	return &viewerPeerCache{
		r:            r,
		users:        peerview.NewBatchCache(users),
		channels:     make(map[int64]map[int64]domain.Channel),
		missingChats: make(map[int64]map[int64]struct{}),
	}
}

func (c *viewerPeerCache) usersForIDs(ctx context.Context, viewerUserID int64, ids []int64) []domain.User {
	if c == nil || c.users == nil {
		return nil
	}
	users, err := c.users.UsersForView(ctx, viewerUserID, ids)
	if err != nil && c.r != nil {
		c.r.log.Warn("batch resolve users for peer projection failed",
			zap.Int64("viewer_user_id", viewerUserID),
			zap.Int("count", len(uniquePeerIDs(ids))),
			zap.Error(err),
		)
	}
	if c.r == nil {
		return users
	}
	return c.r.withUsersPresence(users)
}

// primeUsers 把跨 viewer 一次性投影（ByIDsForViewers）的结果按 viewer 预热进底层 BatchCache，
// 使随后每 viewer 的 usersForIDs 命中缓存、不再逐 viewer 解析投影。仅 fan-out 预热路径调用。
func (c *viewerPeerCache) primeUsers(viewerUserID int64, users []domain.User) {
	if c == nil || c.users == nil {
		return
	}
	c.users.Prime(viewerUserID, users)
}

// Раунд 49 (анти-мерцание): job-область сторонних верификационных иконок
// (user#b1b8cc83 bot_verification_icon:flags2.14 / channel#d49f34c6
// bot_verification_icon:flags2.13). Пуш-фан-ауты затирают кэш пира клиента
// каждым сообщением, поэтому иконка должна ехать в КАЖДОМ peer-объекте.
// Иконки резолвятся один раз на весь fan-out (primeBotVerificationIcons) и
// штампуются билдерами через botVerificationIcons; пуш-оверлей на границе
// доставки видит выставленный флаг и не перечитывает реестр на каждого
// получателя.
func (c *viewerPeerCache) primeBotVerificationIcons(ctx context.Context, peers []domain.Peer) {
	if c == nil || c.r == nil || len(peers) == 0 {
		return
	}
	byPeer := c.r.botVerificationMap(ctx, peers)
	if len(byPeer) == 0 {
		return
	}
	if c.verificationIcons == nil {
		c.verificationIcons = make(map[domain.Peer]int64, len(byPeer))
	}
	for peer, mark := range byPeer {
		if mark.IconDocumentID <= 0 {
			continue
		}
		c.verificationIcons[peer] = mark.IconDocumentID
	}
}

// botVerificationIcons возвращает снимок job-области иконок (только чтение).
func (c *viewerPeerCache) botVerificationIcons() map[domain.Peer]int64 {
	if c == nil {
		return nil
	}
	return c.verificationIcons
}

// applyBotVerificationIconsFromSnapshot штампует заранее разрешённые иконки на
// peer-объекты одного fan-out. Объекты без записи в снимке остаются без флага —
// их дозакрывает пуш-оверлей на границе доставки.
func applyBotVerificationIconsFromSnapshot(users []tg.UserClass, chats []tg.ChatClass, icons map[domain.Peer]int64) {
	if len(icons) == 0 {
		return
	}
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil || u.ID == 0 {
			continue
		}
		if icon, ok := icons[domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}]; ok && icon > 0 {
			u.SetBotVerificationIcon(icon)
		}
	}
	for _, item := range chats {
		ch, ok := item.(*tg.Channel)
		if !ok || ch == nil || ch.ID == 0 {
			continue
		}
		if icon, ok := icons[domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}]; ok && icon > 0 {
			ch.SetBotVerificationIcon(icon)
		}
	}
}

func (c *viewerPeerCache) channelsForIDs(ctx context.Context, viewerUserID int64, ids []int64) []domain.Channel {
	unique := uniquePeerIDs(ids)
	if c == nil || c.r == nil || len(unique) == 0 || c.r.deps.Channels == nil || viewerUserID == 0 {
		return nil
	}
	byID := c.viewerChannels(viewerUserID)
	missing := c.viewerMissingChannels(viewerUserID)
	load := make([]int64, 0, len(unique))
	for _, id := range unique {
		if _, ok := byID[id]; ok {
			continue
		}
		if _, ok := missing[id]; ok {
			continue
		}
		load = append(load, id)
	}
	if len(load) > 0 {
		views, err := c.r.deps.Channels.GetChannels(ctx, viewerUserID, load)
		if err != nil {
			for _, id := range load {
				missing[id] = struct{}{}
			}
		} else {
			found := make(map[int64]struct{}, len(views))
			for _, view := range views {
				if view.Channel.ID == 0 {
					continue
				}
				byID[view.Channel.ID] = view.Channel
				found[view.Channel.ID] = struct{}{}
			}
			for _, id := range load {
				if _, ok := found[id]; !ok {
					missing[id] = struct{}{}
				}
			}
		}
	}
	out := make([]domain.Channel, 0, len(unique))
	for _, id := range unique {
		if ch, ok := byID[id]; ok {
			out = append(out, ch)
		}
	}
	return out
}

func (c *viewerPeerCache) viewerChannels(viewerUserID int64) map[int64]domain.Channel {
	if byID, ok := c.channels[viewerUserID]; ok {
		return byID
	}
	byID := make(map[int64]domain.Channel)
	c.channels[viewerUserID] = byID
	return byID
}

func (c *viewerPeerCache) viewerMissingChannels(viewerUserID int64) map[int64]struct{} {
	if missing, ok := c.missingChats[viewerUserID]; ok {
		return missing
	}
	missing := make(map[int64]struct{})
	c.missingChats[viewerUserID] = missing
	return missing
}

func uniquePeerIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func peerIDMapKeys(ids map[int64]struct{}) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	for id := range ids {
		if id != 0 {
			out = append(out, id)
		}
	}
	return out
}
