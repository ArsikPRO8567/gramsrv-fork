-- Тема оформления чата на базе коллекционного подарка: каждый участник
-- приватного диалога хранит slug подарка рядом с emoji-темой
-- (dialogs.theme_emoticon). Активная тема восстанавливается клиентом из
-- userFull.theme (chatThemeUniqueGift).
ALTER TABLE public.dialogs
  ADD COLUMN theme_gift_slug text NOT NULL DEFAULT '';
