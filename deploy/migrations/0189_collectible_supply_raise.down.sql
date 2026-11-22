-- Откат раунда 34: восстановление guard-триггера неизменяемости из миграции
-- 0093 (supply_total снова байт-в-байт, выдача строго +1).
CREATE OR REPLACE FUNCTION public.telesrv_guard_collectible_revision() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status = 'published' THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.status = 'published' THEN
        IF NEW.gift_id <> OLD.gift_id OR NEW.revision <> OLD.revision OR
           NEW.upgrade_stars <> OLD.upgrade_stars OR NEW.supply_total <> OLD.supply_total OR
           NEW.slug_prefix <> OLD.slug_prefix OR NEW.status <> OLD.status OR
           NEW.created_by <> OLD.created_by OR NEW.command_id <> OLD.command_id OR
           NEW.created_at <> OLD.created_at OR NEW.published_at <> OLD.published_at OR
           NEW.official_gift_id IS DISTINCT FROM OLD.official_gift_id OR
           NEW.source_manifest_sha256 IS DISTINCT FROM OLD.source_manifest_sha256 THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        IF NEW.issued <> OLD.issued + 1 THEN
            RAISE EXCEPTION 'published collectible issuance must advance exactly once';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
