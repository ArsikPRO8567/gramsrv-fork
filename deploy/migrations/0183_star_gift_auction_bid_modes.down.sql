-- Даун-миграция раунда 21. raise_min_bid/round_extends/round_extend_seconds
-- удаляются; строки star_gift_auctions сохраняются. На исторических базах,
-- где столбцы уже удалялись вручную, миграция — no-op.

ALTER TABLE public.star_gift_auctions
    DROP COLUMN IF EXISTS raise_min_bid,
    DROP COLUMN IF EXISTS round_extends,
    DROP COLUMN IF EXISTS round_extend_seconds;

-- Возврат прежней границы цены улучшения (исторические нулевые цены, если
-- появились, блокируют даун-миграцию — осознанная защита данных).
ALTER TABLE public.star_gift_collectible_revisions
    DROP CONSTRAINT IF EXISTS star_gift_collectible_price_check,
    ADD CONSTRAINT star_gift_collectible_price_check CHECK (upgrade_stars > 0);
