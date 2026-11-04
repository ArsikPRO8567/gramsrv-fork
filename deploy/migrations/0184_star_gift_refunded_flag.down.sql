-- Откат 0184_star_gift_refunded_flag: столбец только создан (значения всегда
-- false), данных под флаг нет — удаление безопасно.

ALTER TABLE public.peer_star_gifts
    DROP COLUMN IF EXISTS refunded;
