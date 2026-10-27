-- Откат: сбрасываем только те флаги, которые выставил апгрейд 0175
-- (limited-подарки с исчерпанным остатком). Флаги, выставленные вручную
-- администратором или импортом, могут быть потеряны — даун-миграция
-- симметрична и используется только в тестовой среде.
UPDATE star_gift_catalog_revisions r
SET sold_out = false
FROM star_gift_catalog c
WHERE c.active_revision_id = r.id
  AND c.gift_id = r.gift_id
  AND r.limited
  AND c.availability_remains <= 0
  AND r.sold_out;
