-- Раунд 37: количество возможных вариантов NFT в ревизии коллекционного пула.
--
-- При публикации пула атрибутов общее число вариантов NFT равно произведению
-- количеств атрибутов: модели × узоры × фоны (например 10 моделей × 5 узоров
-- × 10 фонов = 500). Итог записывается в ревизию пула
-- (star_gift_collectible_revisions.upgrade_variants) и синхронизируется в
-- активную ревизию каталога (star_gift_catalog_revisions.upgrade_variants) —
-- именно её клиенты читают в starGift.upgrade_variants, показывая «сколько
-- вариантов коллекционного подарка возможно собрать».

ALTER TABLE public.star_gift_collectible_revisions
    ADD COLUMN IF NOT EXISTS upgrade_variants integer DEFAULT 0 NOT NULL,
    ADD CONSTRAINT star_gift_collectible_upgrade_variants_check CHECK (upgrade_variants >= 0);

-- Историческим ревизиям пересчитываем произведение по фактическим пулам
-- (условие EXISTS гарантирует произведение >= 1).
UPDATE public.star_gift_collectible_revisions r
SET upgrade_variants =
    (SELECT COUNT(*) FROM public.star_gift_collectible_models m WHERE m.collectible_revision_id = r.id)
    * (SELECT COUNT(*) FROM public.star_gift_collectible_patterns p WHERE p.collectible_revision_id = r.id)
    * (SELECT COUNT(*) FROM public.star_gift_collectible_backdrops b WHERE b.collectible_revision_id = r.id)
WHERE r.upgrade_variants = 0
  AND EXISTS (SELECT 1 FROM public.star_gift_collectible_models m WHERE m.collectible_revision_id = r.id)
  AND EXISTS (SELECT 1 FROM public.star_gift_collectible_patterns p WHERE p.collectible_revision_id = r.id)
  AND EXISTS (SELECT 1 FROM public.star_gift_collectible_backdrops b WHERE b.collectible_revision_id = r.id);

-- Активные ревизии каталога с нулевым значением получают итог активного пула,
-- чтобы уже опубликованные подарки начали показывать количество вариантов без
-- перепубликации. Явно заданные значения (официальный импорт) не затираются.
UPDATE public.star_gift_catalog_revisions c
SET upgrade_variants = pool.upgrade_variants
FROM public.star_gift_catalog cat
JOIN public.star_gift_collectible_revisions pool ON pool.id = cat.collectible_revision_id
WHERE c.id = cat.active_revision_id
  AND c.upgrade_variants = 0
  AND pool.upgrade_variants > 0;
