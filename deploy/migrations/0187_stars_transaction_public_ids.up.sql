-- Раунд 30: публичные идентификаторы транзакций и флаг возврата средств.
--
-- Официальный сервер выдаёт клиентам сложные идентификаторы вида
-- "stars_<60 букв и цифр>" (аналогично "ton_..."), а клиенты используют их в
-- payments.getStarsTransactionsByID и в бот-API. До этого раунда сервер
-- отдавал в качестве id десятичную строку identity-колонки. Новые транзакции
-- получают сгенерированный публичный id; историческим строкам он переносится
-- как есть (id::text) — клиенты, уже запомнившие старые id, продолжают их
-- разрешать. Флаг refunded независим от направления транзакции: возврат
-- бывает и у списаний (отмена оплаты), и у зачислений (отзыв начисления).
--
-- gift_id/gift_is_unique связывают транзакцию с подарком (обычным или
-- уникальным): клиенты по stargift в starsTransaction рисуют анимацию подарка
-- или предпросмотр коллекционного вместо текстовой строки. Для
-- stars_transactions колонка добавляется (её не было); в TON/канальных
-- таблицах gift_id уже существовал, добавляется только признак уникальности.

ALTER TABLE public.stars_transactions
    ADD COLUMN IF NOT EXISTS transaction_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS refunded boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS gift_id bigint,
    ADD COLUMN IF NOT EXISTS gift_is_unique boolean NOT NULL DEFAULT false;

ALTER TABLE public.ton_transactions
    ADD COLUMN IF NOT EXISTS transaction_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS refunded boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS gift_is_unique boolean NOT NULL DEFAULT false;

ALTER TABLE public.channel_stars_transactions
    ADD COLUMN IF NOT EXISTS transaction_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS refunded boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS gift_is_unique boolean NOT NULL DEFAULT false;

ALTER TABLE public.channel_ton_transactions
    ADD COLUMN IF NOT EXISTS transaction_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS refunded boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS gift_is_unique boolean NOT NULL DEFAULT false;

-- Совместимость с клиентами: исторический публичный id — десятичная строка
-- identity-колонки. Переносим её в transaction_id без изменений.
UPDATE public.stars_transactions SET transaction_id = id::text WHERE transaction_id = '';
UPDATE public.ton_transactions SET transaction_id = id::text WHERE transaction_id = '';
UPDATE public.channel_stars_transactions SET transaction_id = id::text WHERE transaction_id = '';
UPDATE public.channel_ton_transactions SET transaction_id = id::text WHERE transaction_id = '';

-- Исторические возвраты размечены только английскими заголовками (флага не
-- существовало). Размечаем refunded по этим маркерам, чтобы старые строки
-- клиент показывал как возврат средств.
UPDATE public.stars_transactions SET refunded = true
WHERE NOT refunded AND (
    (reason = 'reaction' AND title = 'Paid reaction refund') OR
    (reason = 'gift' AND title = 'Star gift refund') OR
    (reason = 'gift_offer' AND title LIKE '%offer refund%') OR
    (reason = 'gift_offer_refund_account_deleted') OR
    (reason = 'gift_auction' AND title LIKE '%refund%') OR
    (reason = 'premium' AND title = 'Telegram Premium refund') OR
    (reason = 'suggested_post' AND title = 'Suggested post refund'));

-- ton_transactions не имеет колонки title: зачисление по suggested_post всегда
-- является возвратом плательщику (дебет — оплата, канал получает свой процент в
-- channel_ton_transactions).
UPDATE public.ton_transactions SET refunded = true
WHERE NOT refunded AND amount_nanoton > 0 AND (
    reason = 'gift_offer' OR reason = 'gift_offer_refund_account_deleted' OR
    reason = 'suggested_post');

-- Конвертация подарка в звёзды — продажа, а не получение подарка: клиенты по
-- reason='gift' с зачислением рисуют подпись «подарок от пользователя», для
-- конвертации выделен отдельный reason 'gift_convert' (stargift без флага
-- gift). Исторические строки конвертации переименовываются по их маркеру.
UPDATE public.stars_transactions SET reason = 'gift_convert'
WHERE reason = 'gift' AND title = 'Star gift conversion';

UPDATE public.ton_transactions SET reason = 'gift_convert'
WHERE reason = 'gift' AND title = 'Star gift conversion';

CREATE UNIQUE INDEX IF NOT EXISTS stars_transactions_transaction_id_uidx
    ON public.stars_transactions(transaction_id) WHERE transaction_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS ton_transactions_transaction_id_uidx
    ON public.ton_transactions(transaction_id) WHERE transaction_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS channel_stars_transactions_transaction_id_uidx
    ON public.channel_stars_transactions(transaction_id) WHERE transaction_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS channel_ton_transactions_transaction_id_uidx
    ON public.channel_ton_transactions(transaction_id) WHERE transaction_id <> '';

CREATE INDEX IF NOT EXISTS stars_transactions_gift_id_idx
    ON public.stars_transactions(gift_id) WHERE gift_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ton_transactions_gift_id_idx
    ON public.ton_transactions(gift_id) WHERE gift_id IS NOT NULL;
