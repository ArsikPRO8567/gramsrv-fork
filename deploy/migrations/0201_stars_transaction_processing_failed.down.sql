-- Откат 0201: снять статусные флаги «В обработке»/«Ошибка».

ALTER TABLE public.stars_transactions
    DROP COLUMN IF EXISTS processing,
    DROP COLUMN IF EXISTS failed;

ALTER TABLE public.ton_transactions
    DROP COLUMN IF EXISTS processing,
    DROP COLUMN IF EXISTS failed;

ALTER TABLE public.channel_stars_transactions
    DROP COLUMN IF EXISTS processing,
    DROP COLUMN IF EXISTS failed;

ALTER TABLE public.channel_ton_transactions
    DROP COLUMN IF EXISTS processing,
    DROP COLUMN IF EXISTS failed;
