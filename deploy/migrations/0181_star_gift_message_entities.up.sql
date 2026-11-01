-- Entity-разметка附言 к подарку (премиум-эмодзи и стиль) в peer_star_gifts.
--
-- Оригинальный сервер сохраняет message.entities из invoice.message при
-- отправке подарка и отдаёт её в messageActionStarGift.message
-- (TextWithEntities) и в savedStarGift.message профиля. Ранее хранился только
-- текст: премиум-эмодзи в сообщении к подарку теряли entity и рисовались
-- как обычные эмодзи.
--
-- Хранение: jsonb-массив domain.MessageEntity ({"type":"custom_emoji",
-- "offset":N,"length":N,"document_id":N,...}). Старые строки читаются как
-- пустой массив (COALESCE на стороне SELECT), поэтому колонка добавляется
-- с NOT NULL DEFAULT '[]' — существующие данные не трогаются.
ALTER TABLE public.peer_star_gifts
    ADD COLUMN IF NOT EXISTS message_entities jsonb NOT NULL DEFAULT '[]'::jsonb;
