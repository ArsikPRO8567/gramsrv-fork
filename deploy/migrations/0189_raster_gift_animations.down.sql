-- Откат 0189_raster_gift_animations: восстановление исходных ограничений
-- source_format. Выполняется только когда растровых ревизий нет — при наличии
-- строк с новым форматом откат вернёт ошибку CHECK (это осознанное поведение:
-- данные нельзя незаметно потерять).

ALTER TABLE public.star_gift_collectible_patterns
    DROP CONSTRAINT star_gift_collectible_pattern_source_check;
ALTER TABLE public.star_gift_collectible_patterns
    ADD CONSTRAINT star_gift_collectible_pattern_source_check
    CHECK (source_format IN ('tgs', 'lottie'));

ALTER TABLE public.star_gift_collectible_models
    DROP CONSTRAINT star_gift_collectible_model_source_check;
ALTER TABLE public.star_gift_collectible_models
    ADD CONSTRAINT star_gift_collectible_model_source_check
    CHECK (source_format IN ('tgs', 'lottie'));

ALTER TABLE public.star_gift_catalog_revisions
    DROP CONSTRAINT star_gift_catalog_revision_source_check;
ALTER TABLE public.star_gift_catalog_revisions
    ADD CONSTRAINT star_gift_catalog_revision_source_check
    CHECK (source_format IN ('tgs', 'lottie'));
