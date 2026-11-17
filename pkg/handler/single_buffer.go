// pkg/handler 的 single get buffer：收集不支援批量 IDs 的 webhook 事件，
// 延遲一段時間後以 single get 端點逐一抓取，避免每個事件都立即打 API。
package handler

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	pkgsync "github.com/mmhk/xero-mirror/pkg/sync"
)

// singleItem 是單一 webhook 事件在 single buffer 內的最小表示。
type singleItem struct {
	tenantID string
	entity   pkgsync.Entity
	xeroID   string
}

// SingleGetBuffer 以單一背景 goroutine 維護各 (tenant, entity, xeroID) 的待同步項目。
// 延遲 delay 後順序執行 single get，不推進 cursor。
type SingleGetBuffer struct {
	runner  *pkgsync.Runner
	delay   time.Duration
	baseCtx context.Context

	events    chan singleItem
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	closed    bool

	// pending: tenantID -> entity -> set of IDs
	pending map[string]map[pkgsync.Entity]map[string]struct{}
	timer   *time.Timer
}

// NewSingleGetBuffer 建立並啟動 single get buffer；ctx 作為 flush 同步呼叫的基底 context。
func NewSingleGetBuffer(ctx context.Context, runner *pkgsync.Runner, delay time.Duration) *SingleGetBuffer {
	b := &SingleGetBuffer{
		runner:  runner,
		delay:   delay,
		baseCtx: ctx,
		events:  make(chan singleItem, bufferEventsCap),
		done:    make(chan struct{}),
		pending: make(map[string]map[pkgsync.Entity]map[string]struct{}),
	}
	go b.loop()
	return b
}

// Add 加入一個 entry id；空白值與已關閉後的呼叫直接丟棄，channel 滿時記警告後丟棄，
// 永不阻塞 HTTP handler。
func (b *SingleGetBuffer) Add(tenantID string, entity pkgsync.Entity, xeroID string) {
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
	item := singleItem{tenantID: tenantID, entity: entity, xeroID: xeroID}
	select {
	case b.events <- item:
	default:
		slog.Warn("single get buffer 已滿，事件已丟棄，交由 cron 增量處理",
			"tenant", tenantID, "entity", entity)
	}
	b.mu.Unlock()
}

// Shutdown 關閉事件 channel，等待 event loop 把剩餘 pending flush 完並結束；
// ctx 僅用於等待 done 的逾時／取消控制。可重複呼叫，不會 panic。
func (b *SingleGetBuffer) Shutdown(ctx context.Context) error {
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
func (b *SingleGetBuffer) loop() {
	defer close(b.done)
	var timerC <-chan time.Time
	for {
		select {
		case item, ok := <-b.events:
			if !ok {
				// channel 關閉時停止 timer，避免資源洩漏
				drainTimer(b.timer)
				b.timer = nil
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

// ingest 把事件併入對應 (tenant, entity) 群組並去重；首筆事件啟動 timer。
// 回傳新的 timer channel。
func (b *SingleGetBuffer) ingest(item singleItem, timerC <-chan time.Time) <-chan time.Time {
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
		b.timer = time.NewTimer(b.delay)
		timerC = b.timer.C
	}
	return timerC
}

// flush 清空全部 pending 並逐項以 single get 端點同步；單一項失敗只記警告，不中斷其他項。
func (b *SingleGetBuffer) flush(ctx context.Context) {
	pending := b.pending
	b.pending = make(map[string]map[pkgsync.Entity]map[string]struct{})
	for tenantID, entities := range pending {
		for entity, idSet := range entities {
			for id := range idSet {
				if err := b.runner.RunSingle(ctx, tenantID, entity, id); err != nil {
					slog.Warn("single get 同步失敗，交由 cron 增量處理",
						"tenant", tenantID, "entity", entity, "id", id, "err", err)
				}
			}
		}
	}
}
