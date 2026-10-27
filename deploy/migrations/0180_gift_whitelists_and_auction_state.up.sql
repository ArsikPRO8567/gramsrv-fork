-- Две таблицы, на которые опирается код, но которые не создавались ни одной
-- миграцией набора: они существовали только в исторических базах (унаследованы
-- от схемы до форка). На свежей базе отсутствие gift_whitelists ломало любой
-- RPC с проверкой вайтлиста (SQLSTATE 42P01 "relation does not exist"), а
-- отсутствие star_gift_auction_state молча отключало счётчик номеров подарков
-- аукционных раундов (UPDATE без совпадений).
--
-- Обе формулировки идемпотентны (IF NOT EXISTS): на исторических базах, где
-- таблица уже создана администратором вручную, миграция — no-op и не трогает
-- существующие данные и форму таблицы.

-- Вайтлист редкого подарка (round 2): строка ограничивает отправку/ставки
-- списком аккаунтов allowed_buyers (jsonb-массив текстовых user id).
-- gift_hidden скрывает подарок от не-вайтлистных пользователей в каталоге,
-- numbered включает выдачу пронумерованных экземпляров.
-- Управление таблицей — административное (psql/внешние инструменты), сервер
-- только читает её (StarGiftStore.GetWhitelistInfo).
CREATE TABLE IF NOT EXISTS public.gift_whitelists (
    gift_id        bigint PRIMARY KEY,
    allowed_buyers jsonb NOT NULL DEFAULT '[]'::jsonb,
    gift_hidden    boolean NOT NULL DEFAULT false,
    numbered       boolean NOT NULL DEFAULT false
);

-- Счётчик номеров экземпляров, разыгранных в аукционных раундах редкого
-- подарка: BurnAuctionGifts увеличивает last_gift_num на число выданных
-- подарков, чтобы номера не пересекались с обычными покупками.
CREATE TABLE IF NOT EXISTS public.star_gift_auction_state (
    gift_id       bigint PRIMARY KEY,
    last_gift_num bigint NOT NULL DEFAULT 0
);
