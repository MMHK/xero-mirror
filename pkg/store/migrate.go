package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL driver，本專案唯一 driver
)

//go:embed all:migrations
var migrationFS embed.FS

const (
	// migrateLockName、migrateLockTimeout 是 migration advisory lock 名稱與等待秒數。
	migrateLockName    = "xero-mirror:migrate"
	migrateLockTimeout = 30
	// pingTimeout 是建立 MySQL 連線後的驗證逾時。
	pingTimeout = 10 * time.Second

	// dbMaxOpenConns 是 MySQL 連線池最大開啟連線數。
	dbMaxOpenConns = 25
	// dbMaxIdleConns 是 MySQL 連線池最大空閒連線數。
	dbMaxIdleConns = 10
	// dbConnMaxLifetime 是連線最大存活時間，須遠小於 MySQL wait_timeout（預設 8h）。
	dbConnMaxLifetime = 5 * time.Minute
)

// OpenMigrated 開啟 MySQL、驗證可連線並套用 migration；任何失敗都會關閉連線。
func OpenMigrated(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("開啟 MySQL 連線: %w", err)
	}
	db.SetMaxOpenConns(dbMaxOpenConns)
	db.SetMaxIdleConns(dbMaxIdleConns)
	db.SetConnMaxLifetime(dbConnMaxLifetime)
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("連線 MySQL: %w", err)
	}
	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Migrate 建立版本表、取得 advisory lock，並依檔名順序套用未執行的 up migration。
// GET_LOCK／RELEASE_LOCK 是 session 級，全程綁同一條 *sql.Conn，否則鎖會洩漏在別條連線上。
func Migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("取得 migration 專用連線: %w", err)
	}
	defer conn.Close()

	if err := ensureVersionTable(ctx, conn); err != nil {
		return err
	}
	if err := acquireLock(ctx, conn); err != nil {
		return err
	}
	defer releaseLock(ctx, conn)

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}
	versions, err := upVersions()
	if err != nil {
		return err
	}
	warnUnknownVersions(applied, versions)
	n := 0
	for _, version := range versions {
		if applied[version] {
			continue
		}
		if err := applyFile(ctx, conn, version); err != nil {
			return err
		}
		slog.Info("migration completed", "version", version)
		n++
	}
	slog.Info("all migrations completed", "applied", n)
	return nil
}

// ensureVersionTable 建立 schema_migrations 版本表（若不存在）。
func ensureVersionTable(ctx context.Context, conn *sql.Conn) error {
	const ddl = "CREATE TABLE IF NOT EXISTS schema_migrations (" +
		"version varchar(64) NOT NULL, " +
		"applied_at datetime(6) NOT NULL, " +
		"PRIMARY KEY (version)" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"
	if _, err := conn.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("建立 schema_migrations: %w", err)
	}
	return nil
}

// acquireLock 取得 migration advisory lock；拿不到鎖就回錯誤，避免多實例同時 migrate。
func acquireLock(ctx context.Context, conn *sql.Conn) error {
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", migrateLockName, migrateLockTimeout).Scan(&got); err != nil {
		return fmt.Errorf("取得 migration 鎖: %w", err)
	}
	if !got.Valid || got.Int64 != 1 {
		return fmt.Errorf("取得 migration 鎖失敗（可能被其他實例佔用）")
	}
	return nil
}

// releaseLock 釋放 migration advisory lock；失敗只警告，不影響已完成的 migration。
func releaseLock(ctx context.Context, conn *sql.Conn) {
	var released sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", migrateLockName).Scan(&released); err != nil {
		slog.Warn("release migration lock failed", "err", err)
		return
	}
	if !released.Valid || released.Int64 != 1 {
		slog.Warn("release migration lock failed", "released", released)
	}
}

// appliedVersions 回傳已套用的 migration 版本集合。
func appliedVersions(ctx context.Context, conn *sql.Conn) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("查詢已套用版本: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("讀取已套用版本: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("讀取已套用版本: %w", err)
	}
	return applied, nil
}

// upVersions 回傳依檔名排序的 up migration 版本名稱（去掉 .up.sql）。
// 每個 .up.sql 都必須有對應的 .down.sql，缺檔直接回錯誤。
func upVersions() ([]string, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("讀取 migration 目錄: %w", err)
	}
	up := make(map[string]bool, len(entries))
	down := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(name, ".up.sql") {
			up[strings.TrimSuffix(name, ".up.sql")] = true
		} else if strings.HasSuffix(name, ".down.sql") {
			down[strings.TrimSuffix(name, ".down.sql")] = true
		}
	}
	versions := make([]string, 0, len(up))
	for version := range up {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	for _, version := range versions {
		if !down[version] {
			return nil, fmt.Errorf("migration %s 缺少對應的 .down.sql", version)
		}
	}
	return versions, nil
}

// warnUnknownVersions 對 DB 已記錄但檔案已移除的版本只警告，不視為錯誤。
func warnUnknownVersions(applied map[string]bool, versions []string) {
	known := make(map[string]bool, len(versions))
	for _, version := range versions {
		known[version] = true
	}
	for version := range applied {
		if !known[version] {
			slog.Warn("orphaned migration version in DB", "version", version)
		}
	}
}

// applyFile 讀取指定版本的 up 檔、逐句執行，成功後才寫入版本紀錄。
func applyFile(ctx context.Context, conn *sql.Conn, version string) error {
	raw, err := migrationFS.ReadFile("migrations/" + version + ".up.sql")
	if err != nil {
		return fmt.Errorf("讀取 migration %s: %w", version, err)
	}
	for i, stmt := range splitStatements(string(raw)) {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("套用 migration %s 第 %d 句: %w", version, i+1, err)
		}
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
		version, time.Now().UTC()); err != nil {
		return fmt.Errorf("migration %s 的 DDL 已執行但版本未記錄，請人工確認後補 INSERT 或執行 down 後重跑: %w", version, err)
	}
	return nil
}

// splitStatements 以分號切開 SQL，略過空行與 -- 註解行。
// 僅支援 `;` 結尾語句；不支援區塊註解與行內 --，新增註解須獨占整行。
func splitStatements(raw string) []string {
	lines := make([]string, 0, 16)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		lines = append(lines, line)
	}
	parts := strings.Split(strings.Join(lines, "\n"), ";")
	stmts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			stmts = append(stmts, part)
		}
	}
	return stmts
}
