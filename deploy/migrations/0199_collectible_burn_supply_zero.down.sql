-- Возврат к исходному CHECK 0090: supply_total строго положительный.

ALTER TABLE public.star_gift_collectible_revisions
    DROP CONSTRAINT star_gift_collectible_supply_check;

ALTER TABLE public.star_gift_collectible_revisions
    ADD CONSTRAINT star_gift_collectible_supply_check
    CHECK (supply_total > 0 AND issued >= 0 AND issued <= supply_total);
