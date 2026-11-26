package postgres

import (
        "os"
        "testing"
)

func TestStarGiftLifecycleMigrationsApply(t *testing.T) {
        dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
        if dsn == "" {
                t.Skip("set TELESRV_TEST_POSTGRES_DSN to run postgres integration test")
        }
        status, err := MigrateAndStatus(dsn)
        if err != nil {
                t.Fatalf("migrate star gift lifecycle schema: %v", err)
        }
        // Раунд 47: головная версия цепочки — 0202 (нормализация saved флага
        // messageActionStarGift). Обновляется вместе с каждой новой миграцией.
        if status.Dirty || status.Empty || status.Version != 202 {
                t.Fatalf("migration status = %+v, want clean version 202", status)
        }
}
