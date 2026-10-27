-- Ремонт рассинхронизированных агрегатов peer_star_gifts и последующий
-- backfill theme_available.
--
-- Constraint-триггер unique_star_gift_owner_guard (0095/0128, DEFERRABLE
-- INITIALLY DEFERRED) требует для каждого уникального подарка согласованный
-- "saved aggregate" в peer_star_gifts на момент COMMIT любого изменения
-- unique_star_gifts или peer_star_gifts:
--   burned                        -> lifecycle_status = 'burned'
--   owner_address <> '' (export)  -> lifecycle_status = 'exported'
--   иначе (active)                -> lifecycle_status = 'active' и владелец
--                                    агрегата совпадает с владельцем подарка;
--   craft_chance_permille > 0     -> can_craft_at > 0 и наоборот.
-- На базах, где такие подарки заведены до ввода guard'а (или импортированы с
-- официального сервера в уже экспортированном состоянии), агрегат может
-- отставать от фактического состояния подарка. Любое обновление такой строки —
-- включая backfill theme_available из 0176 — прерывалось ошибкой
-- "exported unique star gift has non-exported saved aggregate (SQLSTATE P0001)".
--
-- Ниже агрегаты приводятся к состоянию, которое пишут живые потоки
-- (star_gift_lifecycle.go CompleteStarGiftWithdrawal, star_gift_craft_auction.go
-- burn): источник истины — unique_star_gifts, агрегат — его проекция.
-- Затем выполняется идемпотентный backfill theme_available (перенос 0176):
-- флаг выводится из активного флага peer_color_available активной ревизии
-- каталога исходного подарка (официальная модель: коллекционный подарок, чья
-- ревизия допускает peer color, разрешено использовать как палитру сообщений
-- и тему оформления чата).

-- 1) Экспортированные подарки (owner_address <> ''): агрегат -> 'exported'.
--    Зеркало CompleteStarGiftWithdrawal: unsaved=true, pinned_order=0, can_craft_at=0.
UPDATE public.peer_star_gifts p
SET lifecycle_status = 'exported',
    unsaved = true,
    pinned_order = 0,
    can_craft_at = 0
FROM public.unique_star_gifts u
WHERE p.unique_gift_id = u.id
  AND NOT u.burned
  AND u.owner_address <> ''
  AND (p.lifecycle_status <> 'exported' OR NOT p.unsaved
       OR p.pinned_order <> 0 OR p.can_craft_at <> 0);

-- 2) Экспортированный подарок с устаревшим шансом крафта: сброс (зеркало
--    экспорта: craft_chance_permille=0; шанс крафта допустим только у активных
--    подарков с владельцем).
UPDATE public.unique_star_gifts u
SET craft_chance_permille = 0,
    updated_at = now()
WHERE NOT u.burned
  AND u.owner_address <> ''
  AND u.craft_chance_permille <> 0;

-- 3) Сожжённые подарки: агрегат -> 'burned' (зеркало burn-потока).
UPDATE public.peer_star_gifts p
SET lifecycle_status = 'burned',
    unsaved = true,
    pinned_order = 0,
    can_craft_at = 0
FROM public.unique_star_gifts u
WHERE p.unique_gift_id = u.id
  AND u.burned
  AND (p.lifecycle_status <> 'burned' OR NOT p.unsaved
       OR p.pinned_order <> 0 OR p.can_craft_at <> 0);

-- 4) Сожжённый подарок с оставшимся шансом крафта: сброс (зеркало burn).
UPDATE public.unique_star_gifts u
SET craft_chance_permille = 0,
    updated_at = now()
WHERE u.burned
  AND u.craft_chance_permille <> 0;

-- 5) Активные подарки (не burned, без адреса): статус и владелец агрегата
--    синхронизируются с подарком. pinned_order/unsaved не трогаем — они не
--    входят в инвариант guard'а и хранят пользовательское состояние профиля.
UPDATE public.peer_star_gifts p
SET lifecycle_status = 'active',
    owner_peer_type = COALESCE(u.owner_peer_type, p.owner_peer_type),
    owner_peer_id = COALESCE(u.owner_peer_id, p.owner_peer_id)
FROM public.unique_star_gifts u
WHERE p.unique_gift_id = u.id
  AND NOT u.burned
  AND u.owner_address = ''
  AND (p.lifecycle_status <> 'active'
       OR p.owner_peer_type IS DISTINCT FROM u.owner_peer_type
       OR p.owner_peer_id IS DISTINCT FROM u.owner_peer_id);

-- 6) Шанс крафта у подарка, чья коллекционная ревизия не содержит ни одной
--    crafted-модели: сброс (guard требует существование crafted-модели в
--    ревизии; без неё способность к крафту неисправима).
UPDATE public.unique_star_gifts u
SET craft_chance_permille = 0,
    updated_at = now()
WHERE u.craft_chance_permille <> 0
  AND NOT EXISTS (
      SELECT 1
      FROM public.star_gift_collectible_models m
      WHERE m.collectible_revision_id = u.collectible_revision_id
        AND m.crafted);

-- 7) Готовность крафта без шанса крафта: сброс can_craft_at
--    (guard: "readiness exists without craft chance").
UPDATE public.peer_star_gifts p
SET can_craft_at = 0
FROM public.unique_star_gifts u
WHERE p.unique_gift_id = u.id
  AND COALESCE(u.craft_chance_permille, 0) = 0
  AND p.can_craft_at <> 0;

-- 8) Шанс крафта без готовности: can_craft_at = дата создания подарка
--    (минимально корректное значение; guard требует can_craft_at > 0).
UPDATE public.peer_star_gifts p
SET can_craft_at = GREATEST(1, EXTRACT(EPOCH FROM u.created_at)::int)
FROM public.unique_star_gifts u
WHERE p.unique_gift_id = u.id
  AND COALESCE(u.craft_chance_permille, 0) > 0
  AND NOT u.burned
  AND u.owner_address = ''
  AND NOT u.crafted
  AND p.can_craft_at <= 0;

-- 9) Backfill theme_available (перенос 0176, идемпотентен).
UPDATE public.unique_star_gifts u
SET theme_available = true,
    updated_at = now()
FROM public.star_gift_catalog c
JOIN public.star_gift_catalog_revisions ar ON ar.id = c.active_revision_id
WHERE c.gift_id = u.gift_id
  AND u.theme_available = false
  AND ar.peer_color_available = true;
