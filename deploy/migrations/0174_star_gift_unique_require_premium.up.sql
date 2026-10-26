-- require_premium у коллекционного (уникального) подарка наследуется от исходного
-- подарка каталога. Ранее флаг не переносился при апгрейде, поэтому уже выпущенные
-- коллекции из premium-подарков оставались без ограничения. Дозаполняем по
-- актуальному состоянию каталога (active_revision). Только усиление: флаг,
-- выставленный вручную, не снимается.
UPDATE unique_star_gifts u
SET require_premium = true,
    updated_at = now()
FROM star_gift_catalog c
JOIN star_gift_catalog_revisions ar ON ar.id = c.active_revision_id
WHERE c.gift_id = u.gift_id
  AND u.require_premium = false
  AND ar.require_premium = true;
