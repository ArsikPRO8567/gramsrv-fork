package rpc

import (
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// clampGiftMessageEntities сохраняет entity-разметку附言 к подарку
// (премиум-эмодзи), отбрасывая только разметку, выходящую за обрезанный текст.
func TestClampGiftMessageEntities(t *testing.T) {
	customEmoji := &tg.MessageEntityCustomEmoji{Offset: 0, Length: 2, DocumentID: 7777}

	t.Run("entities проходят насквозь", func(t *testing.T) {
		text, ents := clampGiftMessageEntities("\U0001F600\U0001F600 hi", []tg.MessageEntityClass{customEmoji})
		if text != "\U0001F600\U0001F600 hi" {
			t.Fatalf("text = %q", text)
		}
		if len(ents) != 1 || ents[0].Type != domain.MessageEntityCustomEmoji || ents[0].DocumentID != 7777 {
			t.Fatalf("ents = %+v", ents)
		}
	})

	t.Run("длинный текст обрезается, entity за границей отбрасываются", func(t *testing.T) {
		long := strings.Repeat("a", domain.MaxStarGiftMessageRunes+50)
		bold := &tg.MessageEntityBold{Offset: 0, Length: 5}
		outOfBounds := &tg.MessageEntityBold{Offset: domain.MaxStarGiftMessageRunes, Length: 10}
		text, ents := clampGiftMessageEntities(long, []tg.MessageEntityClass{bold, outOfBounds})
		if runes := len([]rune(text)); runes != domain.MaxStarGiftMessageRunes {
			t.Fatalf("runes = %d, want %d", runes, domain.MaxStarGiftMessageRunes)
		}
		if len(ents) != 1 || ents[0].Type != domain.MessageEntityBold {
			t.Fatalf("ents = %+v, want only in-bounds bold", ents)
		}
	})

	t.Run("UTF-16 границы: суррогатная пара считается двумя единицами", func(t *testing.T) {
		// "😀" = 1 руна, 2 UTF-16 единицы; entity длиной 2 валидно.
		text, ents := clampGiftMessageEntities("\U0001F600", []tg.MessageEntityClass{customEmoji})
		if text != "\U0001F600" {
			t.Fatalf("text = %q", text)
		}
		if len(ents) != 1 {
			t.Fatalf("entity длиной 2 UTF-16 единицы должен выжить, ents = %+v", ents)
		}
		// Entity длиной 3 за границей (текст всего 2 единицы) — отбрасывается.
		_, ents = clampGiftMessageEntities("\U0001F600", []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 3},
		})
		if len(ents) != 0 {
			t.Fatalf("entity за границей UTF-16 должен быть отброшен, ents = %+v", ents)
		}
	})

	t.Run("нет entities", func(t *testing.T) {
		text, ents := clampGiftMessageEntities("просто текст", nil)
		if text != "просто текст" || ents != nil {
			t.Fatalf("text=%q ents=%v", text, ents)
		}
	})
}
