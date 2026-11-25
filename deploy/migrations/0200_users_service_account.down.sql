-- Откат миграции 0200: удаление флага service_account.
ALTER TABLE public.users DROP COLUMN IF EXISTS service_account;
