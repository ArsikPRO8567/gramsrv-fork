-- theme_available у уникальных подарков выводится из активного флага
-- peer_color_available ревизии каталога исходного подарка (официальная модель:
-- коллекционный подарок, чья ревизия допускает peer color, разрешено
-- использовать как палитру сообщений и тему оформления чата). Апгрейд 0175+
-- проставляет флаг при выпуске новых коллекций; здесь дозаполняем все уже
-- выпущенные уникальные подарки.
UPDATE unique_star_gifts u
SET theme_available = true,
    updated_at = now()
FROM star_gift_catalog c
JOIN star_gift_catalog_revisions ar ON ar.id = c.active_revision_id
WHERE c.gift_id = u.gift_id
  AND u.theme_available = false
  AND ar.peer_color_available = true;
