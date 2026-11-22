DROP INDEX IF EXISTS public.stars_transactions_transaction_id_uidx;
CREATE UNIQUE INDEX IF NOT EXISTS stars_transactions_transaction_id_uidx
    ON public.stars_transactions(transaction_id) WHERE transaction_id <> '';

DROP INDEX IF EXISTS public.ton_transactions_transaction_id_uidx;
CREATE UNIQUE INDEX IF NOT EXISTS ton_transactions_transaction_id_uidx
    ON public.ton_transactions(transaction_id) WHERE transaction_id <> '';
