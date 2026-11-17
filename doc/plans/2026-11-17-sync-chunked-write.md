# sync 寫入分塊與增量能力補齊

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-17
**狀態**: 🟢 完成（A+B＋日誌等級；2026-11-17）
**優先級**: High

## 背景與目標

現況（已審查確認）：

- `Runner.Run` 會等 `GetPages` 把所有頁抓完，累積在 `pageApplier.batch` 記憶體後，才呼叫一次 `store.ApplyBatch`（`pkg/sync/sync.go:86-122`、`317-336`）。
- `ApplyBatch` 在單一交易內**逐筆** `ExecContext` 五張表，最後才推進 cursor（`pkg/store/batch.go:27-80`）。
- 只有 Invoices 開啟 `If-Modified-Since`；CreditNotes／Payments／Contacts／Accounts 每輪 cron 都是全量抓取（`pkg/sync/sync.go:61-67`）。
- `doc/spec.md:179-180` 記載 CreditNotes／Payments **支援** `If-Modified-Since`，但程式關閉，文件與程式不一致；`doc/spec.md:181` 的 Contacts 仍標「待核對」。

目標：

1. 讓 CreditNotes／Payments 走真正的增量，降低每輪 API 呼叫量與 429 等待。
2. 把「全抓後一次寫」改成分塊寫入：記憶體有上限、交易短、失敗仍可冪等重跑。
3. 維持既有正確性保證：資料與 cursor 仍同交易提交；抓取失敗或中途失敗時 cursor 不前進。

**非目標**

- 不改 CLI 介面、除 `LOG_LEVEL` 外不加環境變數、不改 DB schema、不引入新依賴。
- 不改 webhook 決策流程、不改 admin／cron 的觸發方式。
- 不為 Contacts／Accounts 開啟未核對的能力。
- 不改成「邊抓邊推進 cursor」（見風險 R4）。

## 設計

### A. CreditNotes／Payments 開啟 `If-Modified-Since`

1. 先核對：以 `doc/spec.md` §5 矩陣與 Xero 官方文件為準；必要時用 opt-in live test（`TEST_XERO_ACCESS_TOKEN`／`TEST_XERO_TENANT_ID`，只讀、最多 3 次呼叫）確認兩端點接受 `If-Modified-Since` 且仍正常分頁。
2. 核對通過才把 `entitySpecs` 的 `supportsSince` 改為 `true`（僅 CreditNotes、Payments）。
3. 核對結果與日期回寫 `doc/spec.md` §5 矩陣；Contacts／Accounts 不動。
4. 首跑（cursor 為 `nil`）仍全量，行為不變。

### B. 分塊寫入

**store 層**

- 在 `pkg/store/bulk.go` 以既有單列 SQL 模板**動態衍生**多列 upsert（每句最多 `bulkUpsertRows = 100` 列），不另維護第二份欄位清單：
  `bulkUpsertContacts`、`bulkUpsertAccounts`、`bulkUpsertInvoices`、`bulkUpsertCreditNotes`、`bulkUpsertPayments`。
- 沿用既有 `ON DUPLICATE KEY UPDATE` 語意與欄位；SQL 只留在 `pkg/store`，placeholder 用 `?` 拼接固定格式，禁止字串拼接值。
- 保留既有單列函式（tenant、sync_state、webhook 等仍使用）。
- `ApplyBatch` 仍維持「單一交易、原子提交」，只是內部改呼叫多列版本。

**sync 層**

- `pageApplier` 增加 `ctx`、`store` 與 `pending` 狀態；`writeChunkRows = 500`（約每 5 頁 flush 一次，記憶體上限約 500 列 + 單頁 JSON）。
- `handle` 累積到 `writeChunkRows` 就 flush：`ApplyBatch` 帶 `Cursor = nil`，成功後清空待寫 slice；flush 失敗立即回錯。
- `Run`／`RunBatch` 結束時再 flush 一次：僅此時帶入本輪最大 `xero_updated_at` 作為 cursor。
- `total == 0` 維持現行語意（不呼叫 `ApplyBatch`，Phase C 再討論）。
- 記錄：每次 flush 記 `tenant`／`entity`／`count`；整輪結束維持原本「同步完成」log。

**失敗語意（需寫進文件）**

- 中途失敗時：先前已 flush 的 chunk 留在 DB（部分新資料可見），但 cursor 不前進。
- 下次重跑因 cursor 未動仍會全量／增量重抓同一批，`ON DUPLICATE KEY UPDATE` 冪等覆蓋，不會重複列。
- 這與現行「全有或全無」的差異是刻意的（換取記憶體與交易時間）；mirror 資料表可接受短期部分更新。

### D. 日誌等級與分塊 debug log（2026-11-17 追加）

- 新增可選環境變數 `LOG_LEVEL`：`debug|info|warn|error`，大小寫不敏感，預設 `info`；非法值啟動即報錯。
- `pkg/config` 解析成 `slog.Level`；`runSync`／`runServe`／`runAuth` 在載入設定後立即 `slog.SetDefault`，`main.go` 載入設定前維持 Info。
- 分塊寫入每批加 `slog.Debug`（store 多列 upsert、sync flush／page），欄位帶 `table`／`rows`／`total`（sync 另帶 `tenant`／`entity`）；不印 SQL 值、不逐筆印。
- 整批完成仍維持一筆 Info；不新增 CLI flag。

### C. 可選硬化（需另行批准，不與 A/B 同批）

- `If-Modified-Since` 邊界：若核對發現 Xero 語意為「嚴格大於」，cursor 回退 1 秒避免同秒更新漏抓。
- `total == 0` 時仍更新 `last_run_at`／`status`（目前空批次直接 return，監控看不出跑過）。
- 每 chunk 進度 log（`page`／`written`），資料量大時便於追蹤。

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 0 | 先提交現有未提交的 `--entity` 相關變更，避免與本計畫混在一起 | ⏭️ 跳過 | — | 使用者未回覆；實作期間不執行 commit |
| 1 | 核對 CreditNotes／Payments 的 `If-Modified-Since`（文件＋opt-in live test），回寫 spec §5 | ✅ 完成 | — | 官方文件已確認（2026-11-17）：`CreditNotes`、`Payments` 在支援清單內 |
| 2 | `pkg/sync`：CreditNotes／Payments `supportsSince=true`，新增表驅動測試（有 cursor 送 header、無 cursor 不送） | ✅ 完成 | general | code-reviewer PASS（2026-11-17） |
| 3 | `pkg/store`：新增五張表多列 upsert（`bulkUpsertRows=100`），`ApplyBatch` 改用多列版本；補冪等測試；每 chunk 加 `slog.Debug` | ✅ 完成 | general | code-reviewer 覆核 PASS（2026-11-17，含 W1／W2／W4／W6 硬化） |
| 4 | `pkg/sync`：`pageApplier` 分塊 flush（`writeChunkRows=500`），cursor 只在最後一次 flush 帶入；`Run`／`RunBatch` 共用；flush／page 加 `slog.Debug` | ✅ 完成 | general | code-reviewer PASS（2026-11-17） |
| 4b | `pkg/config` 新增 `LOG_LEVEL` 解析，`commands.go` 三個 subcommand 套用；`.env.example`、`doc/spec.md` §8、`doc/dev-setup.md` 更新；補 `parseLogLevel` 測試 | ✅ 完成 | general | code-reviewer 覆核 PASS（2026-11-17） |
| 5 | 測試（3-5 案例）：多次 flush、中途失敗保留舊 cursor 且前段資料存在、同批重跑冪等 | ✅ 完成 | general | 600 筆成功／中途失敗 2 案例；store 層冪等另有覆蓋 |
| 6 | 文件同步：`doc/spec.md` §5、§8、`doc/modules/sync.md` §3／§5／§7／§10、`.env.example`、`doc/dev-setup.md` | ✅ 完成 | general | code-reviewer 確認無舊敘述殘留 |
| 7 | `code-reviewer` 對照本計畫審查，修正後再標 ✅ | ✅ 完成 | code-reviewer | Phase A／store／LOG_LEVEL／sync 分塊四批皆 PASS |
| 8 | 驗證：`go fmt ./...`、`go vet ./...`、`go test ./...`、DB 測試 | ✅ 完成 | — | 本機與測試 MySQL 全綠 |

## 驗證方式

```bash
go build -o xero-mirror.exe .
go fmt ./... && go vet ./...
go test ./...                                  # 無 DB 時 DB 案例 skip

docker compose -f docker-compose.test.yml up -d
TEST_MYSQL_DSN="root:root@tcp(127.0.0.1:13315)/xero_mirror_test?parseTime=true&loc=UTC" \
  go test ./pkg/store ./pkg/sync -count=1

# 可選 live（只讀，消耗少量配額）
TEST_XERO_ACCESS_TOKEN=... TEST_XERO_TENANT_ID=... go test ./pkg/xero -run TestLive -v

# 日誌等級（分塊 debug log）
LOG_LEVEL=debug go run . sync --entity creditnotes
LOG_LEVEL=loud go run . sync            # 必須在連 DB 前回「LOG_LEVEL 不合法」
```

人工驗收：

1. 同一 tenant 連跑兩次 `go run . sync --entity creditnotes`：第二次請求明顯變少、不再抓全歷史，`xero_credit_notes` 列數不變。
2. `xero_payments` 同上。
3. 檢視 log 可見分塊寫入筆數；`sync_state.cursor_time` 只在整輪成功後前進。
4. 故意讓 mock／測試在第 2 頁失敗：前段資料存在、cursor 不變，重跑後資料一致。

## 風險與取捨

| # | 風險 | 處理 |
|---|------|------|
| R1 | 分塊後中途失敗會留下部分新資料 | 刻意取捨；cursor 未動且 upsert 冪等；文件寫明，人工可重跑 |
| R2 | 多列 SQL 的 placeholder 數量／欄位順序寫錯 | 由 `pkg/store` 生成固定格式並以 DB 整合測試驗證；500 列 × 14 欄遠低於 65535 placeholder 上限 |
| R3 | 交易變多、log 變吵 | 每 chunk 一筆 `slog.Debug`，整批一筆 Info；不逐筆記 log |
| R4 | 分頁不保證依 `UpdatedAtUTC` 排序，邊抓邊推 cursor 會漏資料 | 本計畫**不**在抓取中推進 cursor，只在整輪最後 flush 帶 cursor |
| R5 | Xero 忽略 `If-Modified-Since` | 核對＋live test；即使被忽略也只是退化為全量，不會資料錯誤 |
| R6 | IMS 同秒邊界漏抓 | Phase C 的 cursor 回退 1 秒；若無法確認語意則先保留現況並記錄 |
| R7 | 與目前未提交的 `--entity` 變更衝突 | 任務 0 先 commit，再開新分支／工作區 |
| R8 | 大 tenant（約 1.3 萬筆 CN） flush 次數多 | 500 列／次約 26 次交易，可接受；必要時只調常數 |
| R9 | `LOG_LEVEL=debug` 讓 log 量暴增 | 僅分塊與分頁層級 Debug；預設 info 不受影響 |
| R10 | 新增環境變數未同步文件 | `.env.example`、spec §8、dev-setup 同批更新；非法值啟動報錯 |

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-17 | ✅ 已批准（A+B） | 使用者明確選擇 A+B；C 不在此批 |
| 2026-11-17 | ✅ 追加批准 | 分塊寫入加 debug log；補完 `LOG_LEVEL` 設定與 env |
| 2026-11-17 | 🔄 補充核對 | `doc/spec.md:181` 的 Contacts 官方文件亦列在 `If-Modified-Since` 支援清單，但本批不動 Contacts，維持全量 |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|
| 2026-11-17 | code-reviewer（Phase A） | PASS；3 項非阻擋建議：① spec 對 Contacts 仍寫「待核對」與核對紀錄矛盾 ② CN／Payments 矩陣列了 `where` 但 `monthFilter=false` 易誤導 ③ 測試可改用 `cursor.UTC()` 與 header 存在性判斷 | ① ② 併入任務 6 文件；③ 任務 5 追加時順手硬化 |
| 2026-11-17 | code-reviewer（store bulk） | PASS；W1 模板切割無單元測試、W2 placeholder 未交叉驗證、W4 錯誤前綴重複、W6 測試未驗證更新語意、W3／W7 註解 | W1／W2／W4／W6 交回原 agent 硬化；W5 計畫措辭已修正 |
| 2026-11-17 | code-reviewer（LOG_LEVEL） | PASS；建議 dev-setup 勿預設 debug、補 `Load()` 層級測試、`.env.example` 用詞、spec §8 敘述 | 交回原 agent 修正，覆核 PASS |
| 2026-11-17 | code-reviewer（sync 分塊） | PASS；9 點建議（文件 §5／§10 舊敘述、`ApplyBatch` Info 降 Debug、死碼、log 欄位、測試門檻、清理衛生） | 1-7 交回修正並完成任務 6 文件；8／9 記為後續 |
| 2026-11-17 | code-reviewer（收尾覆核） | PASS — 整體可標記完成；3 點非阻擋（spec §3 交易措辭、modules `Cursor` 模式括註、測試 `Equal(500)`） | 交回原 agent 最後修正 |

## 備註

- `If-Modified-Since` 核對來源：<https://developer.xero.com/documentation/best-practices/api-call-efficiencies/if-modified-since/>（2026-11-17 讀取）。支援清單含 Invoices、Contacts、CreditNotes、Payments；本批只開 CreditNotes／Payments，Contacts 留待後續計畫。
- `writeChunkRows = 500`、`bulkUpsertRows = 100` 只放常數，不加環境變數；若要調整，改常數並跑測試即可。
- store 多列 SQL 改為由 `queries.go` 單列模板動態衍生（非在 `queries.go` 新增五份常數），單一真相；模板切割標記是硬約定，改格式須同步 `bulkStatement` 並跑無 DB 單元測試。
- 分塊只影響「一次 Run 的寫入節奏」，`Run`／`RunBatch` 對外 API 與旗標不變。
- `Accounts` 不分頁且受 8MB 回應上限影響，屬另一個問題；本計畫不處理，僅在文件中保留已知限制。
- 目前工作區已有未提交的 `--entity` 變更（`doc/plans/2026-11-17-sync-entity-flag.md` 已標完成），建議先 commit 再開始本計畫。
