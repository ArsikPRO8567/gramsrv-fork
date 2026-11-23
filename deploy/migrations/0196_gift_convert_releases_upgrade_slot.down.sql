-- Откат раунда 41: возвращает охрану 0191 — любое уменьшение issued
-- опубликованной коллекционной ревизии запрещено (конвертация подарка больше
-- не освобождает слот улучшения).

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
        IF NEW.status <> OLD.status OR NEW.gift_id <> OLD.gift_id OR
           NEW.revision <> OLD.revision OR NEW.issued < OLD.issued OR
           NEW.created_at <> OLD.created_at OR NEW.published_at <> OLD.published_at THEN
            RAISE EXCEPTION 'published collectible revision identity cannot change';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
