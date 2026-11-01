-- Даун-миграция раунда 20. disallow_upgrades/disallow_resell/apply_whitelist и
-- round_durations удаляются; сами строки gift_whitelists и star_gift_auctions
-- сохраняются (административные данные вайтлистов терять при откате нельзя).
-- На исторических базах, где столбцы уже удалялись вручную, миграция — no-op.

ALTER TABLE public.gift_whitelists
    DROP COLUMN IF EXISTS disallow_upgrades,
    DROP COLUMN IF EXISTS disallow_resell,
    DROP COLUMN IF EXISTS apply_whitelist;

ALTER TABLE public.star_gift_auctions
    DROP COLUMN IF EXISTS round_durations;
