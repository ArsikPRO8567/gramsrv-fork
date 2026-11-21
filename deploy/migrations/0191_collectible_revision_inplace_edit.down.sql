-- Откат раунда 37: восстановление исходных замораживающих охран
-- опубликованных ревизий коллекции (0090) с учётом более позднего ослабления
-- supply_total (0189: повышение лимита разрешено).

CREATE OR REPLACE FUNCTION public.telesrv_guard_collectible_revision() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'published' THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        IF NEW.gift_id <> OLD.gift_id OR NEW.revision <> OLD.revision OR
           NEW.upgrade_stars <> OLD.upgrade_stars OR
           NEW.slug_prefix <> OLD.slug_prefix OR NEW.status <> OLD.status OR
           NEW.created_by <> OLD.created_by OR NEW.command_id <> OLD.command_id OR
           NEW.created_at <> OLD.created_at OR NEW.published_at <> OLD.published_at THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        IF NEW.issued = OLD.issued THEN
            IF NEW.supply_total < OLD.supply_total THEN
                RAISE EXCEPTION 'published collectible revision supply_total cannot decrease';
            END IF;
        ELSE
            IF NEW.issued <> OLD.issued + 1 OR NEW.supply_total <> OLD.supply_total THEN
                RAISE EXCEPTION 'published collectible revision issued can only increment by 1';
            END IF;
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.telesrv_guard_collectible_attribute() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    revision_status text;
BEGIN
    SELECT status INTO revision_status FROM public.star_gift_collectible_revisions
    WHERE id = COALESCE(OLD.collectible_revision_id, NEW.collectible_revision_id);
    IF revision_status = 'published' THEN
        RAISE EXCEPTION 'published collectible attributes are immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
