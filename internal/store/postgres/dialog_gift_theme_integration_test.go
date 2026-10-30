package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestDialogStoreGiftThemeMoveAcrossChats проверяет официальный контракт
// «тема коллекционного подарка живёт ровно в одном чате» на уровне
// postgres-хранилища: SetChatThemeGift ставит тему в обе стороны диалога,
// ClearGiftThemeElsewhere молча снимает её со всех прочих диалогов (включая
// накопленные прежние двойные установки), сохраняя целевую пару.
func TestDialogStoreGiftThemeMoveAcrossChats(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 36,
		Phone:      "+1778" + suffix + "01",
		FirstName:  "ThemeOwner" + suffix,
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	first, err := users.Create(ctx, domain.User{
		AccessHash: 37,
		Phone:      "+1778" + suffix + "02",
		FirstName:  "ThemeFirst" + suffix,
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := users.Create(ctx, domain.User{
		AccessHash: 38,
		Phone:      "+1778" + suffix + "03",
		FirstName:  "ThemeSecond" + suffix,
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, first.ID, second.ID})
	})

	dialogs := NewDialogStore(pool)
	slug := "crystalball-" + suffix
	peerOf := func(u domain.User) domain.Peer {
		return domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}
	}

	// Установка в первый чат (оба направления).
	if _, err := dialogs.SetChatThemeGift(ctx, owner.ID, peerOf(first), slug); err != nil {
		t.Fatalf("set gift theme owner->first: %v", err)
	}
	if _, err := dialogs.SetChatThemeGift(ctx, first.ID, peerOf(owner), slug); err != nil {
		t.Fatalf("set gift theme first->owner: %v", err)
	}
	assertDialogGiftSlug := func(ownerUserID, peerUserID int64, want string) {
		t.Helper()
		list, err := dialogs.ListByPeers(ctx, ownerUserID, []domain.Peer{{Type: domain.PeerTypeUser, ID: peerUserID}})
		if err != nil {
			t.Fatalf("list dialogs %d/%d: %v", ownerUserID, peerUserID, err)
		}
		if len(list.Dialogs) != 1 {
			t.Fatalf("dialogs %d/%d = %d rows, want 1", ownerUserID, peerUserID, len(list.Dialogs))
		}
		if got := list.Dialogs[0].ThemeGiftSlug; got != want {
			t.Fatalf("dialog %d/%d gift slug = %q, want %q", ownerUserID, peerUserID, got, want)
		}
	}
	assertDialogGiftSlug(owner.ID, first.ID, slug)
	assertDialogGiftSlug(first.ID, owner.ID, slug)

	// Перенос во второй чат: старый чат очищается молча в обе стороны.
	affected, err := dialogs.ClearGiftThemeElsewhere(ctx, slug, owner.ID, second.ID)
	if err != nil {
		t.Fatalf("clear gift theme elsewhere: %v", err)
	}
	// Возврат отсортирован по id — сравним безотносительно порядка создания.
	if len(affected) != 2 {
		t.Fatalf("affected owners = %v, want both old-chat sides", affected)
	}
	wantSet := map[int64]bool{first.ID: true, owner.ID: true}
	for _, id := range affected {
		if !wantSet[id] {
			t.Fatalf("affected owners = %v, contains unexpected id %d", affected, id)
		}
		delete(wantSet, id)
	}
	if len(wantSet) != 0 {
		t.Fatalf("affected owners = %v, missing ids %v", affected, wantSet)
	}
	assertDialogGiftSlug(owner.ID, first.ID, "")
	assertDialogGiftSlug(first.ID, owner.ID, "")

	// Целевая пара получает тему; повторный перенос чистит и накопленный
	// legacy-мусор в стороннем диалоге (двойная установка из старых багов).
	if _, err := dialogs.SetChatThemeGift(ctx, owner.ID, peerOf(second), slug); err != nil {
		t.Fatalf("set gift theme owner->second: %v", err)
	}
	if _, err := dialogs.SetChatThemeGift(ctx, second.ID, peerOf(owner), slug); err != nil {
		t.Fatalf("set gift theme second->owner: %v", err)
	}
	legacy, err := users.Create(ctx, domain.User{
		AccessHash: 39,
		Phone:      "+1778" + suffix + "04",
		FirstName:  "ThemeLegacy" + suffix,
	})
	if err != nil {
		t.Fatalf("create legacy: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", legacy.ID) })
	if _, err := dialogs.SetChatThemeGift(ctx, legacy.ID, peerOf(owner), slug); err != nil {
		t.Fatalf("seed legacy dialog: %v", err)
	}

	affected, err = dialogs.ClearGiftThemeElsewhere(ctx, slug, owner.ID, second.ID)
	if err != nil {
		t.Fatalf("clear gift theme elsewhere (second pass): %v", err)
	}
	if len(affected) != 1 || affected[0] != legacy.ID {
		t.Fatalf("affected owners (second pass) = %v, want [legacy]=[%d]", affected, legacy.ID)
	}
	assertDialogGiftSlug(owner.ID, second.ID, slug)
	assertDialogGiftSlug(second.ID, owner.ID, slug)
	assertDialogGiftSlug(legacy.ID, owner.ID, "")
}
