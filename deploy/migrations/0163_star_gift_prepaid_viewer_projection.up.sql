-- inputInvoiceStarGiftPrepaidUpgrade is for prepaying someone else's gift:
-- its peer is the gift owner. Earlier user-gift purchases copied both
-- receiver-only can_upgrade and non-owner prepaid_upgrade_hash into both
-- private message boxes. DrKLO therefore preferred the prepaid invoice on the
-- owner's incoming card and substituted the private dialog peer (the sender),
-- which correctly failed STARGIFT_INVALID.
--
-- Repair both viewer projections and publish a real account-scoped edit for
-- every changed box. The saved-gift aggregate and its owner/hash remain intact;
-- only the wire capability surface changes.

LOCK TABLE public.peer_star_gifts, public.message_boxes,
    public.private_messages IN SHARE ROW EXCLUSIVE MODE;

CREATE TEMP TABLE star_gift_prepaid_viewer_sources ON COMMIT DROP AS
SELECT gift.id AS saved_gift_id,
       gift.owner_peer_id AS owner_user_id,
       gift.from_user_id AS sender_user_id,
       gift.msg_id AS owner_box_id,
       gift.gift_id,
       gift.prepaid_upgrade_hash,
       owner_box.message_sender_id,
       owner_box.private_message_id
FROM public.peer_star_gifts gift
JOIN public.message_boxes owner_box
  ON owner_box.owner_user_id = gift.owner_peer_id
 AND owner_box.box_id = gift.msg_id
 AND NOT owner_box.deleted
WHERE gift.owner_peer_type = 'user'
  AND gift.lifecycle_status = 'active'
  AND gift.unique_gift_id IS NULL
  AND gift.prepaid_upgrade_stars = 0
  AND gift.prepaid_upgrade_hash <> '';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.peer_star_gifts gift
        WHERE gift.owner_peer_type = 'user'
          AND gift.lifecycle_status = 'active'
          AND gift.unique_gift_id IS NULL
          AND gift.prepaid_upgrade_stars = 0
          AND gift.prepaid_upgrade_hash <> ''
          AND NOT EXISTS (
              SELECT 1
              FROM star_gift_prepaid_viewer_sources source
              WHERE source.saved_gift_id = gift.id
          )
    ) THEN
        RAISE EXCEPTION 'active user prepaid-upgrade entitlement is missing its owner message box';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM star_gift_prepaid_viewer_sources source
        JOIN public.message_boxes box
          ON box.message_sender_id = source.message_sender_id
         AND box.private_message_id = source.private_message_id
         AND NOT box.deleted
        WHERE box.owner_user_id NOT IN (source.owner_user_id, source.sender_user_id)
           OR box.media #>> '{service_action,kind}' IS DISTINCT FROM 'star_gift'
           OR box.media #>> '{service_action,star_gift,gift_id}' IS DISTINCT FROM source.gift_id::text
    ) THEN
        RAISE EXCEPTION 'ordinary star gift private projections disagree with the saved aggregate';
    END IF;
END
$$;

CREATE TEMP TABLE star_gift_prepaid_viewer_repairs (
    owner_user_id bigint NOT NULL,
    box_id integer NOT NULL,
    peer_type text NOT NULL,
    peer_id bigint NOT NULL,
    message_sender_id bigint NOT NULL,
    private_message_id bigint NOT NULL,
    repaired_media jsonb NOT NULL,
    PRIMARY KEY (owner_user_id, box_id)
) ON COMMIT DROP;

INSERT INTO star_gift_prepaid_viewer_repairs(
    owner_user_id, box_id, peer_type, peer_id,
    message_sender_id, private_message_id, repaired_media
)
SELECT box.owner_user_id,
       box.box_id,
       box.peer_type,
       box.peer_id,
       box.message_sender_id,
       box.private_message_id,
       CASE
           WHEN box.owner_user_id = source.owner_user_id THEN
               box.media #- '{service_action,star_gift,prepaid_upgrade_hash}'
           ELSE
               box.media #- '{service_action,star_gift,can_upgrade}'
       END
FROM star_gift_prepaid_viewer_sources source
JOIN public.message_boxes box
  ON box.message_sender_id = source.message_sender_id
 AND box.private_message_id = source.private_message_id
 AND NOT box.deleted
WHERE box.owner_user_id IN (source.owner_user_id, source.sender_user_id)
  AND box.media IS DISTINCT FROM CASE
      WHEN box.owner_user_id = source.owner_user_id THEN
          box.media #- '{service_action,star_gift,prepaid_upgrade_hash}'
      ELSE
          box.media #- '{service_action,star_gift,can_upgrade}'
  END;

DO $$
DECLARE
    repair_row record;
    next_pts integer;
    event_date integer := EXTRACT(EPOCH FROM clock_timestamp())::integer;
BEGIN
    FOR repair_row IN
        SELECT owner_user_id, box_id, peer_type, peer_id, repaired_media
        FROM star_gift_prepaid_viewer_repairs
        ORDER BY owner_user_id, box_id
    LOOP
        INSERT INTO public.user_update_watermarks(user_id, contiguous_pts)
        VALUES(repair_row.owner_user_id, 0)
        ON CONFLICT(user_id) DO NOTHING;

        UPDATE public.user_update_watermarks
        SET contiguous_pts = contiguous_pts + 1,
            updated_at = now()
        WHERE user_id = repair_row.owner_user_id
        RETURNING contiguous_pts INTO next_pts;

        UPDATE public.message_boxes
        SET media = repair_row.repaired_media,
            pts = next_pts
        WHERE owner_user_id = repair_row.owner_user_id
          AND box_id = repair_row.box_id
          AND NOT deleted;

        INSERT INTO public.user_update_events(
            user_id, pts, pts_count, date, event_type,
            message_box_id, peer_type, peer_id
        ) VALUES (
            repair_row.owner_user_id, next_pts, 1, event_date, 'edit_message',
            repair_row.box_id, repair_row.peer_type, repair_row.peer_id
        );

        INSERT INTO public.dispatch_outbox(
            target_user_id, pts, event_type,
            exclude_auth_key_id, exclude_session_id
        ) VALUES(repair_row.owner_user_id, next_pts, 'edit_message', 0, 0);
    END LOOP;
END
$$;

-- A shared private-message envelope is not a viewer projection. Keep neither
-- the owner-only nor non-owner-only capability there; both durable boxes above
-- remain authoritative for replay and history.
UPDATE public.private_messages private_message
SET media = private_message.media
        #- '{service_action,star_gift,prepaid_upgrade_hash}'
        #- '{service_action,star_gift,can_upgrade}'
FROM star_gift_prepaid_viewer_sources source
WHERE private_message.sender_user_id = source.message_sender_id
  AND private_message.id = source.private_message_id;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM star_gift_prepaid_viewer_sources source
        JOIN public.message_boxes box
          ON box.message_sender_id = source.message_sender_id
         AND box.private_message_id = source.private_message_id
         AND NOT box.deleted
        WHERE (box.owner_user_id = source.owner_user_id AND
               box.media #> '{service_action,star_gift,prepaid_upgrade_hash}' IS NOT NULL)
           OR (box.owner_user_id = source.sender_user_id AND
               box.owner_user_id <> source.owner_user_id AND
               box.media #> '{service_action,star_gift,can_upgrade}' IS NOT NULL)
    ) THEN
        RAISE EXCEPTION 'star gift prepaid viewer projection repair did not converge';
    END IF;
END
$$;
