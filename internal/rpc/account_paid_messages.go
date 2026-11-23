package rpc

import (
        "context"

        "github.com/iamxvbaba/td/tg"
        "go.uber.org/zap"

        "telesrv/internal/domain"
)

// Протокол paid-messages (https://core.telegram.org/api/paid-messages):
// приватные платные сообщения, исключения и баннер получателя платы.

// exemptPaidMessagesRecipient — «получатель платы пишет первым»: если
// ownerUserID включил у себя платные сообщения (noncontact_peers_paid_stars>0)
// и сам отправил сообщение recipientUserID, этот peer освобождается от платы —
// добавляется в исключения PrivacyKeyNoPaidMessages. Идемпотентно;
// best-effort: сбой не должен ломать отправку сообщения.
func (r *Router) exemptPaidMessagesRecipient(ctx context.Context, ownerUserID, recipientUserID int64) {
        if r == nil || r.deps.Privacy == nil || ownerUserID == 0 || recipientUserID == 0 || ownerUserID == recipientUserID {
                return
        }
        settings, err := r.cachedAccountSettings(ctx, ownerUserID)
        if err != nil || settings.GlobalPrivacy.NoncontactPeersPaidStars <= 0 {
                return
        }
        // Раунд 41: освобождаем только собеседника, с которым владелец платы
        // инициирует диалог. Если диалог начал платящий (писал первым до или
        // после включения платы), его плата сохраняется до явного «Не брать
        // плату» — ответ владельца платность не снимает.
        if initiated, probed := r.privateDialogInitiatedBySender(ctx, ownerUserID, recipientUserID); probed && !initiated {
                return
        }
        _, changed, err := r.deps.Privacy.AddAllowUser(ctx, ownerUserID, domain.PrivacyKeyNoPaidMessages, recipientUserID)
        if err != nil {
                r.log.Warn("paid messages auto-exemption failed",
                        zap.Int64("owner_user_id", ownerUserID),
                        zap.Int64("recipient_user_id", recipientUserID),
                        zap.Error(err))
                return
        }
        if changed {
                // Баннер вью владельца (peerSettings.charge_paid_message_stars) должен исчезнуть.
                r.invalidateRPCProjectionForViewer(ownerUserID)
        }
}

// paidMessagesPeerSettingsHint заполняет баннер получателя платы: во вью
// владельца (viewer) peer'а-плательщика флаг charge_paid_message_stars несёт
// ставку, когда peer реально платит (не контакт/не исключение).
func (r *Router) paidMessagesPeerSettingsHint(ctx context.Context, viewerUserID int64, peerUserID int64, settings domain.PeerSettings) domain.PeerSettings {
        if viewerUserID == 0 || peerUserID == 0 || viewerUserID == peerUserID || peerUserID < 0 {
                return settings
        }
        requirement, err := r.privateContactRestrictionFor(ctx, peerUserID, viewerUserID)
        if err != nil || requirement.paidStars <= 0 {
                return settings
        }
        settings.ChargePaidMessageStars = requirement.paidStars
        return settings
}

// pushPaidMessagesPeerSettingsRefresh проталкивает обновлённые peerSettings
// владельцу после смены исключения (баннер «Не брать плату» должен исчезнуть
// или появиться во всех его сессиях): событие записывается в update-state
// (offline-сессии заберут через getState) и пушится в текущую сессию.
func (r *Router) pushPaidMessagesPeerSettingsRefresh(ctx context.Context, ownerUserID, peerUserID int64) {
        if r.deps.Contacts == nil || r.deps.Users == nil {
                return
        }
        peer := domain.Peer{Type: domain.PeerTypeUser, ID: peerUserID}
        peerUser, found, err := r.deps.Users.ByID(ctx, ownerUserID, peerUserID)
        if err != nil || !found {
                return
        }
        settings := domain.PeerSettings{}
        if s, err := r.deps.Contacts.GetPeerSettings(ctx, ownerUserID, peer); err == nil {
                settings = s
        }
        settings = r.paidMessagesPeerSettingsHint(ctx, ownerUserID, peerUserID, settings)
        if err := r.recordPeerSettings(ctx, ownerUserID, peer, settings); err != nil {
                r.log.Warn("paid messages peer settings refresh record failed",
                        zap.Int64("owner_user_id", ownerUserID),
                        zap.Int64("peer_user_id", peerUserID),
                        zap.Error(err))
        }
        updates := r.contactPeerSettingsUpdates(ctx, ownerUserID, peerUser, settings, false)
        r.pushUserUpdatesIfNoReliableDispatch(ctx, ownerUserID, updates)
}

// onAccountToggleNoPaidMessagesException — account.toggleNoPaidMessagesException:
// добавить user_id в исключения платы отправителя (без require_payment).
// По протоколу require_payment доступен только моноканалам (monoforum), а
// parent_peer — только им же; приватный сценарий пользователей ограничен
// добавлением исключений. refund_charged дополнительно возвращает peer'у все
// ещё не refund'нутые звёзды, потраченные на сообщения нам.
func (r *Router) onAccountToggleNoPaidMessagesException(ctx context.Context, req *tg.AccountToggleNoPaidMessagesExceptionRequest) (bool, error) {
        if req == nil || req.UserID == nil {
                return false, inputRequestInvalidErr()
        }
        userID, _, err := r.currentUserID(ctx)
        if err != nil {
                return false, internalErr()
        }
        if req.RequirePayment {
                // Официальный протокол: пользователи снимают исключение через
                // account.setPrivacy (inputPrivacyKeyNoPaidMessages), не через этот метод.
                return false, privacyKeyInvalidErr()
        }
        if req.ParentPeer != nil {
                // Моноканальный сценарий (monoforum exceptions) пока не поддерживается.
                return false, peerIDInvalidErr()
        }
        target, found, err := r.userFromInput(ctx, userID, req.UserID)
        if err != nil {
                return false, internalErr()
        }
        if !found || target.ID == 0 || target.ID == userID {
                return false, userIDInvalidErr()
        }
        if req.RefundCharged && r.deps.Stars != nil {
                refund, err := r.deps.Stars.RefundPaidMessages(ctx, userID, target.ID)
                if err != nil {
                        return false, starsErr(err)
                }
                if refund.Refunded > 0 {
                        r.log.Info("paid messages refunded on exception toggle",
                                zap.Int64("owner_user_id", userID),
                                zap.Int64("payer_user_id", target.ID),
                                zap.Int64("refunded_stars", refund.Refunded),
                                zap.Int64("clawed_back_stars", refund.ClawedBack),
                                zap.Int("charges", refund.Charges))
                        // Раунд 41: официальный протокол paid-messages требует
                        // сервисное сообщение messageActionPaidMessagesRefunded в
                        // диалоге — «владелец платы вернул звёзды за сообщения».
                        r.sendPaidMessagesRefundedServiceMessage(ctx, userID, target.ID, refund)
                }
        }
        if r.deps.Privacy != nil {
                if _, _, err := r.deps.Privacy.AddAllowUser(ctx, userID, domain.PrivacyKeyNoPaidMessages, target.ID); err != nil {
                        return false, privacyErr(err)
                }
        }
        r.invalidateRPCProjectionForViewer(userID)
        r.pushPaidMessagesPeerSettingsRefresh(ctx, userID, target.ID)
        return true, nil
}

// sendPaidMessagesRefundedServiceMessage (раунд 41) — сервисное сообщение
// messageActionPaidMessagesRefunded в диалог владельца платы и плательщика:
// официальный протокол (core.telegram.org/api/paid-messages) отправляет
// подтверждение возврата звёзд в самом чате, от имени вернувшего (owner).
// Best-effort: возврат уже зафиксирован в леджере, сбой доставки не отменяет
// toggle и не ломает RPC.
func (r *Router) sendPaidMessagesRefundedServiceMessage(ctx context.Context, ownerUserID, payerUserID int64, refund domain.PaidMessagesRefund) {
        if r == nil || r.deps.Messages == nil || refund.Refunded <= 0 {
                return
        }
        recipientBlocked, err := r.peerBlocksUser(ctx, ownerUserID, payerUserID)
        if err != nil {
                r.log.Warn("paid messages refund service message block probe failed",
                        zap.Int64("owner_user_id", ownerUserID),
                        zap.Int64("payer_user_id", payerUserID),
                        zap.Error(err))
                recipientBlocked = false
        }
        media := &domain.MessageMedia{
                Kind: domain.MessageMediaKindService,
                ServiceAction: &domain.MessageServiceAction{
                        Kind:                 domain.MessageServiceActionPaidMessagesRefunded,
                        PaidMessagesRefunded: &domain.MessagePaidMessagesRefundedAction{Count: refund.Charges, Stars: refund.Refunded},
                },
        }
        sessionID, _ := SessionIDFrom(ctx)
        if _, err := r.deps.Messages.SendPrivateText(ctx, ownerUserID, domain.SendPrivateTextRequest{
                SenderUserID:     ownerUserID,
                RecipientUserID:  payerUserID,
                RandomID:         randomNonZeroInt64(),
                Media:            media,
                Date:             int(r.clock.Now().Unix()),
                OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
                OriginSessionID:  sessionID,
                RecipientBlocked: recipientBlocked,
        }); err != nil {
                r.log.Warn("paid messages refund service message delivery failed",
                        zap.Int64("owner_user_id", ownerUserID),
                        zap.Int64("payer_user_id", payerUserID),
                        zap.Error(err))
        }
}

// onAccountGetPaidMessagesRevenue — account.getPaidMessagesRevenue: сколько
// непогашенных звёзд user_id потратил на сообщения нам напрямую или через
// monoforum (parent_peer). Вызов с parent_peer требует прав управления каналом.
func (r *Router) onAccountGetPaidMessagesRevenue(ctx context.Context, req *tg.AccountGetPaidMessagesRevenueRequest) (*tg.AccountPaidMessagesRevenue, error) {
        if req == nil || req.UserID == nil {
                return nil, inputRequestInvalidErr()
        }
        userID, _, err := r.currentUserID(ctx)
        if err != nil {
                return nil, internalErr()
        }
        target, found, err := r.userFromInput(ctx, userID, req.UserID)
        if err != nil {
                return nil, internalErr()
        }
        if !found || target.ID == 0 {
                return nil, userIDInvalidErr()
        }
        peer := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
        if req.ParentPeer != nil {
                parent, ok := r.domainPeerFromInputPeer(userID, req.ParentPeer)
                if !ok || parent.Type != domain.PeerTypeChannel || parent.ID == 0 {
                        return nil, peerIDInvalidErr()
                }
                if r.deps.Channels != nil {
                        // Права управления каналом (администратор моноканала/родителя).
                        if _, err := r.deps.Channels.GetChannelForChangeInfo(ctx, userID, parent.ID); err != nil {
                                return nil, channelAdminErr(err)
                        }
                }
                peer = parent
        }
        if r.deps.Stars == nil {
                return &tg.AccountPaidMessagesRevenue{StarsAmount: 0}, nil
        }
        total, err := r.deps.Stars.PaidMessagesRevenue(ctx, target.ID, peer)
        if err != nil {
                return nil, starsErr(err)
        }
        return &tg.AccountPaidMessagesRevenue{StarsAmount: total}, nil
}
