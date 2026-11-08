package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/app/messages"
	appstargifts "telesrv/internal/app/stargifts"
	"telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// ---------------------------------------------------------------------------
// Раунд 28, проблема 1: подарок несуществующему пользователю.
// ---------------------------------------------------------------------------

// Отправка подарка несуществующему пользователю должна отклоняться
// документированной ошибкой payments.sendStarsForm («USER_ID_INVALID: The
// provided user ID is invalid»), а не INTERNAL_SERVER_ERROR от упавшей
// доставки сервисного сообщения. Проверка стоит до выдачи формы и до списания.
func TestStarGiftToUnknownRecipientReturnsUserIDInvalid(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)

	const ghostUserID int64 = 990_000_001
	unknown := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: ghostUserID, AccessHash: 1}, GiftID: gift.ID}

	if _, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: unknown}); !tgerr.Is(err, "USER_ID_INVALID") {
		t.Fatalf("unknown recipient form err = %v, want USER_ID_INVALID", err)
	}
	if _, err := r.onPaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: 4242, Invoice: unknown}); !tgerr.Is(err, "USER_ID_INVALID") {
		t.Fatalf("unknown recipient send err = %v, want USER_ID_INVALID", err)
	}

	// Существующий получатель по-прежнему проходит проверку и получает форму.
	live := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, GiftID: gift.ID}
	if _, err := r.onPaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: live}); err != nil {
		t.Fatalf("live recipient form err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Раунд 28, проблема 3: у подарка с предоплаченным улучшением в профиле нет
// convert_stars → клиент не рисует кнопку «Обменять на N звёзд».
// ---------------------------------------------------------------------------

func TestSavedStarGiftProfileConvertStarsMatrix(t *testing.T) {
	gift := domain.StarGift{ID: 8001, RevisionID: 9001, Stars: 50, ConvertStars: 25, Title: "Cake"}
	catalog := map[int64]domain.StarGift{gift.RevisionID: gift}
	availability := map[int64]domain.StarGiftCollectibleAvailability{gift.ID: {UpgradeStars: 25, SupplyTotal: 10}}

	prepaid := domain.SavedStarGift{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 42}, GiftID: gift.ID, RevisionID: gift.RevisionID,
		MsgID: 7, Date: 100, ConvertStars: 25, PrepaidUpgradeStars: 100,
	}
	projected := tgSavedStarGifts(42, []domain.SavedStarGift{prepaid}, catalog, availability)[0]
	if _, ok := projected.GetConvertStars(); ok {
		t.Fatalf("prepaid gift exposes convert_stars=%d, want unset", projected.ConvertStars)
	}
	if !projected.CanUpgrade {
		t.Fatal("prepaid gift lost can_upgrade")
	}
	if stars, ok := projected.GetUpgradeStars(); !ok || stars != 100 {
		t.Fatalf("prepaid gift upgrade_stars = %d set=%v, want 100/true", stars, ok)
	}

	regular := domain.SavedStarGift{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 42}, GiftID: gift.ID, RevisionID: gift.RevisionID,
		MsgID: 8, Date: 100, ConvertStars: 25,
	}
	projected = tgSavedStarGifts(42, []domain.SavedStarGift{regular}, catalog, availability)[0]
	if stars, ok := projected.GetConvertStars(); !ok || stars != 25 {
		t.Fatalf("regular gift convert_stars = %d set=%v, want 25/true", stars, ok)
	}
}

// ---------------------------------------------------------------------------
// Раунд 28, проблема 2: users-вектор подарочных страниц содержит peer'ы из
// starGiftAttributeOriginalDetails уникальных подарков.
// ---------------------------------------------------------------------------

func TestSavedStarGiftUserIDsIncludeOriginalDetailsPeers(t *testing.T) {
	unique := &domain.UniqueStarGift{
		ID: 9200000000000001, GiftID: 8001, KeepOriginalDetails: true,
		OriginalFromUserID: 777, OriginalOwner: domain.Peer{Type: domain.PeerTypeUser, ID: 888},
	}
	gifts := []domain.SavedStarGift{{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 42}, FromUserID: 555, GiftID: 8001, RevisionID: 9001,
		UniqueGiftID: unique.ID, Unique: unique,
	}}

	ids := savedStarGiftUserIDs(42, gifts)
	got := map[int64]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got[555] || !got[777] || !got[888] {
		t.Fatalf("savedStarGiftUserIDs = %v, want wrapper sender 555 + original sender 777 + original recipient 888", ids)
	}

	hidden := *unique
	hidden.OriginalNameHidden = true
	gifts[0].Unique = &hidden
	ids = savedStarGiftUserIDs(42, gifts)
	got = map[int64]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if got[777] {
		t.Fatalf("hidden-name original sender leaked into users ids: %v", ids)
	}
	if !got[555] || !got[888] {
		t.Fatalf("savedStarGiftUserIDs = %v, want 555 and 888", ids)
	}
}

// ---------------------------------------------------------------------------
// PG-интеграция: пол листинга — только серверный лимит из конфига; каталоговый
// resell_min_stars остаётся чисто отображаемой меткой; users-векторы сетки
// маркета и профиля перепроданного подарка содержат original_details peer'ов.
// ---------------------------------------------------------------------------

func round28TestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run postgres integration test")
	}
	if err := postgres.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestStarGiftResaleListingFloorAndOriginalDetailsUsersPG(t *testing.T) {
	pool := round28TestPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	now := int(time.Now().Unix())

	usersStore := postgres.NewUserStore(pool)
	dialogs := postgres.NewDialogStore(pool)
	msgStore := postgres.NewMessageStore(pool)
	sender, err := usersStore.Create(ctx, domain.User{AccessHash: 7201, Phone: "+1555" + suffix[len(suffix)-8:] + "01", FirstName: "Sender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	owner, err := usersStore.Create(ctx, domain.User{AccessHash: 7202, Phone: "+1555" + suffix[len(suffix)-8:] + "02", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	buyer, err := usersStore.Create(ctx, domain.User{AccessHash: 7203, Phone: "+1555" + suffix[len(suffix)-8:] + "03", FirstName: "Buyer"})
	if err != nil {
		t.Fatalf("create buyer: %v", err)
	}
	starsStore := postgres.NewStarsStore(pool)
	for _, u := range []domain.User{sender, owner, buyer} {
		if _, _, err := starsStore.EnsureGrant(ctx, u.ID, 1_000_000, now); err != nil {
			t.Fatalf("grant stars to %d: %v", u.ID, err)
		}
	}

	gifts := postgres.NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Round28 " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		ResellMinStars: 500,
		Document:       round28TestDocument(baseDocumentID, "round28.tgs"),
		Blob:           *round28TestBlob(baseDocumentID),
		Animation:      *round28TestAnimationPtr("round28.tgs"),
		Actor:          "round28", CommandID: "round28-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 20, SlugPrefix: "r28-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: round28TestDocumentPtr(baseDocumentID+1, "model.tgs"), Blob: round28TestBlob(baseDocumentID + 1), Animation: round28TestAnimationPtr("model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: round28TestDocumentPtr(baseDocumentID+2, "model-two.tgs"), Blob: round28TestBlob(baseDocumentID + 2), Animation: round28TestAnimationPtr("model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: round28TestPatternDocumentPtr(baseDocumentID+3, "pattern.tgs"), Blob: round28TestBlob(baseDocumentID + 3), Animation: round28TestAnimationPtr("pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: round28TestPatternDocumentPtr(baseDocumentID+4, "pattern-two.tgs"), Blob: round28TestBlob(baseDocumentID + 4), Animation: round28TestAnimationPtr("pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 91, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 92, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "round28", CommandID: "round28-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish collectible pool: %v", err)
	}

	lifecycle := postgres.NewStarGiftLifecycleStore(pool, msgStore, 1_000_000,
		postgres.WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{StarsProceedsPermille: 900, TONProceedsPermille: 900}),
		// Конфиг-лимит (cfg.StarGiftResellMinStars): пол листинга 10 звёзд —
		// намного ниже каталоговой метки resell_min_stars=500.
		postgres.WithStarGiftPriceLimits(domain.StarGiftPriceLimits{StarsMin: 10, StarsMax: 35000}))
	upgrades := postgres.NewStarGiftUpgradeStore(pool, msgStore, postgres.WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
	}))

	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	form, err := lifecycle.IssueStarGiftPurchaseForm(ctx, domain.StarGiftPurchaseForm{
		BuyerUserID: sender.ID, To: ownerPeer, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Message: "round28 gift", ChargeStars: entry.Gift.Stars, IssuedAt: now, ExpiresAt: now + 600,
	})
	if err != nil {
		t.Fatalf("issue purchase form: %v", err)
	}
	purchased, err := lifecycle.PurchaseStarGift(ctx, domain.StarGiftPurchaseRequest{
		BuyerUserID: sender.ID, To: ownerPeer, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		ChargeStars: entry.Gift.Stars, FormID: form.FormID,
		CommandKey: "round28-purchase-" + suffix, Date: now, Message: "round28 gift",
	})
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{UserID: owner.ID,
		Ref:         domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: purchased.Saved.MsgID},
		ChargeStars: 100, FormID: 77001, KeepOriginalDetails: true,
		CommandKey: "round28-upgrade-" + suffix, Date: now + 5})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if !upgraded.Unique.KeepOriginalDetails || upgraded.Unique.OriginalFromUserID != sender.ID {
		t.Fatalf("upgrade original details = keep:%v from:%d", upgraded.Unique.KeepOriginalDetails, upgraded.Unique.OriginalFromUserID)
	}

	// Пол листинга — ТОЛЬКО конфиг (10): цена 100 ниже каталоговой метки 500,
	// но выше конфига → листинг принимается (STARGIFT_INVALID больше не возникает).
	uniqueRef := domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: upgraded.Saved.UpgradeMsgID}
	if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: owner.ID, Ref: uniqueRef, Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 100},
		Date: now + 10,
	}); err != nil {
		t.Fatalf("listing below catalog resell_min_stars (100 < 500) rejected: %v", err)
	}
	// Ниже конфиг-пола → документированная RESELL_STARS_TOO_FEW.
	if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: owner.ID, Ref: uniqueRef, Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 5},
		Date: now + 11,
	}); !errors.Is(err, domain.ErrStarGiftResalePriceTooLow) {
		t.Fatalf("listing below config floor err = %v, want ErrStarGiftResalePriceTooLow", err)
	}

	// Полный стек RPC: сетка маркета и профиль перепроданного подарка.
	giftsService := appstargifts.NewService(gifts, nil, 2,
		appstargifts.WithLifecycleStore(lifecycle),
		appstargifts.WithUpgradeStore(upgrades))
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398, PublicBaseURL: "https://links.example.test"}, Deps{
		Users:    appusers.NewService(usersStore),
		Messages: messages.NewService(msgStore, dialogs),
		Stars:    stars.NewService(starsStore, stars.WithStartingGrant(0)),
		Gifts:    giftsService,
	}, zaptest.NewLogger(t), clock.System)

	resale, err := r.onPaymentsGetResaleStarGifts(WithUserID(ctx, buyer.ID), &tg.PaymentsGetResaleStarGiftsRequest{GiftID: entry.Gift.ID, Limit: 10})
	if err != nil {
		t.Fatalf("get resale: %v", err)
	}
	if len(resale.Gifts) == 0 {
		t.Fatal("resale grid is empty")
	}
	resaleUsers := map[int64]bool{}
	for _, u := range resale.Users {
		if uu, ok := u.(*tg.User); ok {
			resaleUsers[uu.ID] = true
		}
	}
	resaleReferenced := map[int64]string{}
	for _, giftClass := range resale.Gifts {
		unique, ok := giftClass.(*tg.StarGiftUnique)
		if !ok {
			continue
		}
		if user, ok := unique.GetOwnerID(); ok {
			if peer, ok := user.(*tg.PeerUser); ok && peer != nil {
				resaleReferenced[peer.UserID] = "owner_id"
			}
		}
		for _, attribute := range unique.Attributes {
			details, ok := attribute.(*tg.StarGiftAttributeOriginalDetails)
			if !ok {
				continue
			}
			if user, ok := details.SenderID.(*tg.PeerUser); ok && user != nil {
				resaleReferenced[user.UserID] = "original_details.sender_id"
			}
			if user, ok := details.RecipientID.(*tg.PeerUser); ok && user != nil {
				resaleReferenced[user.UserID] = "original_details.recipient_id"
			}
		}
	}
	if len(resaleReferenced) == 0 {
		t.Fatal("resale grid references no peers, test setup broken")
	}
	for id, source := range resaleReferenced {
		if !resaleUsers[id] {
			t.Fatalf("resale grid users missing %s peer %d (have %v)", source, id, resaleUsers)
		}
	}

	// Подарок перепродан buyer'у: в его профиле original_details ссылается на
	// отправителя и получателя — users-вектор должен содержать обоих.
	if _, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: upgraded.Unique.Slug, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 100},
		FormID: 88001, CommandKey: "round28-resale-" + suffix, Date: now + 20,
	}); err != nil {
		t.Fatalf("resale purchase: %v", err)
	}
	profile, err := r.onPaymentsGetSavedStarGifts(WithUserID(ctx, sender.ID), &tg.PaymentsGetSavedStarGiftsRequest{
		Peer: &tg.InputPeerUser{UserID: buyer.ID, AccessHash: buyer.AccessHash},
	})
	if err != nil {
		t.Fatalf("get profile gifts: %v", err)
	}
	if len(profile.Gifts) == 0 {
		t.Fatal("buyer profile gifts is empty")
	}
	// Инвариант для клиентов: каждый peer, сериализованный в ответе
	// (обёртка savedStarGift.from_id и оригинальные детали уникального
	// подарка — sender_id/recipient_id), должен разрешаться через
	// users-вектор. Иначе сетка/шторка рисуют «Аноним» без иконки.
	profileUsers := map[int64]bool{}
	for _, u := range profile.Users {
		if uu, ok := u.(*tg.User); ok {
			profileUsers[uu.ID] = true
		}
	}
	referenced := map[int64]string{}
	for _, item := range profile.Gifts {
		if from, ok := item.GetFromID(); ok {
			if user, ok := from.(*tg.PeerUser); ok {
				referenced[user.UserID] = "savedStarGift.from_id"
			}
		}
		if unique, ok := item.Gift.(*tg.StarGiftUnique); ok {
			for _, attribute := range unique.Attributes {
				details, ok := attribute.(*tg.StarGiftAttributeOriginalDetails)
				if !ok {
					continue
				}
				if user, ok := details.SenderID.(*tg.PeerUser); ok && user != nil {
					referenced[user.UserID] = "original_details.sender_id"
				}
				if user, ok := details.RecipientID.(*tg.PeerUser); ok && user != nil {
					referenced[user.UserID] = "original_details.recipient_id"
				}
			}
		}
	}
	if len(referenced) == 0 {
		t.Fatal("profile gifts reference no peers, test setup broken")
	}
	for id, source := range referenced {
		if !profileUsers[id] {
			t.Fatalf("profile users missing %s peer %d (have %v)", source, id, profileUsers)
		}
	}
	// wire: обёртка savedStarGift несёт from_id продавца (bit 1).
	var raw bin.Buffer
	if err := profile.Gifts[0].Encode(&raw); err != nil {
		t.Fatalf("encode savedStarGift: %v", err)
	}
	flagsWord := int32(raw.Buf[8]) | int32(raw.Buf[9])<<8 | int32(raw.Buf[10])<<16 | int32(raw.Buf[11])<<24
	if flagsWord&2 == 0 {
		t.Fatalf("resold gift wrapper missing from_id bit: flags=%08x", flagsWord)
	}

	// Слоевая согласованность (228): ответ кодируется/декодируется без потерь.
	var wire bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile228, profile, &wire); err != nil {
		t.Fatalf("encode layer 228 response: %v", err)
	}
	decoded, err := tlprofile.DecodeObject(tlprofile.Profile228, &bin.Buffer{Buf: wire.Buf}, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("decode layer 228 response: %v", err)
	}
	if _, ok := decoded.(*tg.PaymentsSavedStarGifts); !ok {
		t.Fatalf("decode layer 228 response type = %T", decoded)
	}
}

func round28TestBlob(id int64) *domain.FileBlob {
	blob := round28TestBlobValue(id)
	return &blob
}

func round28TestBlobValue(id int64) domain.FileBlob {
	data := []byte("round28")
	sum := sha256.Sum256(data)
	return domain.FileBlob{
		LocationKey: fmt.Sprintf("doc:%d", id), Backend: domain.MediaBackendLocalFS,
		ObjectKey: hex.EncodeToString(sum[:]), Size: int64(len(data)),
		SHA256: append([]byte(nil), sum[:]...), MimeType: "application/x-tgsticker",
	}
}

func round28TestDocument(id int64, name string) domain.Document {
	return domain.Document{
		ID: id, AccessHash: id + 100, FileReference: []byte("round28"), Date: 1700001000,
		MimeType: "application/x-tgsticker", Size: 4, DCID: 2,
		Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrImageSize, W: 512, H: 512},
			{Kind: domain.DocAttrSticker, Alt: "🎁"},
			{Kind: domain.DocAttrFilename, FileName: name},
		},
	}
}

func round28TestDocumentPtr(id int64, name string) *domain.Document {
	document := round28TestDocument(id, name)
	return &document
}

func round28TestPatternDocumentPtr(id int64, name string) *domain.Document {
	document := round28TestDocument(id, name)
	document.Attributes[1] = domain.DocumentAttribute{Kind: domain.DocAttrCustomEmoji, Alt: "🎁", TextColor: true}
	document.Thumbs = []domain.PhotoSize{{Kind: domain.PhotoSizeKindPath, Type: "j", Bytes: []byte{1}}}
	return &document
}

func round28TestAnimationPtr(name string) *domain.StarGiftAnimation {
	return &domain.StarGiftAnimation{
		SourceName: name, SourceFormat: domain.StarGiftAnimationTGS,
		JSON: []byte(`{"v":"5.7","w":512,"h":512,"fr":30,"ip":0,"op":30,"layers":[{}]}`),
		TGS:  []byte("test"), SHA256: make([]byte, 32), Width: 512, Height: 512, FrameRate: 30, OutPoint: 30,
	}
}
