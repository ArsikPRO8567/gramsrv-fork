-- Откат 0187_stars_transaction_public_ids: колонки и индексы только добавлены,
-- поэтому удаление безопасно. Разметка исторических данных сохраняется:
--   - transaction_id = id::text: старый сервер снова отдаёт десятичные id,
--     клиенты продолжают разрешать запомненные идентификаторы;
--   - переименования reason ('gift_convert' у конвертаций) не откатываются
--     сознательно: у строки остаётся исходный английский заголовок
--     ("Star gift conversion"), по которому старый сервер рендерит транзакцию
--     как прежде; строки, созданные новым кодом без заголовка, при откате
--     терять разметку не должны.
--   - флаг refunded просто исчезает вместе с колонкой (старый сервер вёл
--     возвраты только по заголовкам).

DROP INDEX IF EXISTS public.ton_transactions_gift_id_idx;
DROP INDEX IF EXISTS public.stars_transactions_gift_id_idx;
DROP INDEX IF EXISTS public.channel_ton_transactions_transaction_id_uidx;
DROP INDEX IF EXISTS public.channel_stars_transactions_transaction_id_uidx;
DROP INDEX IF EXISTS public.ton_transactions_transaction_id_uidx;
DROP INDEX IF EXISTS public.stars_transactions_transaction_id_uidx;

ALTER TABLE public.channel_ton_transactions
    DROP COLUMN IF EXISTS transaction_id,
    DROP COLUMN IF EXISTS refunded,
    DROP COLUMN IF EXISTS gift_is_unique;

ALTER TABLE public.channel_stars_transactions
    DROP COLUMN IF EXISTS transaction_id,
    DROP COLUMN IF EXISTS refunded,
    DROP COLUMN IF EXISTS gift_is_unique;

ALTER TABLE public.ton_transactions
    DROP COLUMN IF EXISTS transaction_id,
    DROP COLUMN IF EXISTS refunded,
    DROP COLUMN IF EXISTS gift_is_unique;

ALTER TABLE public.stars_transactions
    DROP COLUMN IF EXISTS transaction_id,
    DROP COLUMN IF EXISTS refunded,
    DROP COLUMN IF EXISTS gift_id,
    DROP COLUMN IF EXISTS gift_is_unique;
