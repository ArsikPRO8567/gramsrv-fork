package postgres

import (
	"context"
	"fmt"
	"math"
)

// PrivateDialogInitiatedBy (раунд 41) — проба инициатора приватного диалога
// пары (a,b): true, если самое раннее сообщение пары (по монотонному id) отправил
// a, либо диалога ещё нет — тогда a инициирует его первым сообщением.
//
// Протокол paid-messages: плата за сообщение берётся только с участника,
// который начал диалог с получателем платы первым, независимо от того, была ли
// настройка включена до или после начала диалога. Ответ получателя платы
// платность не меняет — плата сохраняется до явного «Не брать плату»
// (account.toggleNoPaidMessagesException). Индекс 0197 делает обе пробы
// (в обе стороны) дешёвыми по (sender_user_id, recipient_user_id, id).
func (s *MessageStore) PrivateDialogInitiatedBy(ctx context.Context, aUserID, bUserID int64) (bool, error) {
	if s == nil || s.db == nil || aUserID <= 0 || bUserID <= 0 || aUserID == bUserID {
		return true, nil
	}
	var aOut, bOut int64
	err := s.db.QueryRow(ctx, `
SELECT
  COALESCE((SELECT id FROM private_messages
            WHERE sender_user_id = $1 AND recipient_user_id = $2
            ORDER BY id LIMIT 1), $3),
  COALESCE((SELECT id FROM private_messages
            WHERE sender_user_id = $2 AND recipient_user_id = $1
            ORDER BY id LIMIT 1), $3)`,
		aUserID, bUserID, int64(math.MaxInt64)).Scan(&aOut, &bOut)
	if err != nil {
		return true, fmt.Errorf("probe private dialog initiator: %w", err)
	}
	return aOut <= bOut, nil
}
