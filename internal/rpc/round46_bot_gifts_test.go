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

// Раунд 46: подарки от неслужебных ботов теряют конвертацию в звёзды,
// служебные (support) боты и люди конвертируют как раньше.
func TestRound46BotGiftConversionLockedForNonServiceBots(t *testing.T) {
        ctx := context.Background()
        users := memory.NewUserStore()
        dialogStore := memory.NewDialogStore()
        bot, err := users.Create(ctx, domain.User{AccessHash: 9101, Phone: "15550009101", FirstName: "Gift Bot", Bot: true})
        if err != nil {
                t.Fatal(err)
        }
        serviceBot, err := users.Create(ctx, domain.User{AccessHash: 9102, Phone: "15550009102", FirstName: "Service Bot", Bot: true, Support: true})
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

        // Служебный бот сохраняет конвертацию.
        send, err = r.deliverStarGift(WithUserID(ctx, serviceBot.ID), serviceBot.ID, recipient.ID, gift, false, "", nil, 0, "")
        if err != nil {
                t.Fatalf("service bot deliverStarGift: %v", err)
        }
        serviceAction := round46GiftActionFrom(send.RecipientMessage.Media)
        if serviceAction == nil || serviceAction.ConvertStars != 50 {
                t.Fatalf("service bot gift convert_stars = %+v, want 50", serviceAction)
        }

        // Люди не ограничены.
        if locked := r.starGiftSenderConvertLocked(ctx, human.ID); locked {
                t.Fatalf("human convert lock = %v, want false", locked)
        }
}

// Раунд 46: механика знакомства бота — незнакомый бот получает
// PEER_ID_INVALID, знакомый (dialog в любом направлении) проходит,
// служебные боты и люди не ограничены.
func TestRound46BotGiftAcquaintance(t *testing.T) {
        ctx := context.Background()
        users := memory.NewUserStore()
        dialogStore := memory.NewDialogStore()
        bot, err := users.Create(ctx, domain.User{AccessHash: 9201, Phone: "15550009201", FirstName: "Stranger Bot", Bot: true})
        if err != nil {
                t.Fatal(err)
        }
        serviceBot, err := users.Create(ctx, domain.User{AccessHash: 9202, Phone: "15550009202", FirstName: "Service Bot", Bot: true, Support: true})
        if err != nil {
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
                Users:   appusers.NewService(users),
                Dialogs: appdialogs.NewService(dialogStore),
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

// Раунд 46 (задача ⑤): payments.getPremiumGiftCodeOptions доступен ботам,
// но отдает им только звёздные опции; валютный инвойс бота — BOT_METHOD_INVALID.
func TestRound46BotPremiumGiftOptionsStarsOnly(t *testing.T) {
        r, premium, users, human, recipient := premiumRPCTestRouter(t)
        bot, err := users.Create(context.Background(), domain.User{AccessHash: 9301, Phone: "15550009301", FirstName: "Premium Bot", Bot: true})
        if err != nil {
                t.Fatal(err)
        }

        // Бот получает только звёздные опции (клиент больше не уходит в
        // бесконечный перезапрос после пустого фильтра валют).
        botOptions, err := r.onPaymentsGetPremiumGiftCodeOptions(WithUserID(context.Background(), bot.ID), &tg.PaymentsGetPremiumGiftCodeOptionsRequest{})
        if err != nil {
                t.Fatalf("bot gift options: %v", err)
        }
        if len(botOptions) == 0 {
                t.Fatalf("bot gift options empty")
        }
        for _, option := range botOptions {
                if option.Currency != domain.PremiumCurrencyStars {
                        t.Fatalf("bot option currency = %s, want XTR", option.Currency)
                }
        }

        // Людям по-прежнему доступны обе валюты.
        humanOptions, err := r.onPaymentsGetPremiumGiftCodeOptions(WithUserID(context.Background(), human.ID), &tg.PaymentsGetPremiumGiftCodeOptionsRequest{})
        if err != nil {
                t.Fatalf("human gift options: %v", err)
        }
        if len(humanOptions) != len(botOptions)+1 {
                t.Fatalf("human options = %d, bot options = %d, want fiat+stars", len(humanOptions), len(botOptions))
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
