-- Откат 0195_stars_topup_acquisition: восстановление исходных shape-
-- констрейнтов (0166). Внимание: при наличии строк с каналом приобретения
-- (purpose_json с acquisition) пересоздание исходных констрейнтов потребует
-- предварительной очистки таких строк.

ALTER TABLE public.stars_purchase_commands
    DROP CONSTRAINT stars_purchase_commands_shape_check;
ALTER TABLE public.stars_purchase_commands
    ADD CONSTRAINT stars_purchase_commands_shape_check CHECK (
        kind IN ('topup', 'gift', 'giveaway') AND buyer_user_id > 0 AND form_id <> 0 AND
        ((kind IN ('topup', 'giveaway') AND recipient_user_id IS NULL) OR
         (kind = 'gift' AND recipient_user_id > 0 AND buyer_user_id <> recipient_user_id)) AND
        ((spend_peer_type IS NULL AND spend_peer_id IS NULL) OR
         (kind = 'topup' AND spend_peer_type IN ('user', 'channel') AND spend_peer_id > 0)) AND
        ((kind IN ('topup', 'gift') AND purpose_json = '{}'::jsonb) OR
         (kind = 'giveaway' AND jsonb_typeof(purpose_json) = 'object' AND purpose_json <> '{}'::jsonb)) AND
        octet_length(request_fingerprint) = 32 AND stars > 0 AND amount > 0 AND
        char_length(currency) = 3 AND balance_after >= 0 AND
        transaction_id <> '' AND created_at > 0);

ALTER TABLE public.stars_purchase_forms
    DROP CONSTRAINT stars_purchase_forms_shape_check;
ALTER TABLE public.stars_purchase_forms
    ADD CONSTRAINT stars_purchase_forms_shape_check CHECK (
        kind IN ('topup', 'gift', 'giveaway') AND buyer_user_id > 0 AND form_id <> 0 AND
        ((kind IN ('topup', 'giveaway') AND recipient_user_id IS NULL) OR
         (kind = 'gift' AND recipient_user_id > 0 AND buyer_user_id <> recipient_user_id)) AND
        ((spend_peer_type IS NULL AND spend_peer_id IS NULL) OR
         (kind = 'topup' AND spend_peer_type IN ('user', 'channel') AND spend_peer_id > 0)) AND
        ((kind IN ('topup', 'gift') AND purpose_json = '{}'::jsonb) OR
         (kind = 'giveaway' AND jsonb_typeof(purpose_json) = 'object' AND purpose_json <> '{}'::jsonb)) AND
        stars > 0 AND amount > 0 AND char_length(currency) = 3 AND
        currency = upper(currency) AND issued_at > 0 AND
        expires_at = issued_at + 600);
