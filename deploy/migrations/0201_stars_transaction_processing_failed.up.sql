-- Раунд 46: статусные флаги «В обработке» (processing) и «Ошибка» (failed)
-- в таблицах транзакций — продуктивная схема, используемая интеграциями.
--
-- Оба флага независимы от направления суммы (как refunded):
--   processing — средства, вывод или зачисление которых будет обрабатываться
--                (отложенные интеграционные платежи);
--   failed     — интеграция со сторонним ресурсом завершилась ошибкой.
--
-- Флаги не проецируются в tg.starsTransaction (в официальной схеме нет
-- соответствующих полей): их читают учётные системы и интеграции напрямую из БД.
-- Новый тип транзакции «Реклама» (reason='ads') отдельной колонки не требует —
-- reason уже текстовый, код и клиентская проекция добавлены в этом раунде.
--
-- Примечание (фикс сборки run 45743737505): миграция перенумерована
-- 0188 -> 0201, потому что номер 0188 уже занят star_gift_paid_stars;
-- golang-migrate не допускает дубликатов версий.

ALTER TABLE public.stars_transactions
    ADD COLUMN IF NOT EXISTS processing boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS failed boolean NOT NULL DEFAULT false;

ALTER TABLE public.ton_transactions
    ADD COLUMN IF NOT EXISTS processing boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS failed boolean NOT NULL DEFAULT false;

ALTER TABLE public.channel_stars_transactions
    ADD COLUMN IF NOT EXISTS processing boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS failed boolean NOT NULL DEFAULT false;

ALTER TABLE public.channel_ton_transactions
    ADD COLUMN IF NOT EXISTS processing boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS failed boolean NOT NULL DEFAULT false;
