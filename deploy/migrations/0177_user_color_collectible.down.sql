ALTER TABLE public.users
  DROP CONSTRAINT IF EXISTS users_color_collectible_shape_check;

ALTER TABLE public.users
  DROP COLUMN IF EXISTS color_collectible;
