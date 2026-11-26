package rpc

import (
        "context"

        "telesrv/internal/domain"
)

// Раунд 46 (задача ④): механика знакомства бота. Бот может совершать подарочные
// операции в адрес пользователя только если между ними есть чат: бот отправлял
// пользователю что-то, или пользователь запускал/писал боту (dialog в любом из
// двух направлений). Иначе операции возвращают документированную ошибку
// PEER_ID_INVALID — официальные клиенты показывают «пользователь не найден»,
// не раскрывая механику знакомства. Служебные аккаунты (users.support) и
// системные отправители (777000 и партнёры) освобождены от ограничения.
//
// Ошибки чтения (нет deps.Dialogs/деps.Users в изолированных тестовых
// роутерах, сбой кэша) трактуются как «ограничение не применимо»: механика
// не должна ломать подарочные операции при частичной деградации.

// ensureBotGiftAcquaintance проверяет «знакомство» отправителя-бота с
// получателем-пользователем. Все подарочные операции с прямым
// пользовательским адресатом вызывают её до списания звёзд/выдачи формы.
func (r *Router) ensureBotGiftAcquaintance(ctx context.Context, senderUserID int64, recipient domain.Peer) error {
        if recipient.Type != domain.PeerTypeUser || recipient.ID <= 0 {
                return nil
        }
        if senderUserID <= 0 || senderUserID == recipient.ID {
                return nil
        }
        if domain.IsSystemUserID(senderUserID) {
                return nil
        }
        if r.deps.Users == nil {
                return nil
        }
        sender, found, err := r.deps.Users.ByID(ctx, senderUserID, senderUserID)
        if err != nil || !found {
                return nil
        }
        // Ограничение действует только на ботов; служебные аккаунты
        // (users.service_account, миграция 0200 — флаг админ-API
        // set-service-account, а не support) свободно дарят.
        if !sender.Bot || r.actorIsServiceAccount(ctx, r.deps.GiftActorRules, senderUserID) {
                return nil
        }
        recipientUser, found, err := r.deps.Users.ByID(ctx, senderUserID, recipient.ID)
        if err != nil {
                return internalErr()
        }
        // Несуществующий/удалённый адресат отдаётся стандартной ошибкой на
        // предыдущих проверках; здесь не мешаем.
        if !found || recipientUser.Deleted {
                return nil
        }
        dialogs, err := r.botGiftAcquaintanceDialogs(ctx, senderUserID, recipient.ID)
        if err != nil {
                return internalErr()
        }
        if dialogs {
                return nil
        }
        return peerIDInvalidErr()
}

// botGiftAcquaintanceDialogs сообщает, есть ли приватный чат между ботом и
// пользователем в любом направлении. HasUserDialog возвращает точный факт
// существования строки диалога (без фиктивных заглушек GetPeerDialogs), поэтому
// отсутствие в обоих направлениях значит, что бот и пользователь ни разу не
// общались.
func (r *Router) botGiftAcquaintanceDialogs(ctx context.Context, botUserID, userUserID int64) (bool, error) {
        if r.deps.Dialogs == nil {
                return true, nil
        }
        botKnows, err := r.deps.Dialogs.HasUserDialog(ctx, botUserID, userUserID)
        if err != nil {
                return false, err
        }
        if botKnows {
                return true, nil
        }
        return r.deps.Dialogs.HasUserDialog(ctx, userUserID, botUserID)
}
