-- Round 23 rollback: collapse channel gift notification jobs back to the
-- single-kind primary key. Kinds other than the legacy purchase/auction kind
-- had no pre-round-23 producers, so removing them keeps every legacy row.
ALTER TABLE star_gift_channel_notification_jobs
    DROP CONSTRAINT IF EXISTS star_gift_channel_notification_jobs_kind_check;

ALTER TABLE star_gift_channel_notification_jobs
    DROP CONSTRAINT star_gift_channel_notification_jobs_pkey;

DELETE FROM star_gift_channel_notification_jobs
WHERE action_kind <> 'star_gift';

ALTER TABLE star_gift_channel_notification_jobs
    DROP COLUMN action_kind;

ALTER TABLE star_gift_channel_notification_jobs
    ADD PRIMARY KEY (saved_gift_id, target_user_id);
