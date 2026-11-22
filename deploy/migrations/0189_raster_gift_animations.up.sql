-- Раунд 37: растровые форматы анимаций подарков.
--
-- Официальные клиенты Telegram рендерят подарки не только из TGS (Lottie), но
-- и из анимированных .webp, видео-стикеров .webm и статичных .png. Админка
-- получает возможность загружать эти форматы, а сервер хранит их как отдельные
-- source_format'ы рядом с tgs/lottie и отдаёт клиентам документ с корректным
-- mime и атрибутами (documentAttributeAnimated для webm).

ALTER TABLE public.star_gift_catalog_revisions
    DROP CONSTRAINT star_gift_catalog_revision_source_check;
ALTER TABLE public.star_gift_catalog_revisions
    ADD CONSTRAINT star_gift_catalog_revision_source_check
    CHECK (source_format IN ('tgs', 'lottie', 'webp', 'webm', 'png'));

ALTER TABLE public.star_gift_collectible_models
    DROP CONSTRAINT star_gift_collectible_model_source_check;
ALTER TABLE public.star_gift_collectible_models
    ADD CONSTRAINT star_gift_collectible_model_source_check
    CHECK (source_format IN ('tgs', 'lottie', 'webp', 'webm', 'png'));

ALTER TABLE public.star_gift_collectible_patterns
    DROP CONSTRAINT star_gift_collectible_pattern_source_check;
ALTER TABLE public.star_gift_collectible_patterns
    ADD CONSTRAINT star_gift_collectible_pattern_source_check
    CHECK (source_format IN ('tgs', 'lottie', 'webp', 'webm', 'png'));
