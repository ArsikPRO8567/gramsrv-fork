-- Откат 0188: столбец цены выигрышной ставки не нёс уникальных данных
-- (0 для всех не-аукционных экземпляров), безопасно удалить вместе с констрейнтом.
ALTER TABLE public.peer_star_gifts DROP CONSTRAINT IF EXISTS peer_star_gifts_paid_stars_check;
ALTER TABLE public.peer_star_gifts DROP COLUMN IF EXISTS paid_stars;
