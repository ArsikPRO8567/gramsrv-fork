-- Откат: снимаем theme_available там, где его выставил апгрейд 0176
-- (уникальные подарки из ревизий с peer_color_available). Флаги, выставленные
-- вручную, могут быть потеряны — даун-миграция симметрична и используется
-- только в тестовой среде.
UPDATE unique_star_gifts u
SET theme_available = false,
    updated_at = now()
FROM star_gift_catalog c
JOIN star_gift_catalog_revisions ar ON ar.id = c.active_revision_id
WHERE c.gift_id = u.gift_id
  AND u.theme_available = true
  AND ar.peer_color_available = true;
