-- Round 23 rollback: restore bare gift names in collectible emoji status
-- titles. Only snapshots whose title is exactly "{gift title} #{gift num}" are
-- rewritten; anything else is left untouched.
UPDATE users u
SET emoji_status_collectible = jsonb_set(
        u.emoji_status_collectible,
        '{title}',
        to_jsonb(gift.title),
        true),
    updated_at = now()
FROM unique_star_gifts gift
WHERE gift.id = u.emoji_status_collectible_id
  AND u.emoji_status_collectible ? 'title'
  AND u.emoji_status_collectible ->> 'title' = gift.title || ' #' || gift.num::text;
