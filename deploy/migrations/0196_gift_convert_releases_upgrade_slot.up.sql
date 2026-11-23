-- Раунд 41: конвертация редкого подарка, имеющего улучшения, освобождает
-- слот улучшения — общее число выпущенных улучшений
-- (star_gift_collectible_revisions.issued) уменьшается на 1.
--
-- Охрана 0191 запрещает ЛЮБОЕ уменьшение issued у опубликованной ревизии:
-- «порядок выдачи непрозрачно переписывать нельзя». Конвертация подарка —
-- санкционированный сервером путь, при котором слот улучшения законно
-- освобождается: клиент рисует кнопку улучшения по issued < supply_total,
-- и после конвертации один экземпляр снова может быть улучшен.
--
-- Охрана смягчается точечно: уменьшение issued разрешено ТОЛЬКО внутри
-- транзакции конвертации, помеченной локальной настройкой
-- gramsrv.star_gift_release_slot = 'on' (set_config(..., is_local => true) —
-- действует до конца транзакции и автоматически сбрасывается при
-- фиксации/откате). Все прочие уменьшения (ручные UPDATE, административные
-- команды) по-прежнему отвергаются.

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
           NEW.revision <> OLD.revision OR
           NEW.created_at <> OLD.created_at OR NEW.published_at <> OLD.published_at THEN
            RAISE EXCEPTION 'published collectible revision identity cannot change';
        END IF;
        IF NEW.issued < OLD.issued AND
           COALESCE(current_setting('gramsrv.star_gift_release_slot', true), '') <> 'on' THEN
            RAISE EXCEPTION 'published collectible issuance cannot decrease outside gift conversion';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
