package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// MessageStore 用 PostgreSQL 实现 store.MessageStore。
type MessageStore struct {
	db     sqlcgen.DBTX
	q      *sqlcgen.Queries
	boxIDs store.BoxIDAllocator
	log    *zap.Logger
	// paidMessageCommissionPermille — доля получателя платного сообщения в
	// промилле (stars_paid_message_commission_permille, раунд 42).
	// 0 = не настроено → серверный дефолт 850.
	paidMessageCommissionPermille int64
}

type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// MessageStoreOption 调整 PostgreSQL MessageStore 依赖。
type MessageStoreOption func(*MessageStore)

// WithMessageAllocators 注入 Redis-backed box_id allocator。
func WithMessageAllocators(boxIDs store.BoxIDAllocator) MessageStoreOption {
	return func(s *MessageStore) {
		s.boxIDs = boxIDs
	}
}

// WithMessageLogger 注入消息 store 日志器，用于追踪消息与 pts 原子写入。
func WithMessageLogger(log *zap.Logger) MessageStoreOption {
	return func(s *MessageStore) {
		s.log = log
	}
}

// NewMessageStore 基于 pgx 连接池（或事务）创建 MessageStore。
func NewMessageStore(db sqlcgen.DBTX, opts ...MessageStoreOption) *MessageStore {
	s := &MessageStore{db: db, q: sqlcgen.New(db)}
	for _, opt := range opts {
		opt(s)
	}
	if s.log == nil {
		s.log = zap.NewNop()
	}
	if s.boxIDs == nil {
		s.boxIDs = pgBoxIDAllocator{s: s}
	}
	return s
}

// WithPaidMessageCommissionPermille 配置付费消息接收者份额（промилле）:
// .env 变量 TELESRV_STARS_PAID_MESSAGE_COMMISSION_PERMILLE；0/越界保留默认 850。
func WithPaidMessageCommissionPermille(permille int64) MessageStoreOption {
	return func(s *MessageStore) {
		if permille > 0 && permille <= 1000 {
			s.paidMessageCommissionPermille = permille
		}
	}
}
