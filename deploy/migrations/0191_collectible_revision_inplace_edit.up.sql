-- Раунд 37: правка опубликованной ревизии коллекции «на месте».
--
-- Ранее строка опубликованной ревизии и её атрибуты были полностью
-- заморожены (0090): любая правка публиковала НОВУЮ ревизию. У этого два
-- плохих следствия: (а) выпущенные подарки продолжают ссылаться на старые
-- строки атрибутов, поэтому серверная логика (крафт, превью улучшения,
-- rarity-пул) расщепляется между ревизиями; (б) список ревизий одной
-- коллекции растёт от каждой правки.
--
-- Теперь команда publish обновляет СУЩЕСТВУЮЩУЮ опубликованную ревизию того
-- же подарка: строки атрибутов правятся в тех же строках (id сохраняются —
-- выпущенные подарки продолжают видеть актуальные название/редкость/порядок),
-- новые атрибуты добавляются, удалённые из формы — снимаются (FK ON DELETE
-- RESTRICT не даёт снять атрибут, на который ссылается выпущенный подарок).
--
-- Охрана смягчается до содержательных инвариантов:
--  * ревизия не может сменить подарок/номер/статус, issued не может
--    уменьшиться (порядок выдачи непрозрачно переписывать нельзя), а
--    supply_total ограничен табличным CHECK (issued <= supply_total);
--  * строка атрибута не может переехать в другую ревизию — составные FK
--    (attribute_id, collectible_revision_id) выпущенных подарков должны
--    оставаться валидными. Удаление разрешено: FK RESTRICT защищает строки,
--    на которые есть ссылки.

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

CREATE OR REPLACE FUNCTION public.telesrv_guard_collectible_attribute() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    IF NEW.collectible_revision_id IS DISTINCT FROM OLD.collectible_revision_id THEN
        RAISE EXCEPTION 'collectible attribute cannot move between revisions';
    END IF;
    RETURN NEW;
END;
$$;
