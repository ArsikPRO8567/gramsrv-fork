-- Раунд 37 (проблема C): парные переводы «аккаунт -> аккаунт» в пользовательских
-- леджерах. Покупка подарка на маркете и платные сообщения пишут ДВЕ строки с
-- ОДНИМ публичным transaction_id: дебет плательщика и кредит получателя. Уникальный
-- индекс из 0187 запрещал второй строке нести тот же id, поэтому пары продавец/
-- покупатель и отправитель/получатель получали разные идентификаторы.
--
-- Индекс пересоздаётся как обычный (не уникальный): дубликаты случайных 62^60
-- идентификаторов невозможны, а одинаковые id в парных строках — требование
-- продукта (клиент сопоставляет перевод между аккаунтами по этому полю).
-- Канальные леджеры остаются уникальными: кредит канала лежит в отдельной
-- таблице и внутри неё ни с чем не парится.
--
-- Миграция идемпотентна и безопасна для баз, где 0187 уже применён в исходной
-- (уникальной) форме: она снимает уникальный индекс и создаёт обычный с тем же
-- именем.

DROP INDEX IF EXISTS public.stars_transactions_transaction_id_uidx;
CREATE INDEX IF NOT EXISTS stars_transactions_transaction_id_uidx
    ON public.stars_transactions(transaction_id) WHERE transaction_id <> '';

DROP INDEX IF EXISTS public.ton_transactions_transaction_id_uidx;
CREATE INDEX IF NOT EXISTS ton_transactions_transaction_id_uidx
    ON public.ton_transactions(transaction_id) WHERE transaction_id <> '';
