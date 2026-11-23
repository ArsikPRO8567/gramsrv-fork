-- Раунд 41: протокол paid-messages определяет платность сообщения тем, кто
-- начал диалог с получателем платы первым (core.telegram.org/api/paid-messages:
-- «users the recipient has messaged first» бесплатны). Проба инициатора
-- (MessageStore.PrivateDialogInitiatedBy) читает самое раннее сообщение пары в
-- ОБОИХ направлениях: без индекса каждый приватный гейт отправки сканировал бы
-- таблицу private_messages дважды.

CREATE INDEX IF NOT EXISTS private_messages_pair_direction_idx
    ON public.private_messages USING btree (sender_user_id, recipient_user_id, id);
