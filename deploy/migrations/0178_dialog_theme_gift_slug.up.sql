-- Тема оформления чата на базе коллекционного подарка: каждый участник
-- приватного диалога хранит slug подарка рядом с emoji-темой
-- (dialogs.theme_emoticon). Активная тема восстанавливается клиентом из
-- userFull.theme (chatThemeUniqueGift).
--
-- Идемпотентно: повторный запуск после авто-восстановления dirty не падает.
ALTER TABLE public.dialogs
  ADD COLUMN IF NOT EXISTS theme_gift_slug text NOT NULL DEFAULT '';
