-- Раунд 37: платные сообщения в личных чатах (paid messages).
--
-- Официальный клиент позволяет пользователю назначить цену (в Stars) за
-- отправку ему сообщения от не-контакта (настройка "Non-Contact Peers" +
-- PrivacyKeyNoPaidMessages). Отправитель платит Stars, получатель получает
-- их начисление, а само сообщение помечается paid_message_stars, чтобы обе
-- стороны видели звёздный бейдж сообщения.
--
-- Ранее сервер валидировал лимит (allow_paid_stars >= цена получателя), но
-- отказывал PAYMENT_UNSUPPORTED — списание не было реализовано. Эта миграция
-- добавляет колонку фактической платы в приватные сообщения: приватная
-- таблица хранит авторитетное значение, boxes — денормализованную копию для
-- проекции без JOIN (body/media/effect уже копируются в boxes).
--
-- Монета не конвертируется: значение совпадает на обеих сторонах диалога и
-- всегда >= 0. Транзакции списания/зачисления пишутся в stars_transactions с
-- одинаковым публичным transaction_id (см. раунд 30/37).

ALTER TABLE public.private_messages
    ADD COLUMN paid_message_stars bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT private_messages_paid_message_stars_check CHECK (paid_message_stars >= 0);

ALTER TABLE public.message_boxes
    ADD COLUMN paid_message_stars bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT message_boxes_paid_message_stars_check CHECK (paid_message_stars >= 0);
