package rpc

import (
	"context"
	"math"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

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

// ensurePrivateContactAllowed 校验私聊发送门槛（Premium-only / 付费消息），
// 返回发送方需要支付的每条消息星数（0 = 免费）。它不再扣费：扣费由
// chargePrivateMessage 在实际落库前原子完成（余额不足 → BALANCE_TOO_LOW），
// 发送失败路径调用 refundPrivateMessage 补偿 — 与付费 reaction 的
// 借记-动作-退款编排骨架一致。
//
// 问题 B（修复）：此前付费消息在隐私校验通过后直接返回
// PAYMENT_UNSUPPORTED（“账本尚未实现”）。现在账本已实现：配置了 Stars
// 账本的服务器接受 allow_paid_stars 授权并原子扣费入账；账本缺席时保持
// PAYMENT_UNSUPPORTED（诚实降级，历史行为）。
func (r *Router) ensurePrivateContactAllowed(
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
	return required, nil
}

// chargePrivateMessage 在消息落库前执行一笔配对转账（问题 B+C）：发送方
// 借记 stars、接收方贷记同一金额，两条流水共享同一个公开交易 ID。返回的
// 已是客户端可识别的 RPC 错误（调用点直接透传）：账本缺席 →
// PAYMENT_UNSUPPORTED；余额不足 → BALANCE_TOO_LOW；其余 → INTERNAL。
func (r *Router) chargePrivateMessage(ctx context.Context, senderUserID, recipientUserID, stars int64) (domain.StarsPeerTransferResult, error) {
	transfer, ok := r.deps.Stars.(starsPeerTransferService)
	if !ok {
		return domain.StarsPeerTransferResult{}, paymentUnsupportedErr()
	}
	result, err := transfer.TransferToUser(ctx, domain.StarsPeerTransferRequest{
		SenderUserID:    senderUserID,
		RecipientUserID: recipientUserID,
		Amount:          stars,
		Reason:          domain.StarsReasonPaidMessage,
	})
	if err != nil {
		return domain.StarsPeerTransferResult{}, starsErr(err)
	}
	return result, nil
}

// refundPrivateMessage 落库失败后的补偿退款：原路退回配对转账（两侧行
// 带 refund 标志与新的共享 ID）。退款失败只记日志 — 不掩盖原始发送错误。
func (r *Router) refundPrivateMessage(ctx context.Context, senderUserID, recipientUserID, stars int64) {
	transfer, ok := r.deps.Stars.(starsPeerTransferService)
	if !ok {
		return
	}
	if _, err := transfer.RefundPeerTransfer(ctx, domain.StarsPeerTransferRequest{
		SenderUserID:    senderUserID,
		RecipientUserID: recipientUserID,
		Amount:          stars,
		Reason:          domain.StarsReasonPaidMessage,
	}); err != nil {
		r.log.Error("paid message compensation refund failed",
			zap.Int64("sender_user_id", senderUserID), zap.Int64("recipient_user_id", recipientUserID),
			zap.Int64("stars", stars), zap.Error(err))
	}
}
