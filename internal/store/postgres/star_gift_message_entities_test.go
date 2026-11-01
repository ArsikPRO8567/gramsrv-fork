package postgres

import (
	"encoding/json"
	"testing"

	"telesrv/internal/domain"
)

// encode/decodeStarGiftMessageEntities — jsonb-хранение entity-разметки附言
// к подарку (премиум-эмодзи) в peer_star_gifts.message_entities.
func TestStarGiftMessageEntitiesRoundTrip(t *testing.T) {
	entities := []domain.MessageEntity{
		{Type: domain.MessageEntityCustomEmoji, Offset: 0, Length: 2, DocumentID: 5424873531156463617},
		{Type: domain.MessageEntityBold, Offset: 3, Length: 5},
	}
	encoded := string(encodeStarGiftMessageEntities(entities))
	decoded := decodeStarGiftMessageEntities(encoded)
	if len(decoded) != len(entities) {
		t.Fatalf("decoded = %+v, want %d entities", decoded, len(entities))
	}
	for i := range entities {
		if decoded[i] != entities[i] {
			t.Fatalf("decoded[%d] = %+v, want %+v", i, decoded[i], entities[i])
		}
	}
	// Внутреннее json-представление стабильно: каждое поле присутствует,
	// document_id сохраняет точное int64-значение.
	var raw []map[string]any
	if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw[0]["DocumentID"] != float64(5424873531156463617) {
		t.Fatalf("DocumentID field missing: %v", raw[0])
	}
}

func TestStarGiftMessageEntitiesEmpty(t *testing.T) {
	if got := string(encodeStarGiftMessageEntities(nil)); got != "[]" {
		t.Fatalf("encode(nil) = %q", got)
	}
	if got := decodeStarGiftMessageEntities("[]"); got != nil {
		t.Fatalf("decode([]) = %+v", got)
	}
	if got := decodeStarGiftMessageEntities(""); got != nil {
		t.Fatalf("decode(\"\") = %+v", got)
	}
	// Битый JSON не должен ронять проекцию.
	if got := decodeStarGiftMessageEntities("{broken"); got != nil {
		t.Fatalf("decode(broken) = %+v", got)
	}
}
