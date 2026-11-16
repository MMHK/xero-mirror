package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mmhk/xero-mirror/pkg/config"
	"github.com/mmhk/xero-mirror/pkg/store"
)

const syncUsageText = "用法: xero-mirror sync [-month YYYY-MM]\n" +
	"  --month YYYY-MM  回補單月；省略則依 cursor 增量\n"

// runSync 解析 --month 後載入設定、連上 MySQL 並執行同步（同步核心尚待任務 8）。
func runSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, syncUsageText)
	}
	month := fs.String("month", "", "回補月份，格式 YYYY-MM")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析 sync 參數: %w", err)
	}
	if *month != "" {
		if _, err := time.Parse("2006-01", *month); err != nil || len(*month) != 7 {
			return fmt.Errorf("--month 格式必須是 YYYY-MM: %q", *month)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("載入設定失敗: %w", err)
	}
	db, err := store.OpenMigrated(ctx, cfg.MySQLDSN)
	if err != nil {
		return fmt.Errorf("初始化資料庫失敗: %w", err)
	}
	defer db.Close()
	return fmt.Errorf("sync 尚未實作")
}

// runServe 是 serve 的接線入口；cron 與 webhook（任務 9）尚未實作。
func runServe(_ context.Context, _ []string) error {
	return fmt.Errorf("serve 尚未實作")
}

// runAuth 是 auth 的接線入口；授權流程（任務 10）尚未實作。
func runAuth(_ context.Context, _ []string) error {
	return fmt.Errorf("auth 尚未實作")
}
