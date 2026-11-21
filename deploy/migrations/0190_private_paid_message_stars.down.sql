-- Откат раунда 37: платные сообщения в личных чатах.

ALTER TABLE public.message_boxes
    DROP CONSTRAINT IF EXISTS message_boxes_paid_message_stars_check,
    DROP COLUMN IF EXISTS paid_message_stars;

ALTER TABLE public.private_messages
    DROP CONSTRAINT IF EXISTS private_messages_paid_message_stars_check,
    DROP COLUMN IF EXISTS paid_message_stars;
