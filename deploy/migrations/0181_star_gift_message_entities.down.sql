-- Откат entity-разметки附言 к подарку: колонка содержит только украшения
-- сообщения (премиум-эмодзи), текст附言 хранится отдельно в message, поэтому
-- удаление колонки не теряет основной контент.
ALTER TABLE public.peer_star_gifts
    DROP COLUMN IF EXISTS message_entities;
