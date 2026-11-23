-- Возврат к исходному shape-check 0089: только ровно 512×512.

ALTER TABLE public.star_gift_catalog_revisions
    DROP CONSTRAINT star_gift_catalog_revision_shape_check;

ALTER TABLE public.star_gift_catalog_revisions
    ADD CONSTRAINT star_gift_catalog_revision_shape_check
    CHECK (width = 512 AND height = 512 AND jsonb_typeof(animation_json) = 'object');
