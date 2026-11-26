-- Раунд 47 (задача ①): нормализация флага saved в существующих сервисных
-- сообщениях о подарках.
--
-- С раунда 42 сервер переписывал action.saved в messageActionStarGift при
-- переключении отображения подарка в профиле (payments.saveStarGift).
-- Официальный Android-клиент читает этот флаг только для выбора подписи блока
-- подарка (ChatActionCell): saved=true при convert_stars>0 даёт подпись
-- «Вы добавили этот подарок в свой профиль», даже когда отображение выставил
-- сервер (автопоказ по приватности) или сам пользователь, а подпись
-- «Вы добавили...» описывает только ручное добавление. Требование раунда 47:
-- главная подпись-плейсхолдер в блоке не меняется НИЗАВИСИМО от статуса
-- отображения — сервер больше не переписывает saved (см. ToggleStarGiftSaved),
-- а исторические строки нормализуются этой миграцией: saved=false для
-- приватных (пользовательских) копий messageActionStarGift.
--
-- Канальные копии (peer_channel_id задан) не трогаются: подпись канального
-- уведомления строится из ветки isForChannel и от saved не зависит.
-- Уникальные подарки (messageActionStarGiftUnique, kind='star_gift_unique')
-- не затрагиваются: подписи их блоков от saved не зависят.

UPDATE public.message_boxes
SET media = jsonb_set(media, '{service_action,star_gift,saved}', 'false'::jsonb, true)
WHERE media->>'kind' = 'service'
  AND media->'service_action'->>'kind' = 'star_gift'
  AND media->'service_action'->'star_gift'->'peer_channel_id' IS NULL
  AND (media->'service_action'->'star_gift'->>'saved')::boolean IS TRUE;

UPDATE public.private_messages
SET media = jsonb_set(media, '{service_action,star_gift,saved}', 'false'::jsonb, true)
WHERE media->>'kind' = 'service'
  AND media->'service_action'->>'kind' = 'star_gift'
  AND media->'service_action'->'star_gift'->'peer_channel_id' IS NULL
  AND (media->'service_action'->'star_gift'->>'saved')::boolean IS TRUE;
