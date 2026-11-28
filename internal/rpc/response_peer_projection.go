package rpc

import (
        "context"

        "github.com/iamxvbaba/td/tg"
)

// Центральная проекция peer-объектов на границе ответа RPC
// (projectResponsePeerObjects, вызывается из DispatchAdmitted).
//
// Один факт о peer (коллекционные юзернеймы, значок кастомной верификации,
// право owner'а редактировать бота) разъезжается по десяткам ответов —
// users.getUsers, users.getFullUser, dialogs, history, contacts, chats — и
// клиент каждый раз целиком перезаписывает свой кэш пользователя/канала тем
// объектом, что пришёл последним. Если хоть один путь отдаёт объект без
// usernames/bot_verification_icon/bot_can_edit, значок или имя «мигает»:
// профиль открыт — есть, открыт чат — пропало, снова профиль — появилось.
// Локальные оверлеи (applyUsernamesToPeerObjects и т.п.) покрывали лишь часть
// точек, поэтому для устойчивости проекция теперь применяется ко ВСЕМ
// успешным RPC-ответам в одном месте, после диспетчеризации.
//
// Каждый слой оверлея идемпотентен и сам решает, читать ли реестр: nil-сервис
// или пустой набор peer'ов — тихий no-op, форма ответа не меняется.
//
// Сбор peer-слайсов выполняет общий collectResponsePeerSlices из
// peer_overlay_dispatch.go (глубокий обход с защитой от циклов): он является
// надмножеством прежнего поверхностного варианта, поэтому покрытие точек
// ответа не сужается, а повторное объявление одной и той же функции в пакете
// ломало сборку.

func (r *Router) projectResponsePeerObjects(ctx context.Context, result any) {
        if r == nil || result == nil {
                return
        }
        users, chats := collectResponsePeerSlices(result)
        if len(users)+len(chats) == 0 {
                return
        }
        r.applyUsernamesToPeerObjects(ctx, users, chats)
        r.applyBotVerificationIconsToPeerObjects(ctx, users, chats)
        if viewer, authorized := UserIDFrom(ctx); authorized && viewer != 0 {
                r.applyBotCanEditToResponseUsers(ctx, viewer, users)
        }
}

// applyBotCanEditToResponseUsers ставит bot_can_edit (flags2.1) на объекты
// ботов, которыми владеет зритель. DrKLO/TDesktop показывают «карандаш»
// редактирования бота только по этому биту; ставка выполняется на каждом
// ответе, содержащем пользователя-бота, чтобы кэш клиента не терял флаг между
// открытиями профиля и чата.
func (r *Router) applyBotCanEditToResponseUsers(ctx context.Context, viewerUserID int64, users []tg.UserClass) {
        if r.deps.Bots == nil {
                return
        }
        for _, item := range users {
                u, ok := item.(*tg.User)
                if !ok || u == nil || !u.Bot || u.ID == viewerUserID {
                        continue
                }
                owns, err := r.deps.Bots.OwnsBot(ctx, viewerUserID, u.ID)
                if err != nil {
                        continue
                }
                if owns {
                        u.SetBotCanEdit(true)
                }
        }
}
