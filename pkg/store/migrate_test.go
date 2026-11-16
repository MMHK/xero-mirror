package store

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// requireTestDatabase 要求 TEST_MYSQL_DSN 指向名字含 _test 的庫，避免清理 helper 誤刪正式資料。
func requireTestDatabase(t *testing.T, dsn string) {
	t.Helper()
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Skipf("TEST_MYSQL_DSN 無法解析: %v", err)
	}
	if !strings.Contains(cfg.DBName, "_test") {
		t.Skip("TEST_MYSQL_DSN 未指向測試庫")
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未設定 TEST_MYSQL_DSN")
	}
	requireTestDatabase(t, dsn)
	ctx := context.Background()

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

// dropAllTables 清空測試庫的 10 張表，讓測試可重複執行。
func dropAllTables(t *testing.T, db *sql.DB) {
	t.Helper()
	tables := []string{
		"schema_migrations", "xero_tenants", "sync_state", "webhook_events", "sync_jobs",
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
