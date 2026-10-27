-- require_premium у коллекционного (уникального) подарка наследуется от исходного
-- подарка каталога. Ранее флаг не переносился при апгрейде, поэтому уже выпущенные
-- коллекции из premium-подарков оставались без ограничения. Дозаполняем по
-- актуальному состоянию каталога (active_revision). Только усиление: флаг,
-- выставленный вручную, не снимается.
--
-- UPDATE затрагивает unique_star_gifts, поэтому на commit срабатывает отложенный
-- constraint-триггер unique_star_gift_owner_guard (0095): на базах с
-- рассинхронизированными агрегатами peer_star_gifts он прерывал миграцию ошибкой
-- "exported unique star gift has non-exported saved aggregate (SQLSTATE P0001)".
-- Миграция меняет только require_premium/updated_at и не касается инвариантов
-- guard'а (exported/saved/owner-связки), поэтому триггер отключается на время
-- backfill'а. Ремонт самих агрегатов выполняет идемпотентный 0179.
ALTER TABLE public.unique_star_gifts DISABLE TRIGGER unique_star_gift_owner_guard;

UPDATE public.unique_star_gifts u
SET require_premium = true,
    updated_at = now()
FROM public.star_gift_catalog c
JOIN public.star_gift_catalog_revisions ar ON ar.id = c.active_revision_id
WHERE c.gift_id = u.gift_id
  AND u.require_premium = false
  AND ar.require_premium = true;

ALTER TABLE public.unique_star_gifts ENABLE TRIGGER unique_star_gift_owner_guard;
