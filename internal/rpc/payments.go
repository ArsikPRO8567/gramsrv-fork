package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// registerPayments 注册 payments.* RPC：Stars 本地账本（余额/流水真实化）+ 其余
// gift/auction/revenue 第一阶段兼容桩。
func (r *Router) registerPayments(d *tlprofile.Dispatcher) {
	registerRPC[*tg.PaymentsCanPurchaseStoreRequest](d, tlprofile.SemanticMethodPaymentsCanPurchaseStore, func(ctx context.Context, req *tg.PaymentsCanPurchaseStoreRequest) (any, error) {
		return r.onPaymentsCanPurchaseStore(ctx, req)
	})
	registerRPC[*tg.PaymentsAssignPlayMarketTransactionRequest](d, tlprofile.SemanticMethodPaymentsAssignPlayMarketTransaction, func(ctx context.Context, req *tg.PaymentsAssignPlayMarketTransactionRequest) (any, error) {
		return r.onPaymentsAssignPlayMarketTransaction(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsGiftOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsGiftOptions, func(ctx context.Context, req *tg.PaymentsGetStarsGiftOptionsRequest) (any, error) {
		return r.onPaymentsGetStarsGiftOptions(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsGiveawayOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsGiveawayOptions, func(ctx context.Context, _ *tg.PaymentsGetStarsGiveawayOptionsRequest) (any, error) {
		return r.onPaymentsGetStarsGiveawayOptions(ctx)
	})
	registerRPC[*tg.PaymentsGetGiveawayInfoRequest](d, tlprofile.SemanticMethodPaymentsGetGiveawayInfo, func(ctx context.Context, req *tg.PaymentsGetGiveawayInfoRequest) (any, error) {
		return r.onPaymentsGetGiveawayInfo(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsTopupOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsTopupOptions, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsTopupOptionsRequest) (any,

		// Premium gift options come from the same versioned XTR catalog used by
		// payment forms and settlement.
		error) {
		return devStarsTopupOptions(), nil
	})
	registerRPC[*tg.PaymentsGetPremiumGiftCodeOptionsRequest](d, tlprofile.SemanticMethodPaymentsGetPremiumGiftCodeOptions, func(ctx context.Context, req *tg.PaymentsGetPremiumGiftCodeOptionsRequest) (any, error) {
		return r.onPaymentsGetPremiumGiftCodeOptions(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsStatusRequest](d, tlprofile.SemanticMethodPaymentsGetStarsStatus, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsStatusRequest) (any, error) {
		return r.onPaymentsGetStarsStatus(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetStarsSubscriptionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsSubscriptions, func(ctx context.Context, req *tg.PaymentsGetStarsSubscriptionsRequest) (any, error) {
		return r.onPaymentsGetStarsSubscriptions(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarsTransactionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsTransactions, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsTransactionsRequest) (any, error) {
		return r.onPaymentsGetStarsTransactions(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetStarsTransactionsByIDRequest](d, tlprofile.SemanticMethodPaymentsGetStarsTransactionsByID, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsTransactionsByIDRequest) (any, error) {
		return r.onPaymentsGetStarsTransactionsByID(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsCheckCanSendGiftRequest](d, tlprofile.SemanticMethodPaymentsCheckCanSendGift, func(ctx context.Context, req *tg.PaymentsCheckCanSendGiftRequest) (any, error) {
		return r.onPaymentsCheckCanSendGift(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftActiveAuctionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftActiveAuctions, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftActiveAuctionsRequest) (any, error) {
		return r.onPaymentsGetStarGiftActiveAuctions(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetStarGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetStarGifts, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftsRequest) (any, error) {
		return r.onPaymentsGetStarGifts(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.PaymentsGetStarGiftUpgradePreviewRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftUpgradePreview, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftUpgradePreviewRequest) (any, error) {
		return r.onPaymentsGetStarGiftUpgradePreview(ctx, layerRequest.
			GiftID)
	})
	registerRPC[*tg.PaymentsGetStarGiftUpgradeAttributesRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftUpgradeAttributes, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftUpgradeAttributesRequest) (any, error) {
		return r.onPaymentsGetStarGiftUpgradeAttributes(ctx, layerRequest.GiftID)
	})
	registerRPC[*tg.PaymentsGetUniqueStarGiftRequest](d, tlprofile.SemanticMethodPaymentsGetUniqueStarGift, func(ctx context.Context, layerRequest *tg.PaymentsGetUniqueStarGiftRequest) (any, error) {
		return r.onPaymentsGetUniqueStarGift(ctx, layerRequest.
			Slug)
	})
	registerRPC[*tg.PaymentsGetUniqueStarGiftValueInfoRequest](d, tlprofile.SemanticMethodPaymentsGetUniqueStarGiftValueInfo, func(ctx context.Context, req *tg.PaymentsGetUniqueStarGiftValueInfoRequest) (any, error) {
		return r.onPaymentsGetUniqueStarGiftValueInfo(ctx, req)
	})
	registerRPC[*tg.PaymentsGetResaleStarGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetResaleStarGifts, func(ctx context.Context, req *tg.PaymentsGetResaleStarGiftsRequest) (any, error) {
		return r.onPaymentsGetResaleStarGifts(ctx, req)
	})
	registerRPC[*tg.PaymentsGetPaymentFormRequest](d, tlprofile.SemanticMethodPaymentsGetPaymentForm, func(ctx context.Context, layerRequest *tg.PaymentsGetPaymentFormRequest) (any, error) {
		return r.onPaymentsGetPaymentForm(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsValidateRequestedInfoRequest](d, tlprofile.SemanticMethodPaymentsValidateRequestedInfo, func(ctx context.Context, req *tg.PaymentsValidateRequestedInfoRequest) (any, error) {
		return r.onPaymentsValidateRequestedInfo(ctx, req)
	})
	registerRPC[*tg.PaymentsSendStarsFormRequest](d, tlprofile.SemanticMethodPaymentsSendStarsForm, func(ctx context.Context, layerRequest *tg.PaymentsSendStarsFormRequest) (any, error) {
		return r.onPaymentsSendStarsForm(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsSendPaymentFormRequest](d, tlprofile.SemanticMethodPaymentsSendPaymentForm, func(ctx context.Context, req *tg.PaymentsSendPaymentFormRequest) (any, error) {
		return r.onPaymentsSendPaymentForm(ctx, req)
	})
	registerRPC[*tg.PaymentsGetSavedStarGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetSavedStarGifts, func(ctx context.Context, layerRequest *tg.PaymentsGetSavedStarGiftsRequest) (any, error) {
		return r.onPaymentsGetSavedStarGifts(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetSavedStarGiftRequest](d, tlprofile.SemanticMethodPaymentsGetSavedStarGift, func(ctx context.Context, layerRequest *tg.PaymentsGetSavedStarGiftRequest) (any, error) {
		return r.onPaymentsGetSavedStarGift(ctx, layerRequest.
			Stargift)
	})
	registerRPC[*tg.PaymentsSaveStarGiftRequest](d, tlprofile.SemanticMethodPaymentsSaveStarGift, func(ctx context.Context, layerRequest *tg.PaymentsSaveStarGiftRequest) (any, error) {
		return r.onPaymentsSaveStarGift(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsConvertStarGiftRequest](d, tlprofile.SemanticMethodPaymentsConvertStarGift, func(ctx context.Context, layerRequest *tg.PaymentsConvertStarGiftRequest) (any, error) {
		return r.onPaymentsConvertStarGift(ctx, layerRequest.
			Stargift)
	})
	registerRPC[*tg.PaymentsUpgradeStarGiftRequest](d, tlprofile.SemanticMethodPaymentsUpgradeStarGift, func(ctx context.Context, layerRequest *tg.PaymentsUpgradeStarGiftRequest) (any, error) {
		return r.onPaymentsUpgradeStarGift(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsUpdateStarGiftPriceRequest](d, tlprofile.SemanticMethodPaymentsUpdateStarGiftPrice, func(ctx context.Context, req *tg.PaymentsUpdateStarGiftPriceRequest) (any, error) {
		return r.onPaymentsUpdateStarGiftPrice(ctx, req)
	})
	registerRPC[*tg.PaymentsTransferStarGiftRequest](d, tlprofile.SemanticMethodPaymentsTransferStarGift, func(ctx context.Context, req *tg.PaymentsTransferStarGiftRequest) (any, error) {
		return r.onPaymentsTransferStarGift(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftWithdrawalURLRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftWithdrawalURL, func(ctx context.Context, req *tg.PaymentsGetStarGiftWithdrawalURLRequest) (any, error) {
		return r.onPaymentsGetStarGiftWithdrawalURL(ctx, req)
	})
	registerRPC[*tg.PaymentsSendStarGiftOfferRequest](d, tlprofile.SemanticMethodPaymentsSendStarGiftOffer, func(ctx context.Context, req *tg.PaymentsSendStarGiftOfferRequest) (any, error) {
		return r.onPaymentsSendStarGiftOffer(ctx, req)
	})
	registerRPC[*tg.PaymentsResolveStarGiftOfferRequest](d, tlprofile.SemanticMethodPaymentsResolveStarGiftOffer, func(ctx context.Context, req *tg.PaymentsResolveStarGiftOfferRequest) (any, error) {
		return r.onPaymentsResolveStarGiftOffer(ctx, req)
	})
	registerRPC[*tg.PaymentsGetCraftStarGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetCraftStarGifts, func(ctx context.Context, req *tg.PaymentsGetCraftStarGiftsRequest) (any, error) {
		return r.onPaymentsGetCraftStarGifts(ctx, req)
	})
	registerRPC[*tg.PaymentsCraftStarGiftRequest](d, tlprofile.SemanticMethodPaymentsCraftStarGift, func(ctx context.Context, req *tg.PaymentsCraftStarGiftRequest) (any, error) {
		return r.onPaymentsCraftStarGift(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftAuctionStateRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftAuctionState, func(ctx context.Context, req *tg.PaymentsGetStarGiftAuctionStateRequest) (any, error) {
		return r.onPaymentsGetStarGiftAuctionState(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftAuctionAcquiredGiftsRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftAuctionAcquiredGifts, func(ctx context.Context, req *tg.PaymentsGetStarGiftAuctionAcquiredGiftsRequest) (any, error) {
		return r.onPaymentsGetStarGiftAuctionAcquiredGifts(ctx, req)
	})
	registerRPC[*tg.PaymentsToggleChatStarGiftNotificationsRequest](d, tlprofile.SemanticMethodPaymentsToggleChatStarGiftNotifications, func(ctx context.Context, req *tg.PaymentsToggleChatStarGiftNotificationsRequest) (any, error) {
		return r.onPaymentsToggleChatStarGiftNotifications(ctx, req)
	})
	registerRPC[*tg.PaymentsGetStarGiftCollectionsRequest](d, tlprofile.SemanticMethodPaymentsGetStarGiftCollections, func(ctx context.Context, layerRequest *tg.PaymentsGetStarGiftCollectionsRequest) (any, error) {
		return r.onPaymentsGetStarGiftCollections(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsCreateStarGiftCollectionRequest](d, tlprofile.SemanticMethodPaymentsCreateStarGiftCollection, func(ctx context.Context, layerRequest *tg.PaymentsCreateStarGiftCollectionRequest) (any, error) {
		return r.onPaymentsCreateStarGiftCollection(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsUpdateStarGiftCollectionRequest](d, tlprofile.SemanticMethodPaymentsUpdateStarGiftCollection, func(ctx context.Context, layerRequest *tg.PaymentsUpdateStarGiftCollectionRequest) (any, error) {
		return r.onPaymentsUpdateStarGiftCollection(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsDeleteStarGiftCollectionRequest](d, tlprofile.SemanticMethodPaymentsDeleteStarGiftCollection, func(ctx context.Context, layerRequest *tg.PaymentsDeleteStarGiftCollectionRequest) (any, error) {
		return r.onPaymentsDeleteStarGiftCollection(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsReorderStarGiftCollectionsRequest](d, tlprofile.SemanticMethodPaymentsReorderStarGiftCollections, func(ctx context.Context, layerRequest *tg.PaymentsReorderStarGiftCollectionsRequest) (any, error) {
		return r.onPaymentsReorderStarGiftCollections(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsToggleStarGiftsPinnedToTopRequest](d, tlprofile.SemanticMethodPaymentsToggleStarGiftsPinnedToTop, func(ctx context.Context, layerRequest *tg.PaymentsToggleStarGiftsPinnedToTopRequest) (any, error) {
		return r.onPaymentsToggleStarGiftsPinnedToTop(ctx, layerRequest)
	})
	registerRPC[*tg.PaymentsGetStarsRevenueAdsAccountURLRequest](d, tlprofile.SemanticMethodPaymentsGetStarsRevenueAdsAccountURL, func(ctx context.Context, layerRequest *tg.PaymentsGetStarsRevenueAdsAccountURLRequest) (any, error) {
		peer := layerRequest.
			Peer
		_ = peer

		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer); err != nil {
			return nil, err
		}
		return &tg.PaymentsStarsRevenueAdsAccountURL{URL: r.publicLink("")}, nil
	})
	registerRPC[*tg.PaymentsGetStarsRevenueStatsRequest](d, tlprofile.SemanticMethodPaymentsGetStarsRevenueStats, func(ctx context.Context, req *tg.PaymentsGetStarsRevenueStatsRequest) (any, error) {
		return r.onPaymentsGetStarsRevenueStats(ctx, req)
	})

}

func (r *Router) onPaymentsCanPurchaseStore(ctx context.Context, _ *tg.PaymentsCanPurchaseStoreRequest) (bool, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return false, internalErr()
	}
	// telesrv deliberately exposes no Google Play products or receipt verifier.
	// DrKLO is steered to the invoice flow by appConfig; if a stale client still
	// reaches this preflight, fail closed instead of authorizing an unverifiable
	// external charge.
	return false, nil
}

func (r *Router) onPaymentsAssignPlayMarketTransaction(ctx context.Context, _ *tg.PaymentsAssignPlayMarketTransactionRequest) (tg.UpdatesClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return nil, tgerr.New(400, "STORE_PAYMENT_UNAVAILABLE")
}

// onPaymentsGetStarsRevenueStats exposes real channel Star Gift proceeds from
// the same peer-scoped ledger as getStarsStatus/getStarsTransactions. Personal
// and bot revenue remain the bounded compatibility response because their
// revenue bucket is distinct from the general Stars balance and is not modeled.
func (r *Router) onPaymentsGetStarsRevenueStats(ctx context.Context, req *tg.PaymentsGetStarsRevenueStatsRequest) (*tg.PaymentsStarsRevenueStats, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req == nil {
		return nil, peerIDInvalidErr()
	}
	owner, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	ton := req.GetTon()
	if owner.Type != domain.PeerTypeChannel {
		return tdesktop.StarsRevenueStats(ton), nil
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, owner); err != nil {
		return nil, err
	}
	ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
	if !ok {
		return tdesktop.StarsRevenueStats(ton), nil
	}
	var balance int64
	if ton {
		balance, err = ledger.ChannelTonBalance(ctx, owner.ID)
	} else {
		balance, err = ledger.ChannelStarsBalance(ctx, owner.ID)
	}
	if err != nil {
		return nil, internalErr()
	}
	stats := tdesktop.StarsRevenueStats(ton)
	var amount tg.StarsAmountClass = &tg.StarsAmount{Amount: balance}
	if ton {
		amount = &tg.StarsTonAmount{Amount: balance}
	}
	// Channel ledgers currently only receive collectible conversion/marketplace
	// proceeds and have no withdrawal/debit path, so balance equals lifetime
	// revenue. Withdrawal stays disabled because no external payout exists.
	stats.Status.CurrentBalance = amount
	stats.Status.AvailableBalance = amount
	stats.Status.OverallRevenue = amount
	return stats, nil
}

type channelGiftLedgerReader interface {
	ChannelStarsBalance(ctx context.Context, channelID int64) (int64, error)
	ChannelStarsTransactions(ctx context.Context, channelID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error)
	ChannelStarsTransactionsByID(ctx context.Context, channelID int64, ids []string) ([]domain.StarsTransaction, error)
	ChannelTonBalance(ctx context.Context, channelID int64) (int64, error)
	ChannelTonTransactions(ctx context.Context, channelID int64, query domain.StarsTransactionQuery) (domain.TonTransactionPage, error)
	ChannelTonTransactionsByID(ctx context.Context, channelID int64, ids []string) ([]domain.TonTransaction, error)
}

// onPaymentsGetStarsStatus 返回请求 peer 的 Stars/本地 TON 余额。个人与频道账本
// 严格隔离；频道读取要求 Star Gift 管理权限，不能把频道收益投影到执行 RPC 的管理员。
// 响应必须是 payments.starsStatus（balance/chats/users 都是必填，空 vector 即可）——
// 两端客户端无条件读取 balance（DrKLO StarsAmount 反序列化 / TDesktop vbalance()）。
func (r *Router) onPaymentsGetStarsStatus(ctx context.Context, req *tg.PaymentsGetStarsStatusRequest) (*tg.PaymentsStarsStatus, error) {
	userID, owner, err := r.starGiftLedgerOwner(ctx, req)
	if err != nil {
		return nil, err
	}
	ton := req != nil && req.GetTon()
	if owner.Type == domain.PeerTypeChannel {
		ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
		if !ok {
			if ton {
				return emptyStarsStatus(&tg.StarsTonAmount{}), nil
			}
			return emptyStarsStatus(&tg.StarsAmount{}), nil
		}
		var balance int64
		if ton {
			balance, err = ledger.ChannelTonBalance(ctx, owner.ID)
		} else {
			balance, err = ledger.ChannelStarsBalance(ctx, owner.ID)
		}
		if err != nil {
			return nil, internalErr()
		}
		var amount tg.StarsAmountClass = &tg.StarsAmount{Amount: balance}
		if ton {
			amount = &tg.StarsTonAmount{Amount: balance}
		}
		out := emptyStarsStatus(amount)
		out.Chats = r.tgChatsForChannelIDs(ctx, userID, []int64{owner.ID})
		return out, nil
	}
	if ton {
		if r.deps.Gifts == nil {
			return emptyStarsStatus(&tg.StarsTonAmount{}), nil
		}
		balance, err := r.deps.Gifts.TonBalance(ctx, userID)
		if err != nil {
			return nil, internalErr()
		}
		return emptyStarsStatus(&tg.StarsTonAmount{Amount: balance}), nil
	}
	if r.deps.Stars == nil {
		return emptyStarsStatus(&tg.StarsAmount{}), nil
	}
	bal, err := r.deps.Stars.GetBalance(ctx, userID)
	if err != nil {
		return nil, starsErr(err)
	}
	return emptyStarsStatus(&tg.StarsAmount{Amount: bal.Balance}), nil
}

// onPaymentsGetStarsSubscriptions returns the authoritative current balance
// with an empty subscription page. telesrv does not create recurring Stars
// subscriptions yet; returning a well-shaped terminal page lets both official
// clients finish loading the Stars screen without inventing subscription state.
func (r *Router) onPaymentsGetStarsSubscriptions(ctx context.Context, req *tg.PaymentsGetStarsSubscriptionsRequest) (*tg.PaymentsStarsStatus, error) {
	if req == nil || len(req.Offset) > domain.MaxStarsTransactionsOffsetBytes {
		return nil, inputRequestInvalidErr()
	}
	userID, owner, err := r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	if owner.Type != domain.PeerTypeUser || owner.ID != userID {
		return nil, peerIDInvalidErr()
	}
	if r.deps.Stars == nil {
		return emptyStarsStatus(&tg.StarsAmount{}), nil
	}
	balance, err := r.deps.Stars.GetBalance(ctx, userID)
	if err != nil {
		return nil, starsErr(err)
	}
	return emptyStarsStatus(&tg.StarsAmount{Amount: balance.Balance}), nil
}

// onPaymentsGetStarsTransactions 返回 keyset 分页的 Stars 流水（同 starsStatus 信封）。
// 末页必须省略 next_offset（flag 不置），否则 DrKLO 会无限翻页。
func (r *Router) onPaymentsGetStarsTransactions(ctx context.Context, req *tg.PaymentsGetStarsTransactionsRequest) (*tg.PaymentsStarsStatus, error) {
	userID, owner, err := r.starGiftTransactionLedgerOwner(ctx, req)
	if err != nil {
		return nil, err
	}
	query, err := starsTransactionQuery(req)
	if err != nil {
		return nil, err
	}
	ton := req != nil && req.GetTon()
	if owner.Type == domain.PeerTypeChannel {
		ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
		if !ok {
			if ton {
				return emptyStarsStatus(&tg.StarsTonAmount{}), nil
			}
			return emptyStarsStatus(&tg.StarsAmount{}), nil
		}
		if ton {
			page, err := ledger.ChannelTonTransactions(ctx, owner.ID, query)
			if err != nil {
				return nil, internalErr()
			}
			out := emptyStarsStatus(&tg.StarsTonAmount{Amount: page.Balance})
			if txns := tgTonTransactions(page.Transactions, r.loadStarsGiftPayloads(ctx, starsTonTransactionGiftRefs(page.Transactions))); len(txns) > 0 {
				out.SetHistory(txns)
			}
			if page.NextOffset != "" {
				out.SetNextOffset(page.NextOffset)
			}
			r.enrichChannelTonLedgerStatus(ctx, userID, owner.ID, page.Transactions, out)
			return out, nil
		}
		page, err := ledger.ChannelStarsTransactions(ctx, owner.ID, query)
		if err != nil {
			return nil, internalErr()
		}
		out := emptyStarsStatus(&tg.StarsAmount{Amount: page.Balance})
		if txns := tgStarsTransactions(page.Transactions, r.loadStarsGiftPayloads(ctx, starsTransactionGiftRefs(page.Transactions))); len(txns) > 0 {
			out.SetHistory(txns)
		}
		if page.NextOffset != "" {
			out.SetNextOffset(page.NextOffset)
		}
		r.enrichChannelStarsLedgerStatus(ctx, userID, owner.ID, page.Transactions, out)
		return out, nil
	}
	if ton {
		if r.deps.Gifts == nil {
			return emptyStarsStatus(&tg.StarsTonAmount{}), nil
		}
		page, err := r.deps.Gifts.TonTransactions(ctx, userID, query)
		if err != nil {
			return nil, internalErr()
		}
		out := emptyStarsStatus(&tg.StarsTonAmount{Amount: page.Balance})
		if txns := tgTonTransactions(page.Transactions, r.loadStarsGiftPayloads(ctx, starsTonTransactionGiftRefs(page.Transactions))); len(txns) > 0 {
			out.SetHistory(txns)
		}
		if page.NextOffset != "" {
			out.SetNextOffset(page.NextOffset)
		}
		ids := make([]int64, 0)
		for _, txn := range page.Transactions {
			if txn.Peer.Type == domain.PeerTypeUser {
				ids = append(ids, txn.Peer.ID)
			}
		}
		out.Users = tgUsersForViewer(userID, r.domainUsersForIDs(ctx, userID, uniqueInt64(ids)))
		return out, nil
	}
	if r.deps.Stars == nil {
		return emptyStarsStatus(&tg.StarsAmount{}), nil
	}
	page, err := r.deps.Stars.ListTransactions(ctx, userID, query)
	if err != nil {
		return nil, starsErr(err)
	}
	out := emptyStarsStatus(&tg.StarsAmount{Amount: page.Balance})
	if txns := tgStarsTransactions(page.Transactions, r.loadStarsGiftPayloads(ctx, starsTransactionGiftRefs(page.Transactions))); len(txns) > 0 {
		out.SetHistory(txns)
	}
	if page.NextOffset != "" {
		out.SetNextOffset(page.NextOffset)
	}
	// 富化流水中提到的用户对手方（频道对手方进 Chats 留待 paid reaction 阶段）。
	if ids := starsTransactionUserIDs(page.Transactions); len(ids) > 0 {
		out.Users = tgUsersForViewer(userID, r.domainUsersForIDs(ctx, userID, ids))
	}
	return out, nil
}

// maxStarsTransactionIDLen ограничивает длину запрошенного публичного id
// (stars_/ton_ + 60 символов; исторические строки — identity как текст).
const maxStarsTransactionIDLen = 128

// onPaymentsGetStarsTransactionsByID 返回按公开交易 ID 选中的流水
// （payments.getStarsTransactionsByID）。scope 由 peer 决定（本人/频道），ton
// 标志切换 Stars/TON 账本；历史行的 transaction_id 是旧行 id 的十进制串，
// 新行是 stars_/ton_ + 60 位随机串 — 两种形状都按字符串精确匹配。未知 ID
// 静默跳过（批量语义：返回已找到的子集），畸形 ID 报 TRANSACTION_ID_INVALID。
func (r *Router) onPaymentsGetStarsTransactionsByID(ctx context.Context, req *tg.PaymentsGetStarsTransactionsByIDRequest) (*tg.PaymentsStarsStatus, error) {
	if req == nil || len(req.ID) == 0 {
		return nil, inputRequestInvalidErr()
	}
	userID, owner, err := r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(req.ID))
	for _, input := range req.ID {
		// inputStarsTransaction.refund 选中「该交易的退款」；退款事务在本
		// 账本中是独立行，与原交易同 ID 形状，这里按 id 直接匹配即可。
		if input.ID == "" {
			return nil, transactionIDInvalidErr()
		}
		if len(input.ID) > maxStarsTransactionIDLen {
			return nil, transactionIDInvalidErr()
		}
		ids = append(ids, input.ID)
	}
	ton := req.GetTon()
	if owner.Type == domain.PeerTypeChannel {
		ledger, ok := r.deps.Gifts.(channelGiftLedgerReader)
		if !ok {
			if ton {
				return emptyStarsStatus(&tg.StarsTonAmount{}), nil
			}
			return emptyStarsStatus(&tg.StarsAmount{}), nil
		}
		if ton {
			txns, err := ledger.ChannelTonTransactionsByID(ctx, owner.ID, ids)
			if err != nil {
				return nil, internalErr()
			}
			return r.starsStatusFromTonTxns(ctx, userID, owner.ID, txns, 0), nil
		}
		txns, err := ledger.ChannelStarsTransactionsByID(ctx, owner.ID, ids)
		if err != nil {
			return nil, internalErr()
		}
		return r.starsStatusFromStarsTxns(ctx, userID, owner.ID, txns, 0), nil
	}
	if ton {
		if r.deps.Gifts == nil {
			return emptyStarsStatus(&tg.StarsTonAmount{}), nil
		}
		txns, err := r.deps.Gifts.TonTransactionsByID(ctx, userID, ids)
		if err != nil {
			return nil, internalErr()
		}
		balance, err := r.deps.Gifts.TonBalance(ctx, userID)
		if err != nil {
			return nil, internalErr()
		}
		return r.starsStatusFromTonTxns(ctx, userID, owner.ID, txns, balance), nil
	}
	if r.deps.Stars == nil {
		return emptyStarsStatus(&tg.StarsAmount{}), nil
	}
	txns, err := r.deps.Stars.ListTransactionsByID(ctx, userID, ids)
	if err != nil {
		return nil, starsErr(err)
	}
	balance, err := r.deps.Stars.GetBalance(ctx, userID)
	if err != nil {
		return nil, starsErr(err)
	}
	return r.starsStatusFromStarsTxns(ctx, userID, owner.ID, txns, balance.Balance), nil
}

// starsStatusFromStarsTxns 构造 ByID 响应：投影 + balance + 对手方富化（同
// getStarsTransactions 信封，无分页游标）。
func (r *Router) starsStatusFromStarsTxns(ctx context.Context, viewerUserID, ownerChannelID int64, txns []domain.StarsTransaction, balance int64) *tg.PaymentsStarsStatus {
	out := emptyStarsStatus(&tg.StarsAmount{Amount: balance})
	if txns2 := tgStarsTransactions(txns, r.loadStarsGiftPayloads(ctx, starsTransactionGiftRefs(txns))); len(txns2) > 0 {
		out.SetHistory(txns2)
	}
	if ownerChannelID > 0 {
		r.enrichChannelStarsLedgerStatus(ctx, viewerUserID, ownerChannelID, txns, out)
	} else if ids := starsTransactionUserIDs(txns); len(ids) > 0 {
		out.Users = tgUsersForViewer(viewerUserID, r.domainUsersForIDs(ctx, viewerUserID, ids))
	} else {
		out.Users = []tg.UserClass{}
	}
	return out
}

func (r *Router) starsStatusFromTonTxns(ctx context.Context, viewerUserID, ownerChannelID int64, txns []domain.TonTransaction, balance int64) *tg.PaymentsStarsStatus {
	out := emptyStarsStatus(&tg.StarsTonAmount{Amount: balance})
	if txns2 := tgTonTransactions(txns, r.loadStarsGiftPayloads(ctx, starsTonTransactionGiftRefs(txns))); len(txns2) > 0 {
		out.SetHistory(txns2)
	}
	if ownerChannelID > 0 {
		r.enrichChannelTonLedgerStatus(ctx, viewerUserID, ownerChannelID, txns, out)
	} else {
		ids := make([]int64, 0)
		for _, txn := range txns {
			if txn.Peer.Type == domain.PeerTypeUser {
				ids = append(ids, txn.Peer.ID)
			}
		}
		if uids := uniqueInt64(ids); len(uids) > 0 {
			out.Users = tgUsersForViewer(viewerUserID, r.domainUsersForIDs(ctx, viewerUserID, uids))
		} else {
			out.Users = []tg.UserClass{}
		}
	}
	return out
}

func starsTransactionQuery(req *tg.PaymentsGetStarsTransactionsRequest) (domain.StarsTransactionQuery, error) {
	if req == nil {
		return domain.StarsTransactionQuery{}, inputRequestInvalidErr()
	}
	inbound, outbound := req.GetInbound(), req.GetOutbound()
	if inbound && outbound {
		return domain.StarsTransactionQuery{}, inputRequestInvalidErr()
	}
	if _, ok := req.GetSubscriptionID(); ok {
		// Stars subscriptions are not part of the current business model. Do not
		// silently return the unfiltered ledger for a requested subscription.
		return domain.StarsTransactionQuery{}, subscriptionIDInvalidErr()
	}
	direction := domain.StarsTransactionDirectionAll
	if inbound {
		direction = domain.StarsTransactionDirectionIncoming
	} else if outbound {
		direction = domain.StarsTransactionDirectionOutgoing
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxStarsTransactionsLimit {
		limit = domain.MaxStarsTransactionsLimit
	}
	return domain.StarsTransactionQuery{
		Offset:    req.Offset,
		Limit:     limit,
		Direction: direction,
		Ascending: req.GetAscending(),
	}, nil
}

func (r *Router) starGiftLedgerOwner(ctx context.Context, req *tg.PaymentsGetStarsStatusRequest) (int64, domain.Peer, error) {
	if req == nil {
		return 0, domain.Peer{}, peerIDInvalidErr()
	}
	return r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
}

func (r *Router) starGiftTransactionLedgerOwner(ctx context.Context, req *tg.PaymentsGetStarsTransactionsRequest) (int64, domain.Peer, error) {
	if req == nil {
		return 0, domain.Peer{}, peerIDInvalidErr()
	}
	return r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
}

func (r *Router) starGiftLedgerOwnerForPeer(ctx context.Context, input tg.InputPeerClass) (int64, domain.Peer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, domain.Peer{}, internalErr()
	}
	owner, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return 0, domain.Peer{}, err
	}
	if owner.Type == domain.PeerTypeUser {
		if owner.ID != userID {
			return 0, domain.Peer{}, peerIDInvalidErr()
		}
		return userID, owner, nil
	}
	if err := r.checkStarGiftOwnerPermission(ctx, userID, owner); err != nil {
		return 0, domain.Peer{}, err
	}
	return userID, owner, nil
}

func (r *Router) enrichChannelStarsLedgerStatus(ctx context.Context, viewerID, ownerChannelID int64, txns []domain.StarsTransaction, out *tg.PaymentsStarsStatus) {
	userIDs := make([]int64, 0, len(txns))
	channelIDs := []int64{ownerChannelID}
	for _, txn := range txns {
		switch txn.Peer.Type {
		case domain.PeerTypeUser:
			userIDs = append(userIDs, txn.Peer.ID)
		case domain.PeerTypeChannel:
			channelIDs = append(channelIDs, txn.Peer.ID)
		}
	}
	out.Users = tgUsersForViewer(viewerID, r.domainUsersForIDs(ctx, viewerID, uniqueInt64(userIDs)))
	out.Chats = r.tgChatsForChannelIDs(ctx, viewerID, uniqueInt64(channelIDs))
}

func (r *Router) enrichChannelTonLedgerStatus(ctx context.Context, viewerID, ownerChannelID int64, txns []domain.TonTransaction, out *tg.PaymentsStarsStatus) {
	userIDs := make([]int64, 0, len(txns))
	channelIDs := []int64{ownerChannelID}
	for _, txn := range txns {
		switch txn.Peer.Type {
		case domain.PeerTypeUser:
			userIDs = append(userIDs, txn.Peer.ID)
		case domain.PeerTypeChannel:
			channelIDs = append(channelIDs, txn.Peer.ID)
		}
	}
	out.Users = tgUsersForViewer(viewerID, r.domainUsersForIDs(ctx, viewerID, uniqueInt64(userIDs)))
	out.Chats = r.tgChatsForChannelIDs(ctx, viewerID, uniqueInt64(channelIDs))
}

// emptyStarsStatus 构造一个合法的最小 payments.starsStatus（chats/users 非空 vector 但可空）。
func emptyStarsStatus(balance tg.StarsAmountClass) *tg.PaymentsStarsStatus {
	return &tg.PaymentsStarsStatus{
		Balance: balance,
		Chats:   []tg.ChatClass{},
		Users:   []tg.UserClass{},
	}
}

// starsGiftPayloads 缓存一页流水涉及的礼物载荷：唯一礼物（预览/动画源）与
// 目录礼物（普通礼物的贴纸动画源）。历史行没有礼物关联，载荷缺失时退回
// 纯文本/标志渲染，绝不携带空 stargift（客户端会按标志解引用崩溃）。
type starsGiftPayloads struct {
	uniques map[int64]domain.UniqueStarGift
	catalog map[int64]domain.StarGift
}

type starsGiftRef struct {
	id     int64
	unique bool
}

func starsTransactionGiftRefs(in []domain.StarsTransaction) []starsGiftRef {
	refs := make([]starsGiftRef, 0, len(in))
	for _, t := range in {
		if t.GiftID > 0 {
			refs = append(refs, starsGiftRef{id: t.GiftID, unique: t.GiftIsUnique})
		}
	}
	return refs
}

func starsTonTransactionGiftRefs(in []domain.TonTransaction) []starsGiftRef {
	refs := make([]starsGiftRef, 0, len(in))
	for _, t := range in {
		if t.GiftID > 0 {
			refs = append(refs, starsGiftRef{id: t.GiftID, unique: t.GiftIsUnique})
		}
	}
	return refs
}

// loadStarsGiftPayloads 批量装载一页流水的礼物载荷。读取失败按缺载荷处理
// （客户端退回文本渲染），不因装饰性载荷失败整个历史页。
func (r *Router) loadStarsGiftPayloads(ctx context.Context, refs []starsGiftRef) starsGiftPayloads {
	payloads := starsGiftPayloads{
		uniques: map[int64]domain.UniqueStarGift{},
		catalog: map[int64]domain.StarGift{},
	}
	if r.deps.Gifts == nil || len(refs) == 0 {
		return payloads
	}
	uniqueIDs := make([]int64, 0, len(refs))
	seenUnique := make(map[int64]struct{}, len(refs))
	catalogIDs := make([]int64, 0, len(refs))
	seenCatalog := make(map[int64]struct{}, len(refs))
	for _, ref := range refs {
		if ref.unique {
			if _, ok := seenUnique[ref.id]; ok {
				continue
			}
			seenUnique[ref.id] = struct{}{}
			uniqueIDs = append(uniqueIDs, ref.id)
			continue
		}
		if _, ok := seenCatalog[ref.id]; ok {
			continue
		}
		seenCatalog[ref.id] = struct{}{}
		catalogIDs = append(catalogIDs, ref.id)
	}
	if len(uniqueIDs) > 0 {
		if uniques, err := r.deps.Gifts.UniqueByIDs(ctx, uniqueIDs); err == nil {
			payloads.uniques = uniques
		}
	}
	for _, id := range catalogIDs {
		if gift, ok, err := r.deps.Gifts.GiftByID(ctx, id); err == nil && ok {
			payloads.catalog[id] = gift
		}
	}
	return payloads
}

// tgStarsTransactionGift 返回该流水行的 stargift 载荷；无关联/载荷缺失返回 nil。
func tgStarsTransactionGift(payloads starsGiftPayloads, giftID int64, giftIsUnique bool) tg.StarGiftClass {
	if giftID <= 0 {
		return nil
	}
	if giftIsUnique {
		if unique, ok := payloads.uniques[giftID]; ok {
			return tgUniqueStarGift(unique)
		}
		return nil
	}
	if gift, ok := payloads.catalog[giftID]; ok {
		return tgStarGift(gift)
	}
	return nil
}

// tgStarsTransactions 把账本流水投影为 tg.StarsTransaction（amount 带符号：借记为负）。
// Раунд 30: id 用公开 transaction_id（stars_…/ton_…，旧行回退账本 id 十进制串，
// 客户端已记住的 id 继续可解析）；refund 只按 refunded 标志（与方向无关）；
// 礼物类 reason 携带 stargift 载荷并按官方语义设置类型标志（gift 只属于
// 「来自 peer 的礼物」入账侧），文本由客户端本地化——服务器不再下发英文文案。
func tgStarsTransactions(in []domain.StarsTransaction, payloads starsGiftPayloads) []tg.StarsTransaction {
	out := make([]tg.StarsTransaction, 0, len(in))
	for _, t := range in {
		item := tg.StarsTransaction{
			ID:     t.PublicID(),
			Amount: &tg.StarsAmount{Amount: t.Amount},
			Date:   t.Date,
			Peer:   tgStarsTransactionPeer(t),
		}
		if t.Refunded {
			item.Refund = true
		}
		if t.Title != "" {
			item.SetTitle(t.Title)
		}
		if t.Description != "" {
			item.SetDescription(t.Description)
		}
		switch t.Reason {
		case domain.StarsReasonReaction:
			item.Reaction = true
		case domain.StarsReasonPaidMessage:
			item.SetPaidMessages(1)
		case domain.StarsReasonGift:
			// 官方语义：gift=「这笔交易是 peer 发来的礼物」，只属于入账；
			// 发送侧/退款行不置标志（退款按 refund 渲染）。
			if t.Amount > 0 && !t.Refunded {
				item.Gift = true
			}
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftConvert:
			// 礼物转星是「出售」：stargift 渲染礼物本体，不置 gift 标志。
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftUpgrade:
			// Telegram Desktop treats stargift_upgrade as a promise that the
			// optional stargift field contains a unique gift and immediately
			// dereferences its model document while building the history row.
			// The flag is advertised only together with a complete
			// unique-gift projection (legacy rows keep title rendering).
			if unique, ok := payloads.uniques[t.GiftID]; t.GiftIsUnique && ok {
				item.StargiftUpgrade = true
				item.SetStargift(tgUniqueStarGift(unique))
			}
		case domain.StarsReasonGiftResale:
			item.StargiftResale = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftPrepaid:
			item.StargiftPrepaidUpgrade = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftDrop:
			item.StargiftDropOriginalDetails = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftAuction:
			item.StargiftAuctionBid = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftOffer:
			item.Offer = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonPremium:
			if t.PremiumMonths > 0 {
				item.SetPremiumGiftMonths(t.PremiumMonths)
			}
		}
		out = append(out, item)
	}
	return out
}

func tgTonTransactions(in []domain.TonTransaction, payloads starsGiftPayloads) []tg.StarsTransaction {
	out := make([]tg.StarsTransaction, 0, len(in))
	for _, t := range in {
		item := tg.StarsTransaction{ID: t.PublicID(), Amount: &tg.StarsTonAmount{Amount: t.Amount},
			Date: t.Date, Peer: tgStarsTransactionPeer(domain.StarsTransaction{Peer: t.Peer, Reason: t.Reason})}
		if t.Refunded {
			item.Refund = true
		}
		if t.Title != "" {
			item.SetTitle(t.Title)
		}
		if t.Description != "" {
			item.SetDescription(t.Description)
		}
		switch t.Reason {
		case domain.StarsReasonGift:
			if t.Amount > 0 && !t.Refunded {
				item.Gift = true
			}
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftConvert:
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftResale:
			item.StargiftResale = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftOffer:
			item.Offer = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftAuction:
			item.StargiftAuctionBid = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftPrepaid:
			item.StargiftPrepaidUpgrade = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		case domain.StarsReasonGiftDrop:
			item.StargiftDropOriginalDetails = true
			if gift := tgStarsTransactionGift(payloads, t.GiftID, t.GiftIsUnique); gift != nil {
				item.SetStargift(gift)
			}
		}
		out = append(out, item)
	}
	return out
}

// tgStarsTransactionPeer 选择对手方构造器：grant/topup 走 Fragment（站外充值轨），
// 真实 peer 走 starsTransactionPeer，其余兜底 Unsupported（Peer 字段必填，不可为 nil）。
func tgStarsTransactionPeer(t domain.StarsTransaction) tg.StarsTransactionPeerClass {
	switch t.Reason {
	case domain.StarsReasonGrant, domain.StarsReasonTopup:
		return &tg.StarsTransactionPeerFragment{}
	case domain.StarsReasonPremium:
		return &tg.StarsTransactionPeerPremiumBot{}
	}
	if t.Peer.Type != "" && t.Peer.ID != 0 {
		if p := tgPeer(t.Peer); p != nil {
			return &tg.StarsTransactionPeer{Peer: p}
		}
	}
	return &tg.StarsTransactionPeerUnsupported{}
}

// starsTransactionUserIDs 收集流水中去重的用户类对手方 id。
func starsTransactionUserIDs(in []domain.StarsTransaction) []int64 {
	seen := make(map[int64]struct{}, len(in))
	ids := make([]int64, 0, len(in))
	for _, t := range in {
		if t.Peer.Type != domain.PeerTypeUser || t.Peer.ID == 0 {
			continue
		}
		if _, ok := seen[t.Peer.ID]; ok {
			continue
		}
		seen[t.Peer.ID] = struct{}{}
		ids = append(ids, t.Peer.ID)
	}
	return ids
}

// starsErr 把 Stars 账本领域错误映射为客户端可识别的 tgerr（仿 premiumBoostErr）。
func starsErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrStarsInsufficient):
		return balanceTooLowErr()
	case errors.Is(err, domain.ErrStarsInvalidAmount):
		return starsAmountInvalidErr()
	default:
		return internalErr()
	}
}
