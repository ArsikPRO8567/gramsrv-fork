package rpc

import (
	"context"
	"fmt"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// tgSelfUserWithUsernames is the narrow single-object projection used by
// authorization results and self-profile updates. Those constructors already
// carry a User, so it must include the complete username registry vector and
// never downgrade a client's collectible usernames to the legacy scalar. Story
// and bot-verification read models are unrelated and deliberately not queried.
func (r *Router) tgSelfUserWithUsernames(ctx context.Context, u domain.User) *tg.User {
	self := r.tgSelfUser(u)
	users := []tg.UserClass{self}
	r.applyUsernamesToPeerObjects(ctx, users, nil)
	return self
}

// pushUpdatesReadySelfProfile repairs the current session's cached self user
// when it first becomes eligible for proactive updates. updateUserName is the
// authoritative basic-name/username cache write in TDLib and DrKLO. A companion
// username-complete User remains in updates.users so DrKLO's normal peer merge
// persists the vector, but no updateUser is sent because it only invalidates
// userFull (and makes DrKLO clear photo caches).
//
// This is an ephemeral cache-convergence update: it allocates no PTS, writes no
// durable update event and targets only the physical session that just became
// ready. A username-registry read failure suppresses the refresh instead of
// replacing a possibly richer client cache with the legacy scalar-only shape.
func (r *Router) pushUpdatesReadySelfProfile(ctx context.Context, userID int64) {
	updates, err := r.updatesReadySelfProfile(ctx, userID)
	if err != nil {
		r.log.Warn("build updates-ready self profile",
			zap.Int64("user_id", userID),
			zap.Error(err))
		return
	}
	if updates == nil {
		return
	}
	r.pushCurrentSessionMessage(ctx, "push updates-ready self profile", updates)
}

func (r *Router) updatesReadySelfProfile(ctx context.Context, userID int64) (*tg.Updates, error) {
	if userID == 0 || r.deps.Users == nil {
		return nil, nil
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load self user: %w", err)
	}
	if u.ID != userID || u.Deleted {
		return nil, fmt.Errorf("invalid self user: requested %d, got %d deleted=%v", userID, u.ID, u.Deleted)
	}

	self := r.tgSelfUser(u)
	users := []tg.UserClass{self}
	if r.deps.Usernames != nil {
		peer := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
		list, err := r.deps.Usernames.PeerUsernames(ctx, peer)
		if err != nil {
			return nil, fmt.Errorf("load self usernames: %w", err)
		}
		if len(list) != 0 {
			applyUsernamesFromRegistry(users, nil, map[domain.Peer][]domain.Username{peer: list})
		}
	}
	usernames := tgUsernames(u.Username)
	if vector, ok := self.GetUsernames(); ok && len(vector) != 0 {
		usernames = vector
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUserName{
			UserID:    userID,
			FirstName: u.FirstName,
			LastName:  u.LastName,
			Usernames: usernames,
		}},
		Users: users,
		Date:  int(r.clock.Now().Unix()),
		Seq:   0,
	}, nil
}
