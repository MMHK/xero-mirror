package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

// Store 是手寫 SQL 存取層，唯一碰 SQL 的地方。
type Store struct {
	db *sql.DB
}

// NewStore 包裝既有的 *sql.DB；連線生命週期由呼叫端管理。
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Ping 檢查資料庫連線是否可用，供 /health 健康檢查使用。
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping 資料庫: %w", err)
	}
	return nil
}

// WithTx 在單一交易內執行 fn：fn 回錯或 Commit 失敗都 Rollback。
// 供同步編排把實體 upsert 與 sync_state 水位放進同一個交易。
func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("開始資料庫交易: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			slog.Warn("transaction rollback failed", "err", rerr)
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交交易: %w", err)
	}
	committed = true
	return nil
}

// amountOrZero 把空字串金額正規化為 "0"，避免 MySQL 綁定空字串到 DECIMAL 時報錯。
func amountOrZero(v string) string {
	if v == "" {
		return "0"
	}
	return v
}
