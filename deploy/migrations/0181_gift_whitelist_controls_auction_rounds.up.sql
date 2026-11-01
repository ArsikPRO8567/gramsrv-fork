-- Раунд 20: расширенное управление вайтлистами подарков и индивидуальные
-- длительности раундов аукционов.
--
-- gift_whitelists: три новых административных столбца.
--   disallow_upgrades — улучшать подарок могут только пользователи из
--     allowed_buyers; остальным форма улучшения возвращает
--     STARGIFT_UPGRADE_UNAVAILABLE уже на payments.getPaymentForm.
--   disallow_resell — делать офферы и покупать улучшенные экземпляры на
--     маркете могут только пользователи из allowed_buyers; остальным
--     возвращается STARGIFT_RESELL_NOT_ALLOWED / STARGIFT_OFFER_NOT_ALLOWED,
--     а кнопка «Предложить сделку» на экранах подарка не отдаётся. Выставлять
--     на продажу и разрешать/отклонять офферы на свои подарки при этом могут все.
--   apply_whitelist — мастер-выключатель вайтлиста. При false ограничения
--     (allowed_buyers, gift_hidden, disallow_upgrades, disallow_resell)
--     не действуют; numbered работает независимо от этого флага.
--     По умолчанию TRUE: строки, созданные до миграции, продолжают ограничивать
--     отправку/ставки как раньше (прежнее поведение кода — «запись существует,
--     значит действует»).
-- Управление таблицей по-прежнему административное (psql/внешние инструменты),
-- сервер только читает её.

ALTER TABLE public.gift_whitelists
    ADD COLUMN IF NOT EXISTS disallow_upgrades boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS disallow_resell    boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS apply_whitelist    boolean NOT NULL DEFAULT true;

-- star_gift_auctions: индивидуальные длительности раундов в секундах.
-- Формат — jsonb-объект вида {"round_1":120,"round_2":300,"round_4":1000};
-- для раундов, отсутствующих в объекте, действует столбец round_duration.
-- NULL (значение по умолчанию) — общая длительность round_duration для всех
-- раундов. Столбец заполняется администратором; сервер только читает его.

ALTER TABLE public.star_gift_auctions
    ADD COLUMN IF NOT EXISTS round_durations jsonb;
