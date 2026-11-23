-- Раунд 42: конвертация редкого подарка сжимает САПЛАЙ УЛУЧШЕНИЙ
-- (star_gift_collectible_revisions.supply_total -= 1, см.
-- releaseUpgradeSlotOnConvertTx). Когда все слоты пула выжжены
-- (issued = 0, последний свободный слот сконвертирован), supply_total
-- достигает 0 — это корректное конечное состояние пула («улучшений больше
-- не существует»); исходный CHECK 0090 требовал supply_total > 0 и обрушил
-- бы транзакцию конвертации. Ослабляем до supply_total >= 0; инвариант
-- issued <= supply_total сохраняется.

ALTER TABLE public.star_gift_collectible_revisions
    DROP CONSTRAINT star_gift_collectible_supply_check;

ALTER TABLE public.star_gift_collectible_revisions
    ADD CONSTRAINT star_gift_collectible_supply_check
    CHECK (supply_total >= 0 AND issued >= 0 AND issued <= supply_total);
