package domain

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
)

// Stars 本地账本领域模型（无 TL 类型，镜像 boost.go 风格）。本实现是本地账本、
// 非真实支付：余额为整数 Stars（线上 Nanos 恒 0），借记原子、永不为负。

// StarsBalance 是一个账号的当前可用 Stars 余额。
type StarsBalance struct {
	UserID  int64
	Balance int64 // 当前可花费 Stars，恒 >= 0
	Granted bool  // 起始授予是否已应用（惰性首读授予的幂等守卫）
}

// StarsGiftPurchaseForm binds one short-lived fiat Stars-gift checkout to its
// authenticated buyer, recipient and exact server-advertised package. The
// client may echo these values but cannot use a form for a different intent.
type StarsGiftPurchaseForm struct {
	FormID          int64
	BuyerUserID     int64
	RecipientUserID int64
	Stars           int64
	Currency        string
	Amount          int64
	IssuedAt        int
	ExpiresAt       int
}

// StarsGiftPurchaseRequest is the immutable settlement command carried by
// inputInvoiceStars(inputStorePaymentStarsGift).
type StarsGiftPurchaseRequest struct {
	StarsGiftPurchaseForm
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// StarsGiftPurchaseResult is the atomically committed recipient credit and
// bilateral service-message receipt. Duplicate means an exact form replay.
type StarsGiftPurchaseResult struct {
	RecipientBalance StarsBalance
	Send             SendPrivateTextResult
	TransactionID    string
	Duplicate        bool
}

// StarsTransactionReason 标记一条流水的语义（投影到 tg.StarsTransaction 的标志位/标题）。
type StarsTransactionReason string

const (
	StarsReasonGrant         StarsTransactionReason = "grant"        // 起始余额自动授予
	StarsReasonTopup         StarsTransactionReason = "topup"        // 充值（本地铸造）
	StarsReasonReaction      StarsTransactionReason = "reaction"     // 付费 reaction 花费
	StarsReasonGift          StarsTransactionReason = "gift"         // 星礼花费/收取
	StarsReasonGiftUpgrade   StarsTransactionReason = "gift_upgrade" // 普通礼物升级为唯一礼物
	StarsReasonGiftTransfer  StarsTransactionReason = "gift_transfer"
	StarsReasonGiftResale    StarsTransactionReason = "gift_resale"
	StarsReasonGiftOffer     StarsTransactionReason = "gift_offer"
	StarsReasonGiftAuction   StarsTransactionReason = "gift_auction"
	StarsReasonGiftPrepaid   StarsTransactionReason = "gift_prepaid_upgrade"
	StarsReasonGiftDrop      StarsTransactionReason = "gift_drop_original_details"
	StarsReasonPaidMedia     StarsTransactionReason = "paid_media"   // 付费媒体解锁
	StarsReasonPaidMessage   StarsTransactionReason = "paid_message" // 频道 Direct Message 花费
	StarsReasonSuggestedPost StarsTransactionReason = "suggested_post"
	StarsReasonAdjust        StarsTransactionReason = "adjust" // 兜底/人工调整
)

// StarsTransaction 是一条账本流水。amount 带符号：贷记 > 0（含 refund/收取），借记 < 0。
type StarsTransaction struct {
	ID          int64 // 单调递增账本 id（keyset 游标）
	UserID      int64 // 账本归属
	Peer        Peer  // 对手方（grant/topup 等无对手时为零 Peer）
	Amount      int64 // 带符号金额
	Date        int   // Unix 秒
	Reason      StarsTransactionReason
	Title       string // 可选，投影到 tg.StarsTransaction.Title
	Description string // 可选，投影到 tg.StarsTransaction.Description
}

// IsCredit 报告该流水是否为入账（贷记），投影到 tg.StarsTransaction.Refund。
func (t StarsTransaction) IsCredit() bool { return t.Amount > 0 }

// StarsTransactionDirection scopes one payments.getStarsTransactions view.
// The zero value intentionally means the combined inbound/outbound history.
type StarsTransactionDirection uint8

const (
	StarsTransactionDirectionAll StarsTransactionDirection = iota
	StarsTransactionDirectionIncoming
	StarsTransactionDirectionOutgoing
)

func (d StarsTransactionDirection) Valid() bool {
	return d <= StarsTransactionDirectionOutgoing
}

func (d StarsTransactionDirection) IncludesAmount(amount int64) bool {
	switch d {
	case StarsTransactionDirectionAll:
		return true
	case StarsTransactionDirectionIncoming:
		return amount > 0
	case StarsTransactionDirectionOutgoing:
		return amount < 0
	default:
		return false
	}
}

// StarsTransactionQuery keeps direction, ordering and the opaque keyset cursor
// together so filtering is applied before LIMIT in every ledger backend.
type StarsTransactionQuery struct {
	Offset    string
	Limit     int
	Direction StarsTransactionDirection
	Ascending bool
}

// NormalizeStarsTransactionQuery preserves the existing bounded limit/offset
// behavior while rejecting impossible internal direction values.
func NormalizeStarsTransactionQuery(query StarsTransactionQuery) (StarsTransactionQuery, error) {
	if !query.Direction.Valid() {
		return StarsTransactionQuery{}, ErrStarsTransactionQueryInvalid
	}
	if len(query.Offset) > MaxStarsTransactionsOffsetBytes {
		query.Offset = ""
	}
	if query.Limit <= 0 || query.Limit > MaxStarsTransactionsLimit {
		query.Limit = MaxStarsTransactionsLimit
	}
	return query, nil
}

// StarsTransactionPage 是一页账本流水 + 当前余额 + 分页游标 + 对手方用户富化集合。
type StarsTransactionPage struct {
	Balance      int64
	Transactions []StarsTransaction
	NextOffset   string // 空表示无更多页（DrKLO 据此停止翻页，勿在末页给非空值）
	Users        []User // History 中提到的对手方用户，供 tg Users 富化
}

// TonTransaction is an entry in telesrv's internal nanoton ledger. It models
// the Telegram TON-denominated gift UI without contacting a wallet, Fragment,
// a TON node, or any blockchain service.
type TonTransaction struct {
	ID          int64
	UserID      int64
	Peer        Peer
	GiftID      int64
	Amount      int64 // signed nanoton amount
	Date        int
	Reason      StarsTransactionReason
	Title       string
	Description string
}

type TonTransactionPage struct {
	Balance      int64
	Transactions []TonTransaction
	NextOffset   string
	Users        []User
}

// Stars 账本边界常量。
const (
	// DefaultStarsStartingGrant 是惰性首读授予的起始 Stars 余额（本地测试用）。
	DefaultStarsStartingGrant = 1000
	// MaxStarsTransactionsLimit 是 getStarsTransactions 单页上限。
	MaxStarsTransactionsLimit = 100
	// MaxStarsTransactionsOffsetBytes 是 keyset 游标字符串长度上限。
	MaxStarsTransactionsOffsetBytes = 64
)

// Stars 账本哨兵错误（rpc 层 errors.Is 匹配后映射为 tgerr，仿 ErrPremiumRequired）。
var (
	// ErrStarsInsufficient 表示余额不足以完成借记（映射 BALANCE_TOO_LOW）。
	ErrStarsInsufficient = errors.New("stars: insufficient balance")
	// ErrStarsInvalidAmount 表示金额非法（<=0）。
	ErrStarsInvalidAmount = errors.New("stars: invalid amount")
	// ErrStarsTransactionQueryInvalid 表示内部构造了不可能的流水方向。
	ErrStarsTransactionQueryInvalid = errors.New("stars: invalid transaction query")
	// ErrStarsGiftFormInvalid covers a missing/cross-account/mutated form.
	ErrStarsGiftFormInvalid = errors.New("stars: gift form invalid")
	// ErrStarsGiftFormExpired is returned before any settlement write.
	ErrStarsGiftFormExpired = errors.New("stars: gift form expired")
	// ErrStarsGiftUnavailable covers a recipient that cannot receive the gift.
	ErrStarsGiftUnavailable = errors.New("stars: gift unavailable")
)

// StarsPaymentRequiredError reports the minimum paid-message authorization the
// sender must include in allow_paid_stars. The authorization is a ceiling; the
// ledger debits only the channel's current configured price.
type StarsPaymentRequiredError struct {
	Stars int64
}

func (e *StarsPaymentRequiredError) Error() string {
	return fmt.Sprintf("stars: allow payment required: %d", e.Stars)
}

// EncodeStarsCursor 把 keyset 游标（最后一条流水 id）编码为客户端不透明字符串。
func EncodeStarsCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

// DecodeStarsCursor 反解 EncodeStarsCursor；无法解析（含空串）时返回 ok=false，
// 调用方应据此从首页开始（客户端只会回传我们给过的游标，畸形仅作兜底）。
func DecodeStarsCursor(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
