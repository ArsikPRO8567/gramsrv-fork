-- Палитра сообщений из коллекционного подарка (официальный
-- peerColorCollectible): пользователи хранят неизменяемый снапшот, выведенный
-- из фона/узора подарка при account.updateColor с inputPeerColorCollectible
-- (только message-палитра; профильные палитры через emoji status).
--
-- Формулировки идемпотентны: после сбоя миграции и авто-восстановления dirty
-- (postgres.go) повторный запуск не спотыкается о уже созданные объекты.
ALTER TABLE public.users
  ADD COLUMN IF NOT EXISTS color_collectible jsonb DEFAULT '{}'::jsonb NOT NULL;

-- ADD CONSTRAINT IF NOT EXISTS в PostgreSQL нет, поэтому проверяем каталог.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'users_color_collectible_shape_check'
          AND conrelid = 'public.users'::regclass
    ) THEN
        ALTER TABLE public.users
          ADD CONSTRAINT users_color_collectible_shape_check CHECK (
            (
              color_collectible = '{}'::jsonb
            ) OR (
              color_collectible <> '{}'::jsonb
              AND NOT color_set
              AND jsonb_typeof(color_collectible) = 'object'
              AND color_collectible ? 'collectible_id'
              AND color_collectible ? 'gift_emoji_id'
              AND color_collectible ? 'background_emoji_id'
              AND color_collectible ? 'accent_color'
              AND color_collectible ? 'colors'
              AND (color_collectible->>'collectible_id')::bigint > 0
              AND (color_collectible->>'gift_emoji_id')::bigint > 0
              AND (color_collectible->>'background_emoji_id')::bigint > 0
              AND (color_collectible->>'accent_color')::integer BETWEEN 0 AND 16777215
              AND jsonb_typeof(color_collectible->'colors') = 'array'
              AND jsonb_array_length(color_collectible->'colors') BETWEEN 1 AND 3
            )
          );
    END IF;
END
$$;
