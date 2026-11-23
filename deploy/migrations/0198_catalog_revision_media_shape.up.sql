-- Раунд 42: медиа-анимации подарков (.webp/.webm/.png) могут быть меньше
-- 512×512 и не квадратными — официальный клиент масштабирует блок подарка под
-- документ (prepareMediaAnimation валидирует предел 512×512 по лимитам
-- стикеров Telegram). Shape-check 0089 требовал ровно 512×512, из-за чего
-- валидный медиа-исходник отвергался на этапе проверки метаданных уже после
-- успешного парсинга заголовков.

ALTER TABLE public.star_gift_catalog_revisions
    DROP CONSTRAINT star_gift_catalog_revision_shape_check;

ALTER TABLE public.star_gift_catalog_revisions
    ADD CONSTRAINT star_gift_catalog_revision_shape_check
    CHECK (width >= 1 AND width <= 512 AND height >= 1 AND height <= 512
           AND jsonb_typeof(animation_json) = 'object');
