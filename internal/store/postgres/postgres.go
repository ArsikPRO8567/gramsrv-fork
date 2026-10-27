// Package postgres 用 PostgreSQL 实现持久化存储接口（第一阶段：AuthKeyStore）。
//
// 查询代码由 sqlc 生成于 ./sqlcgen（见 telesrv/sqlc.yaml）；本包在其上实现 store 接口。
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // 注册 pgx5:// migrate driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // sql.Open("pgx", ...) для снятия dirty-флага

	"telesrv/deploy"
	"telesrv/internal/store/postgres/sqlcgen"
)

const defaultMinConns = 16

// MigrationStatus 是启动迁移后的 schema 状态。
type MigrationStatus struct {
	Version uint
	Dirty   bool
	Empty   bool
	// RecoveredDirty: при старте схема была помечена dirty (последствия
	// прерванной миграции), runner сам снял флаг и повторил миграцию.
	// Информационное поле для лога запуска.
	RecoveredDirty bool
}

// schemaMigrationLockTimeout ограничивает ожидание блокировки миграции, когда
// параллельно запущен другой инстанс telesrv. Драйвер golang-migrate ждёт лишь
// 15 секунд (DefaultLockTimeout) и затем роняет запуск с неочевидной ошибкой
// "timeout: can't acquire database lock" — первая установка схемы на свежей
// базе легко превышает этот бюджет на медленном железе.
const schemaMigrationLockTimeout = 5 * time.Minute

// PoolOption 调整 pgxpool 连接池配置。
type PoolOption func(*pgxpool.Config)

// WithMaxConns 设置连接池最大连接数；<=0 时保持 pgx 默认。
// 同时把 MinConns 预热到 min(maxConns, 16)，降低 TDesktop 启动风暴下的冷连接尾延迟突刺。
func WithMaxConns(n int) PoolOption {
	return func(cfg *pgxpool.Config) {
		if n <= 0 {
			return
		}
		cfg.MaxConns = int32(n)
		minConns := int32(defaultMinConns)
		if int32(n) < minConns {
			minConns = int32(n)
		}
		cfg.MinConns = minConns
	}
}

// WithMinConns 设置启动时预热的最小连接数；<=0 保持既有配置。
func WithMinConns(n int) PoolOption {
	return func(cfg *pgxpool.Config) {
		if n <= 0 {
			return
		}
		minConns := int32(n)
		if cfg.MaxConns > 0 && minConns > cfg.MaxConns {
			minConns = cfg.MaxConns
		}
		cfg.MinConns = minConns
	}
}

// Open 建立 pgxpool 连接池并 ping 验证。
func Open(ctx context.Context, dsn string, opts ...PoolOption) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool parse config: %w", err)
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	cfg.ConnConfig.Tracer = queryStatsTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool new: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg ping: %w", err)
	}
	if err := warmMinConns(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func warmMinConns(ctx context.Context, pool *pgxpool.Pool) error {
	target := pool.Config().MinConns
	if target <= 0 {
		return nil
	}
	conns := make([]*pgxpool.Conn, 0, target)
	defer func() {
		for _, conn := range conns {
			conn.Release()
		}
	}()
	for int32(len(conns)) < target {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return fmt.Errorf("prewarm pg connection %d/%d: %w", len(conns)+1, target, err)
		}
		conns = append(conns, conn)
	}
	return nil
}

func withTx(ctx context.Context, db sqlcgen.DBTX, op string, fn func(pgx.Tx) error) error {
	beginner, ok := db.(txBeginner)
	if !ok {
		return fmt.Errorf("%s: db does not support transactions", op)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s: %w", op, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", op, err)
	}
	committed = true
	return nil
}

// Migrate 用嵌入的迁移脚本将数据库迁移到最新版本。幂等：已最新时返回 nil。
func Migrate(dsn string) error {
	_, err := MigrateAndStatus(dsn)
	return err
}

// MigrateAndStatus 用嵌入迁移脚本迁移数据库，并返回迁移后的 schema 版本。
//
// Устойчивость запуска:
//   - ожидание advisory-lock драйвера продлено до schemaMigrationLockTimeout,
//     чтобы второй параллельный инстанс дождался своей очереди вместо падения;
//   - dirty-флаг самовосстанавливается: pgx5-драйвер выполняет каждую миграцию
//     в одной неявной транзакции, неудачная миграция полностью откатывается,
//     поэтому флаг можно снять и повторить попытку; повторная ошибка вернёт
//     реальную причину вместо вечной блокировки запуска ("Dirty database
//     version N. Fix and force version.").
func MigrateAndStatus(dsn string) (MigrationStatus, error) {
	src, err := iofs.New(deploy.Migrations, "migrations")
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("iofs source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, toPgx5DSN(dsn))
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("migrate new: %w", err)
	}
	m.LockTimeout = schemaMigrationLockTimeout
	defer m.Close()

	recovered := false
	const dirtyRetries = 2
	for attempt := 0; ; attempt++ {
		err = m.Up()
		if err == nil || errors.Is(err, migrate.ErrNoChange) {
			break
		}
		var dirtyErr migrate.ErrDirty
		if !errors.As(err, &dirtyErr) || attempt >= dirtyRetries {
			return MigrationStatus{}, fmt.Errorf("migrate up: %w", err)
		}
		// Прерванная миграция полностью откатилась (одна миграция = одна
		// неявная транзакция): снимаем dirty и откатываем счётчик на
		// предыдущую версию, чтобы прерванная миграция выполнилась
		// заново, а не была навсегда пропущена как «применённая».
		if uerr := clearDirtyFlag(dsn); uerr != nil {
			return MigrationStatus{}, fmt.Errorf("clear dirty flag: %w", uerr)
		}
		recovered = true
	}

	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return MigrationStatus{Empty: true}, nil
	}
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("migrate version: %w", err)
	}
	return MigrationStatus{Version: version, Dirty: dirty, RecoveredDirty: recovered}, nil
}

// clearDirtyFlag восстанавливает схему после прерванной миграции: снимает
// dirty-маркер и откатывает счётчик версий на предыдущую версию, чтобы
// прерванная миграция выполнилась заново при следующем Up. Безопасно: каждая
// миграция pgx5-драйвера выполняется в одной неявной транзакции, неудачная
// версия полностью откатывается. Вызывается только после ErrDirty, когда
// schema_migrations заведомо существует.
func clearDirtyFlag(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open pgx: %w", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET version = version - 1, dirty = false WHERE dirty AND version > 0`); err != nil {
		return fmt.Errorf("update schema_migrations: %w", err)
	}
	return nil
}

// toPgx5DSN 把 pgxpool 用的 postgres:// DSN 转成 golang-migrate pgx5 driver 所需的 pgx5:// scheme。
func toPgx5DSN(dsn string) string {
	if s, ok := strings.CutPrefix(dsn, "postgres://"); ok {
		return "pgx5://" + s
	}
	if s, ok := strings.CutPrefix(dsn, "postgresql://"); ok {
		return "pgx5://" + s
	}
	return dsn
}
