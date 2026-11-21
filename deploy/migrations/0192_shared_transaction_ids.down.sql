-- Откат раунда 37: восстановление глобальных уникальных индексов раунда 30.
-- Внимание: при наличии строк с общим transaction_id (переводы раунда 37)
-- пересоздание глобальных индексов потребует предварительной очистки дублей.

DROP INDEX IF EXISTS public.stars_transactions_owner_transaction_uidx;
DROP INDEX IF EXISTS public.ton_transactions_owner_transaction_uidx;
DROP INDEX IF EXISTS public.channel_stars_transactions_owner_transaction_uidx;
DROP INDEX IF EXISTS public.channel_ton_transactions_owner_transaction_uidx;

CREATE UNIQUE INDEX IF NOT EXISTS stars_transactions_transaction_id_uidx
    ON public.stars_transactions(transaction_id) WHERE transaction_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS ton_transactions_transaction_id_uidx
    ON public.ton_transactions(transaction_id) WHERE transaction_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS channel_stars_transactions_transaction_id_uidx
    ON public.channel_stars_transactions(transaction_id) WHERE transaction_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS channel_ton_transactions_transaction_id_uidx
    ON public.channel_ton_transactions(transaction_id) WHERE transaction_id <> '';
