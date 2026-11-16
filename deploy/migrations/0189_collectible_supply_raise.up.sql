-- Раунд 34: административное изменение лимита возможных улучшенных подарков
-- (supply_total) опубликованной ревизии коллекции.
--
-- Зачем: supply каталога может быть увеличен админом (настройки выпуска),
-- но лимит улучшений живёт в ОПУБЛИКОВАННОЙ ревизии коллекции
-- (star_gift_collectible_revisions.supply_total) и охраняется триггером
-- неизменяемости (последняя версия — миграция 0093): апгрейд блокируется при
-- issued >= supply_total, и админка не могла поднять лимит.
--
-- Правило (база — guard 0093): определение ревизии остаётся байт-в-байт
-- (gift_id, revision, upgrade_stars, slug_prefix, status, created_by,
-- command_id, created_at, published_at, official_gift_id,
-- source_manifest_sha256), выдача по-прежнему продвигается ровно на +1 за
-- обновление, но supply_total теперь можно ТОЛЬКО ПОВЫШАТЬ (понижение/сброс
-- запрещены; инвариант issued <= supply_total дополнительно охраняет
-- констрейнт star_gift_collectible_supply_check). Допустимые формы UPDATE
-- опубликованной ревизии: (а) выдача issued = OLD.issued + 1 при неизменном
-- supply_total (путь апгрейда), (б) повышение supply_total при неизменном
-- issued (админский путь раунда 34).
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
           NEW.upgrade_stars <> OLD.upgrade_stars OR
           NEW.slug_prefix <> OLD.slug_prefix OR NEW.status <> OLD.status OR
           NEW.created_by <> OLD.created_by OR NEW.command_id <> OLD.command_id OR
           NEW.created_at <> OLD.created_at OR NEW.published_at <> OLD.published_at OR
           NEW.official_gift_id IS DISTINCT FROM OLD.official_gift_id OR
           NEW.source_manifest_sha256 IS DISTINCT FROM OLD.source_manifest_sha256 THEN
            RAISE EXCEPTION 'published collectible revision is immutable';
        END IF;
        IF NEW.supply_total < OLD.supply_total THEN
            RAISE EXCEPTION 'published collectible revision supply_total cannot decrease';
        END IF;
        -- Допустимые формы UPDATE: (а) выдача +1 (путь апгрейда, supply_total
        -- не меняется), (б) повышение supply_total при неизменной выдаче
        -- (админский путь раунда 34). Прочие изменения выдачи запрещены.
        IF NOT (NEW.issued = OLD.issued + 1) AND
           NOT (NEW.issued = OLD.issued AND NEW.supply_total > OLD.supply_total) THEN
            RAISE EXCEPTION 'published collectible issuance must advance exactly once';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
