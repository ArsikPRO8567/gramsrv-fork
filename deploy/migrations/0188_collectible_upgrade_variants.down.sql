-- Откат 0188_collectible_upgrade_variants: колонка и констрейнт только добавлены,
-- синхронизация каталога не изменяла схему (обнулять пересчитанные значения не
-- требуется: они были выведены из самих пулов атрибутов).

ALTER TABLE public.star_gift_collectible_revisions
    DROP CONSTRAINT IF EXISTS star_gift_collectible_upgrade_variants_check,
    DROP COLUMN IF EXISTS upgrade_variants;
