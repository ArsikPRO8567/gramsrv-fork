-- Round 23: channel gift notifications learn action kinds. One gift aggregate
-- now produces several independent owner notifications: the original
-- purchase/auction delivery ('star_gift'), a unique gift placed onto the
-- channel ('star_gift_unique', official dump 198247) and an upgrade of a
-- channel-owned gift ('star_gift_unique_upgrade', official dump 198254).
-- The kind joins the primary key so events do not overwrite each other;
-- existing rows keep working as 'star_gift'.
ALTER TABLE star_gift_channel_notification_jobs
    ADD COLUMN action_kind text NOT NULL DEFAULT 'star_gift';

ALTER TABLE star_gift_channel_notification_jobs
    DROP CONSTRAINT star_gift_channel_notification_jobs_pkey;

ALTER TABLE star_gift_channel_notification_jobs
    ADD PRIMARY KEY (saved_gift_id, target_user_id, action_kind);

ALTER TABLE star_gift_channel_notification_jobs
    ADD CONSTRAINT star_gift_channel_notification_jobs_kind_check CHECK (
        action_kind IN ('star_gift', 'star_gift_unique', 'star_gift_unique_upgrade')
    );
