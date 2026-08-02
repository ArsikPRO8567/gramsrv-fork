package rpc

import (
	"context"
	"fmt"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// tgSelfUserWithReadModels is the single-object response-boundary projection
// used by authorization results and self-profile updates. tgSelfUser itself is
// intentionally a pure domain -> TL conversion because many list paths call it
// in loops; the read-model pass belongs here, where it stays one query per
// response.
func (r *Router) tgSelfUserWithReadModels(ctx context.Context, u domain.User) *tg.User {
	self := r.tgSelfUser(u)
	users := []tg.UserClass{self}
	r.applyPeerReadModels(ctx, u.ID, users, nil)
	return self
}

// pushUpdatesReadySelfProfile repairs the current session's cached self user
// when it first becomes eligible for proactive updates. Telegram's updateUser
// contract requires the complete User to be carried by the outer updates.users
// vector; TDLib applies that vector before invalidating userFull for updateUser.
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
	// These read models are optional projections. Their existing response-boundary
	// contract degrades independently; the username registry above is handled
	// strictly because losing that vector is the cache corruption fixed here.
	r.applyStoryMaxIDsToPeerObjects(ctx, userID, users, nil)
	r.applyBotVerificationIconsToPeerObjects(ctx, users, nil)

	usernames := tgUsernames(u.Username)
	if vector, ok := self.GetUsernames(); ok && len(vector) != 0 {
		usernames = vector
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{
			// updateUserName is the authoritative basic-name/username cache write.
			// TDLib handles it by calling on_update_user_usernames directly.
			&tg.UpdateUserName{
				UserID:    userID,
				FirstName: u.FirstName,
				LastName:  u.LastName,
				Usernames: usernames,
			},
			// updateUser additionally invalidates userFull while the complete basic
			// User remains bundled in the outer users vector.
			&tg.UpdateUser{UserID: userID},
		},
		Users: users,
		Date:  int(r.clock.Now().Unix()),
		Seq:   0,
	}, nil
}
