package rpc

import (
        "context"
        "testing"
        "time"

        "github.com/iamxvbaba/td/tg"
        "github.com/iamxvbaba/td/tgerr"
        "go.uber.org/zap/zaptest"

        appdialogs "telesrv/internal/app/dialogs"
        appmessages "telesrv/internal/app/messages"
        appusers "telesrv/internal/app/users"
        "telesrv/internal/domain"
        "telesrv/internal/store/memory"
)

// round46GiftActionFrom достаёт messageActionStarGift из media доставленного
// сообщения (аналог postgres.privateStarGiftAction в тестах rpc).
func round46GiftActionFrom(media *domain.MessageMedia) *domain.MessageStarGiftAction {
        if media == nil || media.Kind != domain.MessageMediaKindService || media.ServiceAction == nil ||
                media.ServiceAction.Kind != domain.MessageServiceActionStarGift {
                return nil
        }
        return media.ServiceAction.StarGift
}

// Раунд 47 (задача ②): подарки от неслужебных ботов теряют конвертацию в звёзды;
// служебные — с флагом service_account (миграция 0200, админ-API
// set-service-account) — конвертируют как раньше. Флаг support на ограничение
// не влияет: бот-«поддержка» без service_account всё равно заблокирован.
func TestRound46BotGiftConversionLockedForNonServiceBots(t *testing.T) {
        ctx := context.Background()
        users := memory.NewUserStore()
        dialogStore := memory.NewDialogStore()
        bot, err := users.Create(ctx, domain.User{AccessHash: 9101, Phone: "15550009101", FirstName: "Gift Bot", Bot: true})
        if err != nil {
                t.Fatal(err)
        }
        serviceBot, err := users.Create(ctx, domain.User{AccessHash: 9102, Phone: "15550009102", FirstName: "Service Bot", Bot: true})
        if err != nil {
                t.Fatal(err)
        }
        if err := users.SetServiceAccount(ctx, serviceBot.ID, true); err != nil {
                t.Fatal(err)
        }
        supportBot, err := users.Create(ctx, domain.User{AccessHash: 9105, Phone: "15550009105", FirstName: "Support Bot", Bot: true, Support: true})
        if err != nil {
                t.Fatal(err)
        }
        human, err := users.Create(ctx, domain.User{AccessHash: 9103, Phone: "15550009103", FirstName: "Human"})
        if err != nil {
                t.Fatal(err)
        }
        recipient, err := users.Create(ctx, domain.User{AccessHash: 9104, Phone: "15550009104", FirstName: "Recipient"})
        if err != nil {
                t.Fatal(err)
        }
        r := New(Config{}, Deps{
                Users:    appusers.NewService(users),
                Dialogs:  appdialogs.NewService(dialogStore),
                Messages: appmessages.NewService(memory.NewMessageStore(dialogStore), dialogStore),
        }, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})
        gift := domain.StarGift{ID: 77, Stars: 100, ConvertStars: 50, Title: "Round46", RevisionID: 1}

        // Неслужебный бот: convert_stars снят и с сообщения, и из агрегата.
        send, err := r.deliverStarGift(WithUserID(ctx, bot.ID), bot.ID, recipient.ID, gift, false, "", nil, 0, "")
        if err != nil {
                t.Fatalf("bot deliverStarGift: %v", err)
        }
        botAction := round46GiftActionFrom(send.RecipientMessage.Media)
        if botAction == nil {
                t.Fatalf("bot deliver media = %+v", send.RecipientMessage.Media)
        }
        if botAction.ConvertStars != 0 {
                t.Fatalf("non-service bot gift convert_stars = %d, want 0", botAction.ConvertStars)
        }
        if locked := r.starGiftSenderConvertLocked(ctx, bot.ID); !locked {
                t.Fatalf("bot convert lock = %v, want true", locked)
        }

        // Служебный бот (service_account=true) сохраняет конвертацию.
        send, err = r.deliverStarGift(WithUserID(ctx, serviceBot.ID), serviceBot.ID, recipient.ID, gift, false, "", nil, 0, "")
        if err != nil {
                t.Fatalf("service bot deliverStarGift: %v", err)
        }
        serviceAction := round46GiftActionFrom(send.RecipientMessage.Media)
        if serviceAction == nil || serviceAction.ConvertStars != 50 {
                t.Fatalf("service bot gift convert_stars = %+v, want 50", serviceAction)
        }
        if locked := r.starGiftSenderConvertLocked(ctx, serviceBot.ID); locked {
                t.Fatalf("service bot convert lock = %v, want false", locked)
        }

        // Флаг support не влияет: бот-«поддержка» без service_account заблокирован.
        if locked := r.starGiftSenderConvertLocked(ctx, supportBot.ID); !locked {
                t.Fatalf("support bot convert lock = %v, want true", locked)
        }

        // Люди не ограничены.
        if locked := r.starGiftSenderConvertLocked(ctx, human.ID); locked {
                t.Fatalf("human convert lock = %v, want false", locked)
        }
}

// Раунд 47: механика знакомства бота — незнакомый бот получает
// PEER_ID_INVALID, знакомый (dialog в любом направлении) проходит,
// служебные аккаунты (service_account) и люди не ограничены.
func TestRound46BotGiftAcquaintance(t *testing.T) {
        ctx := context.Background()
        users := memory.NewUserStore()
        dialogStore := memory.NewDialogStore()
        bot, err := users.Create(ctx, domain.User{AccessHash: 9201, Phone: "15550009201", FirstName: "Stranger Bot", Bot: true})
        if err != nil {
                t.Fatal(err)
        }
        serviceBot, err := users.Create(ctx, domain.User{AccessHash: 9202, Phone: "15550009202", FirstName: "Service Bot", Bot: true})
        if err != nil {
                t.Fatal(err)
        }
        if err := users.SetServiceAccount(ctx, serviceBot.ID, true); err != nil {
                t.Fatal(err)
        }
        human, err := users.Create(ctx, domain.User{AccessHash: 9203, Phone: "15550009203", FirstName: "Human"})
        if err != nil {
                t.Fatal(err)
        }
        recipient, err := users.Create(ctx, domain.User{AccessHash: 9204, Phone: "15550009204", FirstName: "Recipient"})
        if err != nil {
                t.Fatal(err)
        }
        r := New(Config{}, Deps{
                Users:          appusers.NewService(users),
                Dialogs:        appdialogs.NewService(dialogStore),
                GiftActorRules: users,
        }, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})
        target := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}

        // Незнакомый бот — PEER_ID_INVALID.
        err = r.ensureBotGiftAcquaintance(ctx, bot.ID, target)
        if err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
                t.Fatalf("stranger bot err = %v, want PEER_ID_INVALID", err)
        }

        // Пользователь запустил бота (диалог на стороне пользователя) — знакомы.
        if err := dialogStore.Upsert(ctx, recipient.ID, domain.Dialog{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: bot.ID}}); err != nil {
                t.Fatal(err)
        }
        if err := r.ensureBotGiftAcquaintance(ctx, bot.ID, target); err != nil {
                t.Fatalf("user-started bot err = %v, want nil", err)
        }

        // Бот писал пользователю (диалог на стороне бота) — тоже знакомы.
        otherBot, err := users.Create(ctx, domain.User{AccessHash: 9205, Phone: "15550009205", FirstName: "Chatty Bot", Bot: true})
        if err != nil {
                t.Fatal(err)
        }
        if err := dialogStore.Upsert(ctx, otherBot.ID, domain.Dialog{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}}); err != nil {
                t.Fatal(err)
        }
        if err := r.ensureBotGiftAcquaintance(ctx, otherBot.ID, target); err != nil {
                t.Fatalf("bot-wrote-first err = %v, want nil", err)
        }

        // Служебный бот и человек не ограничены даже без диалогов.
        if err := r.ensureBotGiftAcquaintance(ctx, serviceBot.ID, target); err != nil {
                t.Fatalf("service bot err = %v, want nil", err)
        }
        if err := r.ensureBotGiftAcquaintance(ctx, human.ID, target); err != nil {
                t.Fatalf("human err = %v, want nil", err)
        }
}

// Раунд 47 (задача ③): payments.getPremiumGiftCodeOptions отдаёт ботам полный
// каталог (fiat + звёздные опции) — клиент GiftSheet строит тарифы из fiat-опций
// и уходит в StackOverflow на списке из одних XTR. Валютный инвойс бота по-прежнему
// BOT_METHOD_INVALID; звёздный разрешён.
func TestRound46BotPremiumGiftOptionsStarsOnly(t *testing.T) {
        r, premium, users, human, recipient := premiumRPCTestRouter(t)
        bot, err := users.Create(context.Background(), domain.User{AccessHash: 9301, Phone: "15550009301", FirstName: "Premium Bot", Bot: true})
        if err != nil {
                t.Fatal(err)
        }

        // Бот получает тот же каталог fiat+звёзды, что и человек (без него
        // GiftSheet не может построить тарифы и циклится на кэше ответа).
        botOptions, err := r.onPaymentsGetPremiumGiftCodeOptions(WithUserID(context.Background(), bot.ID), &tg.PaymentsGetPremiumGiftCodeOptionsRequest{})
        if err != nil {
                t.Fatalf("bot gift options: %v", err)
        }
        if len(botOptions) == 0 {
                t.Fatalf("bot gift options empty")
        }
        fiatCount := 0
        for _, option := range botOptions {
                if option.Currency != domain.PremiumCurrencyStars {
                        fiatCount++
                }
        }
        if fiatCount == 0 {
                t.Fatalf("bot gift options have no fiat options: %d options", len(botOptions))
        }

        // Людям доступен тот же каталог.
        humanOptions, err := r.onPaymentsGetPremiumGiftCodeOptions(WithUserID(context.Background(), human.ID), &tg.PaymentsGetPremiumGiftCodeOptionsRequest{})
        if err != nil {
                t.Fatalf("human gift options: %v", err)
        }
        if len(humanOptions) != len(botOptions) {
                t.Fatalf("human options = %d, bot options = %d, want identical catalogs", len(humanOptions), len(botOptions))
        }

        // Валютный инвойс от бота — BOT_METHOD_INVALID; звёздный разрешён.
        fiats := &tg.InputInvoicePremiumGiftCode{
                Option:  tg.PremiumGiftCodeOption{Users: 1, Months: 3, Currency: "USD", Amount: premium.plans[0].EffectiveFiatAmount()},
                Purpose: &tg.InputStorePaymentPremiumGiftCode{Currency: "USD", Amount: premium.plans[0].EffectiveFiatAmount(),
                        Users: []tg.InputUserClass{&tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}}},
        }
        if _, err := r.resolvePremiumInvoice(WithUserID(context.Background(), bot.ID), bot.ID, fiats); err == nil || !tgerr.Is(err, "BOT_METHOD_INVALID") {
                t.Fatalf("bot fiat invoice err = %v, want BOT_METHOD_INVALID", err)
        }
        stars := &tg.InputInvoicePremiumGiftStars{UserID: &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, Months: 3}
        if _, err := r.resolvePremiumInvoice(WithUserID(context.Background(), bot.ID), bot.ID, stars); err != nil {
                t.Fatalf("bot stars invoice err = %v, want nil", err)
        }
}
