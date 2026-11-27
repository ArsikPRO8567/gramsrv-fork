package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

// Collectible (Fragment-style) usernames on the protocol edge.
//
// This file owns three things:
//
//   - fragment.getCollectibleInfo, the purchase-record lookup a client opens from
//     the "this username was bought on Fragment" badge;
//   - the projection overlay that turns the legacy single-username vector into
//     the full username#b4073647 list (editable slot + collectibles);
//   - the shared toggle/reorder/deactivate plumbing behind
//     account.*, channels.* and bots.* username management.
//
// Every entry point degrades: with Deps.Usernames nil (or on any registry error)
// the wire shape is exactly what it was before collectibles existed.

// registerFragment 注册 fragment.* RPC handler。
func (r *Router) registerFragment(d *tlprofile.Dispatcher) {
	registerRPC[*tg.FragmentGetCollectibleInfoRequest](d, tlprofile.SemanticMethodFragmentGetCollectibleInfo, func(ctx context.Context, layerRequest *tg.FragmentGetCollectibleInfoRequest) (any, error) {
		return r.onFragmentGetCollectibleInfo(ctx, layerRequest)
	})
}

// onFragmentGetCollectibleInfo answers fragment.getCollectibleInfo.
//
// Only inputCollectibleUsername is answerable here: this server has no
// collectible-phone registry. The method exposes only assets visible to the
// caller: their own inactive collectible, a collectible attached to a channel
// they own, or anybody's active collectible.
func (r *Router) onFragmentGetCollectibleInfo(ctx context.Context, req *tg.FragmentGetCollectibleInfoRequest) (*tg.FragmentCollectibleInfo, error) {
	if req == nil {
		return nil, collectibleInvalidErr()
	}
	// An authenticated caller is required, matching every other profile lookup.
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	switch collectible := req.Collectible.(type) {
	case *tg.InputCollectibleUsername:
		if collectible == nil {
			return nil, collectibleInvalidErr()
		}
		return r.collectibleUsernameInfo(ctx, userID, collectible.Username)
	case *tg.InputCollectiblePhone:
		if collectible == nil || strings.TrimSpace(collectible.Phone) == "" {
			return nil, collectibleInvalidErr()
		}
		return r.collectiblePhoneInfo(ctx, userID, collectible.Phone)
	default:
		return nil, collectibleInvalidErr()
	}
}

func (r *Router) collectiblePhoneInfo(ctx context.Context, viewerUserID int64, phone string) (*tg.FragmentCollectibleInfo, error) {
	number := domain.NormalizeCollectiblePhone(phone)
	if number == "" {
		return nil, collectibleInvalidErr()
	}
	if !domain.ValidCollectiblePhone(number) {
		return nil, collectibleNotFoundErr()
	}
	if r.deps.CollectiblePhones == nil || r.deps.Users == nil {
		return nil, collectibleNotFoundErr()
	}
	asset, err := r.deps.CollectiblePhones.CollectiblePhone(ctx, number)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrCollectiblePhoneInvalid):
			return nil, collectibleInvalidErr()
		case errors.Is(err, domain.ErrCollectiblePhoneNotFound), errors.Is(err, domain.ErrCollectiblePhoneNotOwned), errors.Is(err, domain.ErrCollectiblePhoneBurned):
			return nil, collectibleNotFoundErr()
		default:
			return nil, internalErr()
		}
	}
	if !asset.Owned() {
		return nil, collectibleNotFoundErr()
	}
	projected, found, err := r.deps.Users.ByID(ctx, viewerUserID, asset.OwnerUserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || projected.Phone != asset.Phone {
		return nil, collectibleNotFoundErr()
	}
	info := asset.Info()
	return &tg.FragmentCollectibleInfo{PurchaseDate: info.PurchaseDate, Currency: info.Currency,
		Amount: info.Amount, CryptoCurrency: info.CryptoCurrency, CryptoAmount: info.CryptoAmount, URL: info.URL}, nil
}

func (r *Router) collectibleUsernameInfo(ctx context.Context, userID int64, username string) (*tg.FragmentCollectibleInfo, error) {
	name := domain.NormalizeUsername(username)
	// Syntax first: a name that cannot be a collectible is COLLECTIBLE_INVALID, and
	// rejecting it here keeps malformed input off the registry.
	if !domain.ValidCollectibleUsername(name) {
		return nil, collectibleInvalidErr()
	}
	if r.deps.Usernames == nil {
		return nil, collectibleNotFoundErr()
	}
	asset, err := r.deps.Usernames.Collectible(ctx, name)
	if err != nil {
		return nil, collectibleInfoErr(err)
	}
	if !asset.Owned() {
		return nil, collectibleNotFoundErr()
	}
	visible, err := r.collectibleVisibleTo(ctx, userID, asset)
	if err != nil {
		return nil, internalErr()
	}
	if !visible {
		// Do not reveal that an inactive collectible is associated with another
		// peer. The official surface intentionally collapses that state to not found.
		return nil, collectibleNotFoundErr()
	}
	info := asset.Info()
	out := &tg.FragmentCollectibleInfo{
		PurchaseDate:   info.PurchaseDate,
		Currency:       info.Currency,
		Amount:         info.Amount,
		CryptoCurrency: info.CryptoCurrency,
		CryptoAmount:   info.CryptoAmount,
		URL:            info.URL,
	}
	return out, nil
}

func (r *Router) collectibleVisibleTo(ctx context.Context, userID int64, asset domain.CollectibleUsername) (bool, error) {
	if asset.Owner.Type == domain.PeerTypeUser && asset.Owner.ID == userID {
		return true, nil
	}
	list, err := r.deps.Usernames.PeerUsernames(ctx, asset.Owner)
	if err != nil {
		return false, err
	}
	for _, item := range list {
		if item.CollectibleID == asset.ID && item.Active {
			return true, nil
		}
	}
	if asset.Owner.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return false, nil
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, asset.Owner.ID)
	if err != nil {
		// Inaccessible channels are indistinguishable from an inactive asset owned
		// by somebody else.
		return false, nil
	}
	return view.Self.Role == domain.ChannelRoleCreator, nil
}

func collectibleInvalidErr() error  { return tgerr400("COLLECTIBLE_INVALID") }
func collectibleNotFoundErr() error { return tgerr400("COLLECTIBLE_NOT_FOUND") }

// collectibleInfoErr maps registry lookup failures onto TL.
func collectibleInfoErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrCollectibleUsernameNotFound),
		errors.Is(err, domain.ErrUsernameNotOccupied),
		errors.Is(err, domain.ErrCollectibleUsernameBurned),
		errors.Is(err, domain.ErrCollectibleUsernameNotOwned):
		return collectibleNotFoundErr()
	case errors.Is(err, domain.ErrUsernameNotCollectible),
		errors.Is(err, domain.ErrUsernameInvalid):
		return collectibleInvalidErr()
	default:
		return internalErr()
	}
}

// collectibleUsernameErr maps registry mutation failures onto TL.
//
// domain.ErrUsernameNotCollectible and domain.ErrUsernameOrderInvalid both mean
// "the client asked for something the collectible slots cannot express" -- moving
// or deactivating the editable slot, or an order that is not a permutation of the
// peer's collectibles -- so both are USERNAME_INVALID.
func collectibleUsernameErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrUsernameNotCollectible),
		errors.Is(err, domain.ErrUsernameOrderInvalid),
		errors.Is(err, domain.ErrUsernameNotEditable),
		errors.Is(err, domain.ErrUsernameInvalid):
		return usernameInvalidErr()
	case errors.Is(err, domain.ErrUsernameNotOccupied),
		errors.Is(err, domain.ErrCollectibleUsernameNotFound),
		errors.Is(err, domain.ErrCollectibleUsernameNotOwned),
		errors.Is(err, domain.ErrCollectibleUsernameBurned):
		return usernameNotOccupiedErr()
	case errors.Is(err, domain.ErrCollectibleUsernameLimit):
		return limitInvalidErr()
	default:
		return internalErr()
	}
}

// toggleRegistryUsername is the shared body of account/channels/bots
// .toggleUsername. Callers do the permission check first.
func (r *Router) toggleRegistryUsername(ctx context.Context, peer domain.Peer, username string, active bool) error {
	name := domain.NormalizeUsername(username)
	if !domain.ValidCollectibleUsername(name) {
		return usernameInvalidErr()
	}
	changed, err := r.deps.Usernames.ToggleUsername(ctx, peer, name, active)
	if err != nil {
		return collectibleUsernameErr(err)
	}
	if !changed {
		return usernameNotModifiedErr()
	}
	r.invalidateRegistryProjection(peer)
	return nil
}

// reorderRegistryUsernames is the shared body of account/channels/bots
// .reorderUsernames.
func (r *Router) reorderRegistryUsernames(ctx context.Context, peer domain.Peer, order []string) error {
	normalized := make([]string, 0, len(order))
	for _, name := range order {
		normalized = append(normalized, domain.NormalizeUsername(name))
	}
	changed, err := r.deps.Usernames.ReorderUsernames(ctx, peer, normalized)
	if err != nil {
		if errors.Is(err, domain.ErrUsernameOrderInvalid) {
			return tgerr400("ORDER_INVALID")
		}
		return collectibleUsernameErr(err)
	}
	if !changed {
		return usernameNotModifiedErr()
	}
	r.invalidateRegistryProjection(peer)
	return nil
}

// deactivateAllRegistryUsernames is the shared body of
// channels.deactivateAllUsernames: it hides every collectible username of a peer.
//
// Deactivating an empty set is success, not USERNAME_NOT_MODIFIED: Telegram
// Desktop calls channels.deactivateAllUsernames as a step of its "set the
// username" flow, so a peer that has no collectible usernames yet -- the common
// case for a freshly created channel -- would abort that flow on a 400. The
// no-op stub this replaced also answered true, and clients depend on it.
func (r *Router) deactivateAllRegistryUsernames(ctx context.Context, peer domain.Peer) error {
	changed, err := r.deps.Usernames.DeactivateAllUsernames(ctx, peer)
	if err != nil {
		return collectibleUsernameErr(err)
	}
	if !changed {
		return nil
	}
	r.invalidateRegistryProjection(peer)
	return nil
}

// invalidateRegistryProjection drops the cached user/channel projections that
// embed the username vector, so the next getFullUser / getFullChannel rebuilds it.
func (r *Router) invalidateRegistryProjection(peer domain.Peer) {
	switch peer.Type {
	case domain.PeerTypeUser:
		r.invalidateRPCProjectionForUser(peer.ID)
	case domain.PeerTypeChannel:
		r.invalidateRPCProjectionForChannel(peer.ID)
	}
}

// applyUsernamesToPeerObjects overlays the registry onto already-projected user
// and channel objects. Per the Fragment contract, the scalar username is cleared
// and the vector is set only when a collectible is associated with the peer.
//
// It mirrors applyStoryMaxIDsToPeerObjects: one batched read-model call per
// response instead of a per-peer query, and a silent no-op whenever the read
// model is unavailable. Overlaying after projection is what keeps the ~90
// pure tgUser/tgChannel call sites untouched -- they keep emitting the legacy
// scalar, and this pass upgrades it wherever a Router-level entry point runs.
func (r *Router) applyUsernamesToPeerObjects(ctx context.Context, users []tg.UserClass, chats []tg.ChatClass) {
	if r.deps.Usernames == nil || len(users)+len(chats) == 0 {
		return
	}
	peers := make([]domain.Peer, 0, len(users)+len(chats))
	seen := make(map[domain.Peer]struct{}, len(users)+len(chats))
	peers = appendUsernameProjectionPeers(peers, seen, users, chats)
	if len(peers) == 0 {
		return
	}
	byPeer := r.usernameRegistryMap(ctx, peers)
	if len(byPeer) == 0 {
		return
	}
	applyUsernamesFromRegistry(users, chats, byPeer)
}

// applyUsernamesToUpdatesBatch projects one username-registry snapshot over a
// whole outbox claim. A claim may contain repeated peer objects for several
// events and viewers; collecting the peer union first keeps the hot path at one
// registry round trip rather than one read per event or online session.
func (r *Router) applyUsernamesToUpdatesBatch(ctx context.Context, updates []*tg.Updates) {
	if r.deps.Usernames == nil || len(updates) == 0 {
		return
	}
	peerCapacity := 0
	for _, update := range updates {
		if update != nil {
			peerCapacity += len(update.Users) + len(update.Chats)
		}
	}
	if peerCapacity == 0 {
		return
	}
	peers := make([]domain.Peer, 0, peerCapacity)
	seen := make(map[domain.Peer]struct{}, peerCapacity)
	for _, update := range updates {
		if update == nil {
			continue
		}
		peers = appendUsernameProjectionPeers(peers, seen, update.Users, update.Chats)
	}
	if len(peers) == 0 {
		return
	}
	byPeer := r.usernameRegistryMap(ctx, peers)
	if len(byPeer) == 0 {
		return
	}
	for _, update := range updates {
		if update != nil {
			applyUsernamesFromRegistry(update.Users, update.Chats, byPeer)
		}
	}
}

func appendUsernameProjectionPeers(peers []domain.Peer, seen map[domain.Peer]struct{}, users []tg.UserClass, chats []tg.ChatClass) []domain.Peer {
	addPeer := func(peer domain.Peer) {
		if peer.ID == 0 {
			return
		}
		if _, ok := seen[peer]; ok {
			return
		}
		seen[peer] = struct{}{}
		peers = append(peers, peer)
	}
	for _, item := range users {
		if u, ok := item.(*tg.User); ok && u != nil {
			addPeer(domain.Peer{Type: domain.PeerTypeUser, ID: u.ID})
		}
	}
	for _, item := range chats {
		if ch, ok := item.(*tg.Channel); ok && ch != nil {
			addPeer(domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID})
		}
	}
	return peers
}

// applyUsernamesFromRegistry applies a previously loaded registry snapshot.
// Notification fan-out uses this form so one peer-wide read does not become one
// database query per online viewer.
func applyUsernamesFromRegistry(users []tg.UserClass, chats []tg.ChatClass, byPeer map[domain.Peer][]domain.Username) {
	if len(byPeer) == 0 {
		return
	}
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil {
			continue
		}
		list, ok := byPeer[domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}]
		if !ok || !hasCollectibleUsername(list) {
			continue
		}
		if vector := tgUsernamesFromRegistry(list, u.Username); len(vector) > 0 {
			// Official clients treat the legacy scalar and the complete vector as
			// alternative representations. TDLib rejects a User carrying both and
			// discards the complete username set, while TDesktop and DrKLO derive
			// the primary username from the first active vector entry.
			u.Flags.Unset(3)
			u.Username = ""
			u.SetUsernames(vector)
		}
	}
	for _, item := range chats {
		ch, ok := item.(*tg.Channel)
		if !ok || ch == nil {
			continue
		}
		list, ok := byPeer[domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}]
		if !ok || !hasCollectibleUsername(list) {
			continue
		}
		// ch.Username is the flagged scalar; GetUsername reports the empty string
		// when unset, which is exactly the fallback tgUsernamesFromRegistry wants.
		scalar, _ := ch.GetUsername()
		if vector := tgUsernamesFromRegistry(list, scalar); len(vector) > 0 {
			ch.Flags.Unset(6)
			ch.Username = ""
			ch.SetUsernames(vector)
		}
	}
}

// usernameRegistryMap loads the registry for the given peers. A single peer goes
// through PeerUsernames so a one-object projection does not pay for a batch
// round trip; anything larger goes through UsernamesBatch (no N+1). Any error
// yields an empty map, which the caller treats as "keep the legacy scalar".
//
// Раунд 49 (анти-мерцание): каждый выполненный реестр-челлендж пишет вердикт в
// короткий TTL-кэш usernameNoCollectible (см. applyPushPeerOverlays): «коллек-
// ционных строк нет» — пишется, «есть» — запись сбрасывается. Кэш ЧИТАЕТ только
// пуш-оверлей на границе доставки; ответная граница всегда читает реестр
// напрямую, поэтому свежекупленный коллекционный юзернейм в ответах не может
// «застрять» в устаревшем вердикте.
func (r *Router) usernameRegistryMap(ctx context.Context, peers []domain.Peer) map[domain.Peer][]domain.Username {
	if r.deps.Usernames == nil || len(peers) == 0 {
		return nil
	}
	if len(peers) == 1 {
		list, err := r.deps.Usernames.PeerUsernames(ctx, peers[0])
		if err != nil || len(list) == 0 {
			if err == nil {
				r.usernameNegativesPrime(peers)
			}
			return nil
		}
		if hasCollectibleUsername(list) {
			r.usernameNegativesDrop(peers)
		} else {
			r.usernameNegativesPrime(peers)
		}
		return map[domain.Peer][]domain.Username{peers[0]: list}
	}
	byPeer, err := r.deps.Usernames.UsernamesBatch(ctx, peers)
	if err != nil {
		return nil
	}
	prime := make([]domain.Peer, 0, len(peers))
	drop := make([]domain.Peer, 0, len(peers))
	for _, peer := range peers {
		if hasCollectibleUsername(byPeer[peer]) {
			drop = append(drop, peer)
		} else {
			prime = append(prime, peer)
		}
	}
	r.usernameNegativesPrime(prime)
	r.usernameNegativesDrop(drop)
	return byPeer
}

// usernameNegativesTTL живёт несколько секунд — окно покрывает серию пушей
// одного события (fan-out / notify-волна). Изменение реестра всегда
// сопровождается push-уведомлением (которое сбрасывает запись) и инвалидацией
// проекций, поэтому окно устаревания вердикта не наблюдаемо в UX: пуш-оверлей
// может пропустить перечитывание максимум до ближайшего ответного RPC.
const usernameNegativesTTL = 5 * time.Second

// usernameNegativesLimit — ёмкость отрицательного кэша; переполнение
// представляет собой полный сброс (профиль нагрузки — пуш-шторм, где дешевле
// перечитать, чем выгрезать LRU).
const usernameNegativesLimit = 8192

// usernameNegativesPrime запоминает вердикт «коллекционных строк нет» до TTL.
func (r *Router) usernameNegativesPrime(peers []domain.Peer) {
	if len(peers) == 0 {
		return
	}
	r.usernameNoCollectibleMu.Lock()
	defer r.usernameNoCollectibleMu.Unlock()
	if r.usernameNoCollectible == nil {
		r.usernameNoCollectible = make(map[domain.Peer]int64, len(peers))
	}
	if len(r.usernameNoCollectible)+len(peers) > usernameNegativesLimit {
		r.usernameNoCollectible = make(map[domain.Peer]int64, len(peers))
	}
	until := int64(r.clock.Now().Add(usernameNegativesTTL).Unix())
	for _, peer := range peers {
		r.usernameNoCollectible[peer] = until
	}
}

// usernameNegativesDrop сбрасывает вердикт (коммит реестра с коллекционными
// строками или прямое уведомление об изменении).
func (r *Router) usernameNegativesDrop(peers []domain.Peer) {
	if len(peers) == 0 || r.usernameNoCollectible == nil {
		return
	}
	r.usernameNoCollectibleMu.Lock()
	defer r.usernameNoCollectibleMu.Unlock()
	for _, peer := range peers {
		delete(r.usernameNoCollectible, peer)
	}
}

// usernameNegativesPending возвращает пиры, чей вердикт устарел/отсутствует и
// требует чтения реестра. Читает ТОЛЬКО пуш-оверлей на границе доставки.
func (r *Router) usernameNegativesPending(peers []domain.Peer) []domain.Peer {
	if len(peers) == 0 || r.usernameNoCollectible == nil {
		return peers
	}
	freshUntil := int64(r.clock.Now().Add(usernameNegativesTTL).Unix())
	r.usernameNoCollectibleMu.Lock()
	defer r.usernameNoCollectibleMu.Unlock()
	out := make([]domain.Peer, 0, len(peers))
	for _, peer := range peers {
		if until, ok := r.usernameNoCollectible[peer]; ok && until >= freshUntil {
			continue
		}
		out = append(out, peer)
	}
	return out
}
