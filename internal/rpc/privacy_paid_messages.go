package rpc

import (
	"context"
	"math"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

type privateContactRequirement struct {
	paidStars      int64
	requirePremium bool
}

type privacyContactFreeEvaluator interface {
	CanContactForFreeBatch(ctx context.Context, ownerUserIDs []int64, viewerUserID int64) (map[int64]bool, error)
}

type privacyViewerPremiumEvaluator interface {
	ViewerIsPremium(ctx context.Context, viewerUserID int64) (bool, error)
}

// starsPeerTransferService 是账本服务的可选扩展（问题 B/C）：账户对账户的
// 配对转账 — 付款方借记行与收款方贷记行共享同一个公开交易 ID。真实实现
// （app/stars 包 postgres + memory）都支持；轻量测试替身可以不实现，此时
// 付费消息保持 PAYMENT_UNSUPPORTED（无账本无法原子扣费）。
type starsPeerTransferService interface {
	TransferToUser(ctx context.Context, req domain.StarsPeerTransferRequest) (domain.StarsPeerTransferResult, error)
	RefundPeerTransfer(ctx context.Context, req domain.StarsPeerTransferRequest) (domain.StarsPeerTransferResult, error)
}

func applyPrivateContactRestrictionToUser(user *tg.User, restriction privateContactRequirement) {
	if user == nil {
		return
	}
	user.SetContactRequirePremium(restriction.requirePremium)
	if restriction.paidStars > 0 {
		user.SetSendPaidMessagesStars(restriction.paidStars)
	} else {
		user.Flags2.Unset(15)
		user.SendPaidMessagesStars = 0
	}
}

func applyPrivateContactRestrictionToUserFull(full *tg.UserFull, restriction privateContactRequirement) {
	if full == nil {
		return
	}
	full.SetContactRequirePremium(restriction.requirePremium)
	if restriction.paidStars > 0 {
		full.SetSendPaidMessagesStars(restriction.paidStars)
	} else {
		full.Flags2.Unset(14)
		full.SendPaidMessagesStars = 0
	}
}

// privateContactRequirementFor returns the recipient's current restriction
// from two in-memory read models:
//   - account settings: base premium/paid requirement;
//   - privacy/contact facts: contacts and PrivacyKeyNoPaidMessages exceptions.
//
// PostgreSQL is only a bounded cache-miss loader. Local writes are
// write-through and cross-instance changes invalidate + prewarm both models.
func (r *Router) privateContactRestrictionFor(
	ctx context.Context,
	senderUserID, recipientUserID int64,
) (privateContactRequirement, error) {
	if r == nil || senderUserID == 0 || recipientUserID == 0 || senderUserID == recipientUserID {
		return privateContactRequirement{}, nil
	}
	if evaluator, ok := r.deps.Privacy.(privacyContactFreeEvaluator); ok {
		free, err := evaluator.CanContactForFreeBatch(ctx, []int64{recipientUserID}, senderUserID)
		if err != nil {
			return privateContactRequirement{}, internalErr()
		}
		if free[recipientUserID] {
			return privateContactRequirement{}, nil
		}
	}
	settings, err := r.cachedAccountSettings(ctx, recipientUserID)
	if err != nil {
		return privateContactRequirement{}, internalErr()
	}
	global := settings.GlobalPrivacy
	if global.NoncontactPeersPaidStars > 0 {
		return privateContactRequirement{paidStars: global.NoncontactPeersPaidStars}, nil
	}
	return privateContactRequirement{requirePremium: global.NewNoncontactPeersRequirePremium}, nil
}

func (r *Router) viewerIsPremiumForPrivacy(ctx context.Context, viewerUserID int64) (bool, error) {
	var err error
	premium := false
	if evaluator, ok := r.deps.Privacy.(privacyViewerPremiumEvaluator); ok {
		premium, err = evaluator.ViewerIsPremium(ctx, viewerUserID)
		if err != nil {
			return false, internalErr()
		}
	} else if r.deps.Users != nil {
		user, found, loadErr := r.deps.Users.ByID(ctx, viewerUserID, viewerUserID)
		if loadErr != nil {
			return false, internalErr()
		}
		premium = found && user.PremiumActiveAt(r.clock.Now().Unix())
	}
	return premium, nil
}

// privateContactPaidStarsFor решает, сколько Stars нужно заплатить за ОДНО
// сообщение получателю. Гейт официального клиента:
//  1. не-контакт с require_premium без Premium → PREMIUM_ACCOUNT_REQUIRED;
//  2. не-контакт с ценой paidStars: allow_paid_stars из запроса должен
//     покрывать paidStars × messageCount, иначе ALLOW_PAYMENT_REQUIRED(сумма) —
//     клиент показывает платёжную форму и повторяет отправку с авторизацией;
//  3. иначе возвращает цену ОДНОГО сообщения (0 = бесплатно).
//
// Раунд 37: сам платёж выполняет store в транзакции отправки
// (chargePrivatePaidMessageTx) — списание отправителю и зачисление
// получателю с одним публичным ID транзакции. Здесь PAYMENT_UNSUPPORTED
// больше не возвращается: проверка без списания и была причиной
// неработающих платных сообщений.
func (r *Router) privateContactPaidStarsFor(
	ctx context.Context,
	senderUserID, recipientUserID, allowPaidStars int64,
	messageCount int,
) (int64, error) {
	if allowPaidStars < 0 || messageCount < 1 {
		return 0, starsAmountInvalidErr()
	}
	requirement, err := r.privateContactRestrictionFor(ctx, senderUserID, recipientUserID)
	if err != nil {
		return 0, err
	}
	if requirement.requirePremium {
		premium, err := r.viewerIsPremiumForPrivacy(ctx, senderUserID)
		if err != nil {
			return 0, err
		}
		if !premium {
			return 0, premiumAccountRequiredErr()
		}
		return 0, nil
	}
	if requirement.paidStars <= 0 {
		return 0, nil
	}
	if requirement.paidStars > math.MaxInt64/int64(messageCount) {
		return 0, starsAmountInvalidErr()
	}
	required := requirement.paidStars * int64(messageCount)
	if allowPaidStars < required {
		return 0, allowPaymentRequiredErr(required)
	}
	return requirement.paidStars, nil
}
