-- sold_out на активной ревизии каталога должен отражать фактическое исчерпание
-- лимита редкого (limited) подарка: как только availability_remains каталога
-- доходит до нуля, флаг ставится в true. Самовосстановление для уже
-- распроданных подарков (флаг выставлялся только при импорте с официального
-- сервера и при административной раздаче, но не при исчерпании на живом
-- сервере).
UPDATE star_gift_catalog_revisions r
SET sold_out = true
FROM star_gift_catalog c
WHERE c.active_revision_id = r.id
  AND c.gift_id = r.gift_id
  AND r.limited
  AND c.availability_remains <= 0
  AND NOT r.sold_out;
