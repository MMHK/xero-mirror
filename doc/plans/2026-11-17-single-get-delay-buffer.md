# 單一實體延遲同步 Buffer

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-17  
**狀態**: 🟢 完成  
**優先級**: Medium

## 背景與目標

### 問題

目前 webhook 處理事件時：
- **Contacts / Invoices**：支援 `?IDs=` 批量查詢，由 `WebhookBuffer` 聚合後呼叫 `RunBatch`
- **CreditNotes / Payments / Accounts**：不支援 `?IDs=`，目前只記 Info log「實體不支援批量同步，交由 cron 增量處理」

但 cron job（`RunAllIncremental`）**不會消費 `sync_jobs` 表**，導致這些實體的 webhook 事件實際上沒有被處理。

### 目標

為不支援批量查詢的實體建立獨立的延遲同步機制：
1. 獨立 buffer，與 `WebhookBuffer` 分開
2. 按 `(entity, xero_id)` 去重
3. 延遲一段時間後，用 single get 端點逐一抓取
4. 順序執行，避免併發問題

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 新增 `SingleGetBuffer` 結構 | ✅ 完成 | — | `pkg/handler/single_buffer.go` |
| 2 | 實作 `Add` 方法：按 (entity, id) 去重 | ✅ 完成 | — | 用 map 去重，channel 傳事件 |
| 3 | 實作 delay loop：timer 到期後順序執行 | ✅ 完成 | — | 單一 goroutine，避免併發 |
| 4 | 新增 `Runner.RunSingle` 方法 | ✅ 完成 | — | 呼叫 single get 端點 |
| 5 | 修改 `WebhookHandler.dispatchToBuffer` | ✅ 完成 | — | 依實體分流到兩個 buffer |
| 6 | 修改 `serveDeps` 與 `shutdown` | ✅ 完成 | — | 管理新 buffer 的生命週期 |
| 7 | 更新 `config` 新增設定項 | ✅ 完成 | — | `SINGLE_BUFFER_DELAY` |
| 8 | 單元測試 | ✅ 完成 | — | 去重、延遲、順序執行 |

## 設計細節

### 1. `SingleGetBuffer` 結構

```go
// pkg/handler/single_buffer.go

type SingleGetBuffer struct {
    runner     *pkgsync.Runner
    delay      time.Duration  // 延遲時間，預設 5 秒
    maxPending int            // 最大待處理數量
    
    baseCtx context.Context
    events  chan singleItem
    done    chan struct{}
    closeOnce sync.Once
    mu      sync.Mutex
    closed  bool
    
    // pending 以 (entity, id) 為鍵去重
    pending map[string]map[string]struct{}  // entity -> set of IDs
    timer   *time.Timer
}

type singleItem struct {
    entity pkgsync.Entity
    xeroID string
}
```

### 2. 分流邏輯

```go
// webhook.go
func (h *WebhookHandler) dispatchToBuffer(targets []webhookTarget) {
    for _, t := range targets {
        if pkgsync.SupportsBatchIDs(pkgsync.Entity(t.entity)) {
            h.buffer.Add(t.tenantID, pkgsync.Entity(t.entity), t.xeroID)
        } else if h.singleBuffer != nil {
            h.singleBuffer.Add(t.tenantID, pkgsync.Entity(t.entity), t.xeroID)
        }
    }
}
```

### 3. `Runner.RunSingle` 方法

```go
// pkg/sync/sync.go

// RunSingle 用 single get 端點抓取單一實體；不推進 cursor。
func (r *Runner) RunSingle(ctx context.Context, tenantID string, entity Entity, id string) error {
    spec, ok := entitySpecs[entity]
    if !ok {
        return fmt.Errorf("單一同步 %s: 未知的實體", entity)
    }
    
    // 組合路徑：/CreditNotes/{id}、/Payments/{id}、/Accounts/{id}
    path := spec.path + "/" + id
    
    body, err := r.client.Get(ctx, tenantID, path, nil, nil)
    if err != nil {
        return fmt.Errorf("單一同步 %s/%s: %w", entity, id, err)
    }
    
    // 解析並寫入 DB
    // ...（需要處理 single get 的 response 格式）
}
```

### 4. 設定項

```go
// pkg/config/config.go

SingleBufferDelay  time.Duration  // 預設 5 * time.Second
SingleBufferSize   int            // 預設 100
```

環境變數：
- `SINGLE_BUFFER_DELAY`：延遲秒數
- `SINGLE_BUFFER_SIZE`：最大待處理數量

### 5. Response 格式差異

Single get 的 response 格式與 list 不同：
- List: `{"CreditNotes": [...]}`
- Single: `{"CreditNotes": [{...}]}` 或直接 `{...}`

需要確認 Xero 實際回傳格式，可能需要調整 `decodePage` 或新增 single decode 函式。

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-17 | ✅ 已批准 | 開始實作 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| 2026-11-17 | code-reviewer | 2 Minor：timer drain、多餘 mutex | ✅ 已修正 |

## 備註

- 不支援批量的實體：`CREDIT_NOTES`、`PAYMENTS`、`ACCOUNTS`
- Accounts 的 webhook 事件目前會被 `parseTargets` 略過（`webhookEntity` map 沒有 ACCOUNTS），所以實際只有 CreditNotes 和 Payments 會進入這個 buffer
- 延遲時間建議 5-10 秒，確保 Xero 已處理完變更
- 順序執行避免 API 限流問題
