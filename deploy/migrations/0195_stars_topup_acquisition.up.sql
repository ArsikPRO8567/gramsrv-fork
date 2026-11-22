-- Раунд 31: канал приобретения пополнения звёзд (acquisition) в purpose_json.
--
-- Официальные клиенты локализуют начисление звёзд по типу peer в
-- starsTransaction: StarsTransactionPeerFragment («Покупка на Fragment»),
-- StarsTransactionPeerAppStore / PlayMarket («Покупка в приложении») и
-- StarsTransactionPeerPremiumBot («Бот для покупки звёзд»). Канал выбирается
-- по платформе сессии покупателя (iOS -> App Store, Android -> Google Play,
-- desktop/web -> Fragment) и персистится в purpose_json формы и команды,
-- чтобы settlement записал транзакцию с reason выбранного канала
-- (topup_appstore / topup_playmarket / topup_bot; Fragment — исторический
-- reason topup с пустым purpose_json '{}').
--
-- Миграция только ослабляет shape-констрейнты: исторические строки
-- (purpose_json = '{}') проходят новый констрейнт без изменений.

ALTER TABLE public.stars_purchase_forms
    DROP CONSTRAINT stars_purchase_forms_shape_check;
ALTER TABLE public.stars_purchase_forms
    ADD CONSTRAINT stars_purchase_forms_shape_check CHECK (
        kind IN ('topup', 'gift', 'giveaway') AND buyer_user_id > 0 AND form_id <> 0 AND
        ((kind IN ('topup', 'giveaway') AND recipient_user_id IS NULL) OR
         (kind = 'gift' AND recipient_user_id > 0 AND buyer_user_id <> recipient_user_id)) AND
        ((spend_peer_type IS NULL AND spend_peer_id IS NULL) OR
         (kind = 'topup' AND spend_peer_type IN ('user', 'channel') AND spend_peer_id > 0)) AND
        ((kind = 'topup' AND jsonb_typeof(purpose_json) = 'object' AND
          (purpose_json = '{}'::jsonb OR
           (purpose_json ? 'acquisition' AND jsonb_typeof(purpose_json->'acquisition') = 'string' AND
            purpose_json->>'acquisition' IN ('appstore', 'playmarket', 'bot')))) OR
         (kind = 'gift' AND purpose_json = '{}'::jsonb) OR
         (kind = 'giveaway' AND jsonb_typeof(purpose_json) = 'object' AND purpose_json <> '{}'::jsonb)) AND
        stars > 0 AND amount > 0 AND char_length(currency) = 3 AND
        currency = upper(currency) AND issued_at > 0 AND
        expires_at = issued_at + 600);

ALTER TABLE public.stars_purchase_commands
    DROP CONSTRAINT stars_purchase_commands_shape_check;
ALTER TABLE public.stars_purchase_commands
    ADD CONSTRAINT stars_purchase_commands_shape_check CHECK (
        kind IN ('topup', 'gift', 'giveaway') AND buyer_user_id > 0 AND form_id <> 0 AND
        ((kind IN ('topup', 'giveaway') AND recipient_user_id IS NULL) OR
         (kind = 'gift' AND recipient_user_id > 0 AND buyer_user_id <> recipient_user_id)) AND
        ((spend_peer_type IS NULL AND spend_peer_id IS NULL) OR
         (kind = 'topup' AND spend_peer_type IN ('user', 'channel') AND spend_peer_id > 0)) AND
        ((kind = 'topup' AND jsonb_typeof(purpose_json) = 'object' AND
          (purpose_json = '{}'::jsonb OR
           (purpose_json ? 'acquisition' AND jsonb_typeof(purpose_json->'acquisition') = 'string' AND
            purpose_json->>'acquisition' IN ('appstore', 'playmarket', 'bot')))) OR
         (kind = 'gift' AND purpose_json = '{}'::jsonb) OR
         (kind = 'giveaway' AND jsonb_typeof(purpose_json) = 'object' AND purpose_json <> '{}'::jsonb)) AND
        octet_length(request_fingerprint) = 32 AND stars > 0 AND amount > 0 AND
        char_length(currency) = 3 AND balance_after >= 0 AND
        transaction_id <> '' AND created_at > 0);
