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
	// Раунд 31: 0187_stars_transaction_public_ids — публичные id, флаг
	// refunded и индексы транзакций.
	if status.Dirty || status.Empty || status.Version != 187 {
		t.Fatalf("migration status = %+v, want clean version 187", status)
	}
}
