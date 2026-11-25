package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestUserStoreUpdateColorRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewUserStore()
	user, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550003301", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	updated, err := store.UpdateColor(ctx, user.ID, false, domain.PeerColor{
		HasColor:          true,
		Color:             0,
		BackgroundEmojiID: 100,
	})
	if err != nil {
		t.Fatalf("update message color: %v", err)
	}
	if !updated.Color.HasColor || updated.Color.Color != 0 || updated.Color.BackgroundEmojiID != 100 {
		t.Fatalf("message color = %+v, want explicit color=0 bg=100", updated.Color)
	}
	if !updated.ProfileColor.Empty() {
		t.Fatalf("profile color = %+v, want unchanged empty", updated.ProfileColor)
	}

	updated, err = store.UpdateColor(ctx, user.ID, true, domain.PeerColor{
		HasColor:          true,
		Color:             3,
		BackgroundEmojiID: 200,
	})
	if err != nil {
		t.Fatalf("update profile color: %v", err)
	}
	if updated.Color.Color != 0 || updated.Color.BackgroundEmojiID != 100 {
		t.Fatalf("message color after profile update = %+v, want unchanged", updated.Color)
	}
	if !updated.ProfileColor.HasColor || updated.ProfileColor.Color != 3 || updated.ProfileColor.BackgroundEmojiID != 200 {
		t.Fatalf("profile color = %+v, want color=3 bg=200", updated.ProfileColor)
	}

	updated, err = store.UpdateColor(ctx, user.ID, true, domain.PeerColor{})
	if err != nil {
		t.Fatalf("clear profile color: %v", err)
	}
	if !updated.ProfileColor.Empty() {
		t.Fatalf("profile color after clear = %+v, want empty", updated.ProfileColor)
	}
}

// Раунд 45: флаг service_account в store.UserStore — служебные аккаунты и
// боты (встроенные ряды) не получают подарки, но сами дарят свободно. Флаг не
// связан с support; неизвестный id = ErrUserNotFound.
func TestUserStoreServiceAccountFlag(t *testing.T) {
	ctx := context.Background()
	store := NewUserStore()
	user, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550003391", FirstName: "Service"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	// Встроенные системные ряды — service accounts по умолчанию.
	if service, err := store.ServiceAccount(ctx, domain.OfficialSystemUserID); err != nil || !service {
		t.Fatalf("system account service flag = %v/%v, want true/nil", service, err)
	}
	if service, _ := store.ServiceAccount(ctx, user.ID); service {
		t.Fatalf("fresh user service flag = true, want false")
	}
	if err := store.SetServiceAccount(ctx, user.ID, true); err != nil {
		t.Fatalf("set service flag: %v", err)
	}
	if service, _ := store.ServiceAccount(ctx, user.ID); !service {
		t.Fatalf("service flag after set = false, want true")
	}
	if err := store.SetServiceAccount(ctx, user.ID, false); err != nil {
		t.Fatalf("clear service flag: %v", err)
	}
	if service, _ := store.ServiceAccount(ctx, user.ID); service {
		t.Fatalf("service flag after clear = true, want false")
	}
	if err := store.SetServiceAccount(ctx, user.ID+999, true); !domainErrIs(err, domain.ErrUserNotFound) {
		t.Fatalf("unknown id error = %v, want ErrUserNotFound", err)
	}
	// Правило знакомства в памяти fail-open (см. HasPrivateChatEver).
	if chatted, err := store.HasPrivateChatEver(ctx, user.ID, user.ID+1); err != nil || !chatted {
		t.Fatalf("memory HasPrivateChatEver = %v/%v, want permissive true", chatted, err)
	}
}

func domainErrIs(err, target error) bool {
	if err == nil {
		return false
	}
	return err.Error() == target.Error()
}
