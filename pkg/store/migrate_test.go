package store

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"testing"

	"github.com/mmhk/xero-mirror/tests"
	"github.com/stretchr/testify/require"
)

func TestMigrateIdempotent(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未設定 TEST_MYSQL_DSN")
	}
	tests.RequireTestDatabase(t, dsn)
	ctx := context.Background()

	tests.LockTestDatabase(t, dsn)
	cleaner, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	defer cleaner.Close()
	dropAllTables(t, cleaner)

	db, err := OpenMigrated(ctx, dsn)
	require.NoError(t, err)
	defer db.Close()

	upFiles, err := fs.Glob(migrationFS, "migrations/*.up.sql")
	require.NoError(t, err)

	first := countMigrations(t, db)
	require.Equal(t, len(upFiles), first, "首次 migrate 應套用全部 up 檔")
	require.NoError(t, Migrate(ctx, db))
	second := countMigrations(t, db)
	require.Equal(t, len(upFiles), second, "重跑 migrate 不得新增版本")
}

// dropAllTables 清空測試庫的 11 張表，讓測試可重複執行。
func dropAllTables(t *testing.T, db *sql.DB) {
	t.Helper()
	tables := []string{
		"schema_migrations", "xero_tenants", "sync_state", "webhook_events", "sync_jobs", "failed_sync_jobs",
		"xero_contacts", "xero_accounts", "xero_invoices", "xero_credit_notes", "xero_payments",
	}
	for _, table := range tables {
		_, err := db.Exec("DROP TABLE IF EXISTS " + table)
		require.NoError(t, err, "清理資料表 %s", table)
	}
}

// countMigrations 回傳 schema_migrations 的列數。
func countMigrations(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n))
	return n
}
