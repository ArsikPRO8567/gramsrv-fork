-- Раунд 37: перевод средств между аккаунтами пишется ДВУМЯ строками в двух
-- личных леджерах (или в леджере канала) с ОДНИМ публичным transaction_id —
-- клиент показывает обе стороны как одну операцию (продажа подарка, платное
-- сообщение и т.п.). Глобальный уникальный индекс раунда 30 не позволял двум
-- аккаунтам иметь один и тот же ID; уникальность сохраняется в пределах
-- одного владельца леджера (защита от двойной записи внутри аккаунта), а
-- случайные коллизии 62^60-пространства по-прежнему невозможны на практике.
--
-- Исторические строки раунда 30 (transaction_id = десятичная строка identity)
-- уникальны в пределах владельца и без изменений проходят новый индекс.

DROP INDEX IF EXISTS public.stars_transactions_transaction_id_uidx;
DROP INDEX IF EXISTS public.ton_transactions_transaction_id_uidx;
DROP INDEX IF EXISTS public.channel_stars_transactions_transaction_id_uidx;
DROP INDEX IF EXISTS public.channel_ton_transactions_transaction_id_uidx;

CREATE UNIQUE INDEX IF NOT EXISTS stars_transactions_owner_transaction_uidx
    ON public.stars_transactions(user_id, transaction_id) WHERE transaction_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS ton_transactions_owner_transaction_uidx
    ON public.ton_transactions(user_id, transaction_id) WHERE transaction_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS channel_stars_transactions_owner_transaction_uidx
    ON public.channel_stars_transactions(channel_id, transaction_id) WHERE transaction_id <> '';
CREATE UNIQUE INDEX IF NOT EXISTS channel_ton_transactions_owner_transaction_uidx
    ON public.channel_ton_transactions(channel_id, transaction_id) WHERE transaction_id <> '';
