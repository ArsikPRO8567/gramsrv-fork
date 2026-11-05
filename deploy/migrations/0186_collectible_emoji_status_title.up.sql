-- Round 23: emoji status collectible titles must read "{name} #{number}"
-- (official shape: emojiStatusCollectible{title:'Spy Agaric #5',
-- slug:'SpyAgaric-5'}). Existing immutable user snapshots store the bare gift
-- name, so rewrite them in place; future updates reproject with the same
-- format, keeping the stored/projection invariant intact.
WITH snapshot AS (
    SELECT id,
           emoji_status_collectible_id,
           emoji_status_collectible
    FROM users
    WHERE emoji_status_collectible_id IS NOT NULL
      AND emoji_status_collectible ? 'title'
),
joined AS (
    SELECT snapshot.id,
           snapshot.emoji_status_collectible,
           gift.num,
           COALESCE(snapshot.emoji_status_collectible ->> 'title', '') AS title
    FROM snapshot
    JOIN unique_star_gifts gift ON gift.id = snapshot.emoji_status_collectible_id
    WHERE gift.num > 0
)
UPDATE users u
SET emoji_status_collectible = jsonb_set(
        j.emoji_status_collectible,
        '{title}',
        to_jsonb(j.title || ' #' || j.num),
        true),
    updated_at = now()
FROM joined j
WHERE u.id = j.id
  AND j.title <> ''
  AND j.title NOT LIKE '% #' || j.num::text;
