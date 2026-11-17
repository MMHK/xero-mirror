// pkg/handler 的 webhook buffer：收集 webhook 事件的 entry IDs 並去重，
// 達 batch size 或 timer 到期後轉 pkg/sync 批量同步，避免每個事件都打一次 API。
package handler

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	pkgsync "github.com/mmhk/xero-mirror/pkg/sync"
)

const (
	// bufferEventsCap 是事件 channel 容量；滿了代表 event loop 來不及處理，
	// 新事件直接丟棄（事件已入列 sync_jobs，cron 仍可補抓），不得阻塞 HTTP handler。
	bufferEventsCap = 1024
)

// bufferItem 是單一 webhook 事件在 buffer 內的最小表示。
type bufferItem struct {
	tenantID string
	entity   pkgsync.Entity
	xeroID   string
}

// WebhookBuffer 以單一背景 goroutine 維護各 (tenant, entity) 群組的待同步 IDs。
// pending 只在 event loop 內存取，故不需額外鎖保護。
type WebhookBuffer struct {
	runner     *pkgsync.Runner
	batchSize  int
	flushDelay time.Duration

	// baseCtx 建構期設定後唯讀，loop 所有 flush 皆用它，避免與 Shutdown 產生資料競爭。
	baseCtx context.Context

	events    chan bufferItem
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	closed    bool

	pending map[string]map[pkgsync.Entity]map[string]struct{}
	timer   *time.Timer
}

// NewWebhookBuffer 建立並啟動 webhook buffer；ctx 作為 flush 同步呼叫的基底 context
// （建構後唯讀），Shutdown 傳入的 ctx 只控制關閉等待上限，不影響 flush。
func NewWebhookBuffer(ctx context.Context, runner *pkgsync.Runner, batchSize int, flushDelay time.Duration) *WebhookBuffer {
	b := &WebhookBuffer{
		runner:     runner,
		batchSize:  batchSize,
		flushDelay: flushDelay,
		baseCtx:    ctx,
		events:     make(chan bufferItem, bufferEventsCap),
		done:       make(chan struct{}),
		pending:    make(map[string]map[pkgsync.Entity]map[string]struct{}),
	}
	go b.loop()
	return b
}

// Add 加入一個 entry id；空白值與已關閉後的呼叫直接丟棄，channel 滿時記警告後丟棄，
// 永不阻塞 HTTP handler。
func (b *WebhookBuffer) Add(tenantID string, entity pkgsync.Entity, xeroID string) {
	tenantID = strings.TrimSpace(tenantID)
	xeroID = strings.TrimSpace(xeroID)
	if tenantID == "" || xeroID == "" {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	item := bufferItem{tenantID: tenantID, entity: entity, xeroID: xeroID}
	select {
	case b.events <- item:
	default:
		slog.Warn("webhook buffer 已滿，事件已丟棄，交由 cron 增量處理",
			"tenant", tenantID, "entity", entity)
	}
	b.mu.Unlock()
}

// Shutdown 關閉事件 channel，等待 event loop 把剩餘 pending flush 完並結束；
// ctx 僅用於等待 done 的逾時／取消控制，逾時或取消時回 ctx.Err()，不寫入任何
// 供 loop 讀取的共享欄位。可重複呼叫，不會 panic。
func (b *WebhookBuffer) Shutdown(ctx context.Context) error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		close(b.events)
	})
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loop 是唯一的背景 goroutine：串行處理事件與 flush，不另起未受控 goroutine。
func (b *WebhookBuffer) loop() {
	defer close(b.done)
	var timerC <-chan time.Time
	for {
		select {
		case item, ok := <-b.events:
			if !ok {
				b.flush(b.baseCtx)
				return
			}
			timerC = b.ingest(item, timerC)
		case <-timerC:
			timerC = nil
			b.timer = nil
			b.flush(b.baseCtx)
		}
	}
}

// ingest 把事件併入對應 (tenant, entity) 群組並去重；首筆事件啟動 timer，
// 任一群組累積達 batchSize 立即 flush 全部 pending。回傳新的 timer channel。
func (b *WebhookBuffer) ingest(item bufferItem, timerC <-chan time.Time) <-chan time.Time {
	entities, ok := b.pending[item.tenantID]
	if !ok {
		entities = make(map[pkgsync.Entity]map[string]struct{})
		b.pending[item.tenantID] = entities
	}
	ids, ok := entities[item.entity]
	if !ok {
		ids = make(map[string]struct{})
		entities[item.entity] = ids
	}
	ids[item.xeroID] = struct{}{}
	if b.timer == nil {
		b.timer = time.NewTimer(b.flushDelay)
		timerC = b.timer.C
	}
	if len(ids) >= b.batchSize {
		drainTimer(b.timer)
		b.timer = nil
		b.flush(b.baseCtx)
		return nil
	}
	return timerC
}

// drainTimer 停止並排空 timer，避免已觸發的訊號殘留造成誤 flush。
func drainTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// flush 清空全部 pending 並逐群組同步；單一群組失敗只記警告，不中斷其他群組。
// 不支援批量 IDs 的實體不呼叫 API，保留 sync_jobs 交由 cron 增量處理。
func (b *WebhookBuffer) flush(ctx context.Context) {
	pending := b.pending
	b.pending = make(map[string]map[pkgsync.Entity]map[string]struct{})
	for tenantID, entities := range pending {
		for entity, idSet := range entities {
			if !pkgsync.SupportsBatchIDs(entity) {
				slog.Info("實體不支援批量同步，交由 cron 增量處理",
					"tenant", tenantID, "entity", entity, "count", len(idSet))
				continue
			}
			ids := setToSlice(idSet)
			if err := b.runner.RunBatch(ctx, tenantID, entity, ids); err != nil {
				slog.Warn("webhook buffer 批量同步失敗，交由 cron 增量處理",
					"tenant", tenantID, "entity", entity, "ids", len(ids), "err", err)
			}
		}
	}
}

// setToSlice 把去重集合轉回 slice；RunBatch 內部會再做一次去重與分批。
func setToSlice(set map[string]struct{}) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids
}
