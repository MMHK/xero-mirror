// Package tests 提供跨 package 共用的測試工具；只依賴 stdlib、MySQL driver 與 testify。
// 刻意不 import pkg/store，避免 store 套件的測試出現 import cycle。
package tests

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

const (
	testLockName = "xero-mirror:test"
	testLockWait = 60
)

// RequireTestDatabase 要求 dsn 指向名字含 _test 的庫，避免清理 helper 誤刪正式資料。
func RequireTestDatabase(t *testing.T, dsn string) {
	t.Helper()
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Skipf("TEST_MYSQL_DSN 無法解析: %v", err)
	}
	if !strings.Contains(cfg.DBName, "_test") {
		t.Skip("TEST_MYSQL_DSN 未指向測試庫")
	}
}

// LockTestDatabase 取測試庫 advisory lock，避免跨 package 的 DROP TABLE／migration 互踩；
// 取鎖失敗（例如 MySQL 未啟動）時跳過測試。回傳的釋放函式可重複呼叫。
func LockTestDatabase(t *testing.T, dsn string) func() {
	t.Helper()
	RequireTestDatabase(t, dsn)
	ctx := context.Background()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Skipf("開啟測試庫失敗: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Skipf("取得測試庫連線失敗: %v", err)
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx,
		"SELECT GET_LOCK(?, ?)", testLockName, testLockWait).Scan(&got); err != nil || !got.Valid || got.Int64 != 1 {
		_ = conn.Close()
		_ = db.Close()
		t.Skipf("取得測試庫鎖失敗: err=%v got=%v", err, got)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			var released sql.NullInt64
			_ = conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", testLockName).Scan(&released)
			_ = conn.Close()
			_ = db.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// OpenTestDB 開啟測試庫連線並在測試結束時關閉；非測試庫或連不上時跳過。
func OpenTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	RequireTestDatabase(t, dsn)
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Skipf("連線測試庫失敗: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
