# DB Connection Pool 設定

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-18
**狀態**: 🟡 等待 Review
**優先級**: Medium

## 背景與目標

目前 `pkg/store/migrate.go` 的 `OpenMigrated` 在 `sql.Open` + `PingContext` 後直接回傳 `*sql.DB`，**沒有設定任何連線池參數**。Go 的 `database/sql` 預設值：

| 參數 | 預設值 | 風險 |
|------|--------|------|
| `MaxOpenConns` | 0（無限） | 高併發時可能開太多連線，超過 MySQL `max_connections` |
| `MaxIdleConns` | 2 | 空閒連線太少，頻繁建立新連線 |
| `ConnMaxLifetime` | 0（永久） | 連線可能活太久，被 MySQL `wait_timeout`（預設 8h）無聲斷開 |

**目標**：在 `OpenMigrated` 設定合理的連線池參數，確保連線穩定、不洩漏。

## 方案

**最簡方案：硬編碼合理預設值**。本服務是單實例常駐，不需要 env 可調。理由：

1. 遵循 AGENTS.md「不過度設計」原則
2. 連線池參數是底層基礎設施，不是業務邏輯
3. 若日後需要調整，改常數值即可，不需要引入 env 變數

### 預設值

```go
const (
    dbMaxOpenConns    = 25                  // 合理上限，避免爆 MySQL max_connections
    dbMaxIdleConns    = 10                  // 保留空閒連線，減少重建開銷
    dbConnMaxLifetime = 5 * time.Minute     // 遠小於 MySQL wait_timeout（8h），避免被無聲斷開
)
```

### 變更範圍

| # | 檔案 | 變更 |
|---|------|------|
| 1 | `pkg/store/migrate.go` | 在 `OpenMigrated` 的 `sql.Open` 之後、`PingContext` 之前，加入三行 pool 設定 |

### 程式碼變更

```go
// OpenMigrated 內，sql.Open 成功後：
db.SetMaxOpenConns(dbMaxOpenConns)
db.SetMaxIdleConns(dbMaxIdleConns)
db.SetConnMaxLifetime(dbConnMaxLifetime)
```

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 在 `OpenMigrated` 加入連線池設定（3 個常數 + 3 行 setter） | ⬜ 待處理 | — | 改 `pkg/store/migrate.go` |
| 2 | 確認 `go build` 通過、`go test ./...` 通過 | ⬜ 待處理 | — | |
| 3 | Code review | ⬜ 待處理 | — | |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-18 | ⏳ 等待 Review | |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|

## 備註

- 不需要新增 env 變數（遵循「不過度設計」）
- 不需要改 `doc/spec.md`（這是底層實作細節，不影響架構契約）
- 不需要新增測試（只是設定 sql.DB 的內建方法，沒有自訂邏輯）
