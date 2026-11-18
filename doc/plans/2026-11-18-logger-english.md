# Logger 輸出從中文改為英文

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-18
**狀態**: 🟢 完成
**優先級**: Medium

## 背景與目標

目前專案所有 `slog` 日誌訊息使用繁體中文。用戶要求將所有 log 訊息改為英文，以利國際化或日誌分析工具整合。

### 目標

1. 將所有 `slog.Info/Warn/Error/Debug` 的訊息字串從中文改為英文
2. 更新 `AGENTS.md` 中的日誌語言規定（繁體中文 → 英文）
3. 保持結構化欄位（key）不變，只改訊息字串

## 影響範圍

共 **64 處** log 呼叫，分布在 **14 個檔案**：

| 檔案 | log 數量 |
|------|---------|
| `commands.go` | 18 |
| `main.go` | 1 |
| `pkg/sync/sync.go` | 14 |
| `pkg/store/store.go` | 1 |
| `pkg/handler/webhook.go` | 7 |
| `pkg/handler/sync_queue.go` | 5 |
| `pkg/handler/page.go` | 1 |
| `pkg/xero/auth.go` | 4 |
| `pkg/handler/health.go` | 1 |
| `pkg/handler/admin.go` | 2 |
| `pkg/store/bulk.go` | 1 |
| `pkg/xero/token.go` | 3 |
| `pkg/store/batch.go` | 1 |
| `pkg/store/migrate.go` | 5 |

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 更新 `AGENTS.md` 日誌語言規定 | ✅ 完成 | general | 第 4 節「日誌」與第 0 節「回覆語言」 |
| 2 | 替換 `commands.go` 的中文 log | ✅ 完成 | general | 16 處 |
| 3 | 替換 `main.go` 的中文 log | ✅ 完成 | general | 1 處 |
| 4 | 替換 `pkg/sync/sync.go` 的中文 log | ✅ 完成 | general | 14 處 |
| 5 | 替換 `pkg/store/*.go` 的中文 log | ✅ 完成 | general | store.go, bulk.go, batch.go, migrate.go 共 8 處 |
| 6 | 替換 `pkg/handler/*.go` 的中文 log | ✅ 完成 | general | webhook.go, sync_queue.go, page.go, health.go, admin.go 共 16 處 |
| 7 | 替換 `pkg/xero/*.go` 的中文 log | ✅ 完成 | general | auth.go, token.go 共 7 處 |
| 8 | Code Review | ✅ 完成 | code-reviewer | 通過，無需修改 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-18 | ⏳ 等待 Review | |
| 2026-11-18 | ✅ 已批准 | 用戶確認改為英文 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| 2026-11-18 | code-reviewer | 無問題，翻譯品質良好 | 審查通過 |
| 2026-11-18 | code-reviewer | 2 處低優先建議改善 | 已修正：`sync_queue.go:213` 改為 `"move to failed_sync_jobs failed"`；`migrate.go:187` 改為 `"orphaned migration version in DB"` |

## 備註

- 結構化欄位 key（如 `"tenant"`, `"entity"`, `"count"`）保持英文不變
- 只改訊息字串，不改 log level 或欄位結構
- 翻譯原則：保持語意精確，使用簡潔的英文短句
