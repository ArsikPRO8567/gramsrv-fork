-- Откат: снимаем theme_available у подарков из ревизий с peer_color_available
-- (симметрично backfill'у). Ремонт агрегатов (шаги 1-7 up-миграции) обратной
-- силы не имеет — он приводил данные к инвариантам guard'а и не отменяется.
UPDATE public.unique_star_gifts u
SET theme_available = false,
    updated_at = now()
FROM public.star_gift_catalog c
JOIN public.star_gift_catalog_revisions ar ON ar.id = c.active_revision_id
WHERE c.gift_id = u.gift_id
  AND u.theme_available = true
  AND ar.peer_color_available = true;
