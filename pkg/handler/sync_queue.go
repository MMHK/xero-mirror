// pkg/handler/sync_queue.go：背景 poll sync_jobs 的佇列處理器。
// 持續撈 pending records，按 (tenant_id, entity) 分組後呼叫 Runner 同步，
// 完成標記 done、失敗標記 failed。佇列為空時等待 pollInterval 再重試。
package handler

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/mmhk/xero-mirror/pkg/store"
	pkgsync "github.com/mmhk/xero-mirror/pkg/sync"
)

// SyncJobQueue 背景 poll sync_jobs pending records 並處理。
type SyncJobQueue struct {
	runner       *pkgsync.Runner
	store        *store.Store
	batchSize    int
	pollInterval time.Duration
	baseCtx      context.Context
	done         chan struct{}
	closeOnce    sync.Once
	mu           sync.Mutex
	closed       bool
}

// NewSyncJobQueue 建立並啟動 queue；ctx 作為背景 goroutine 的基底 context。
func NewSyncJobQueue(ctx context.Context, runner *pkgsync.Runner, store *store.Store, batchSize int, pollInterval time.Duration) *SyncJobQueue {
	q := &SyncJobQueue{
		runner:       runner,
		store:        store,
		batchSize:    batchSize,
		pollInterval: pollInterval,
		baseCtx:      ctx,
		done:         make(chan struct{}),
	}
	go q.loop()
	return q
}

// Shutdown 關閉 queue，等待當前處理完成；ctx 僅用於等待逾時控制。
func (q *SyncJobQueue) Shutdown(ctx context.Context) error {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
	})
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loop 是唯一的背景 goroutine：持續 poll 並處理 pending jobs。
func (q *SyncJobQueue) loop() {
	defer close(q.done)
	for {
		if q.isClosed() {
			return
		}
		jobs, err := q.store.GetPendingSyncJobs(q.baseCtx, q.batchSize)
		if err != nil {
			slog.Warn("查詢待處理 sync_jobs 失敗", "err", err)
			if !q.sleep(q.baseCtx, q.pollInterval) {
				return
			}
			continue
		}
		if len(jobs) == 0 {
			if !q.sleep(q.baseCtx, q.pollInterval) {
				return
			}
			continue
		}
		q.processBatch(jobs)
	}
}

// isClosed 檢查 queue 是否已關閉。
func (q *SyncJobQueue) isClosed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

// sleep 等待指定時間或 context 取消；回傳 true 表示正常醒來，false 表示應結束。
func (q *SyncJobQueue) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// processBatch 把 jobs 按 (tenant_id, entity) 分組後逐組處理。
func (q *SyncJobQueue) processBatch(jobs []store.SyncJob) {
	groups := q.groupJobs(jobs)
	for key, group := range groups {
		if q.isClosed() {
			return
		}
		q.processGroup(key.tenantID, key.entity, group)
	}
}

// groupKey 是分組鍵。
type groupKey struct {
	tenantID string
	entity   string
}

// groupJobs 把 jobs 按 (tenant_id, entity) 分組，維持原順序。
func (q *SyncJobQueue) groupJobs(jobs []store.SyncJob) map[groupKey][]store.SyncJob {
	groups := make(map[groupKey][]store.SyncJob)
	for _, j := range jobs {
		k := groupKey{tenantID: j.TenantID, entity: j.Entity}
		groups[k] = append(groups[k], j)
	}
	return groups
}

// processGroup 處理單一 (tenant_id, entity) 群組；依實體能力分派到批量或逐筆。
func (q *SyncJobQueue) processGroup(tenantID, entity string, jobs []store.SyncJob) {
	e := pkgsync.Entity(entity)
	if pkgsync.SupportsBatchIDs(e) {
		q.processBatchIDs(tenantID, e, jobs)
		return
	}
	q.processSingles(tenantID, e, jobs)
}

// processBatchIDs 收集全部 xero_ids 呼叫 RunBatch；成功全部標記 done，失敗全部標記 failed。
func (q *SyncJobQueue) processBatchIDs(tenantID string, entity pkgsync.Entity, jobs []store.SyncJob) {
	ids := make([]string, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.XeroID)
	}
	err := q.runner.RunBatch(q.baseCtx, tenantID, entity, ids)
	if err != nil {
		slog.Warn("批量同步失敗", "tenant", tenantID, "entity", entity, "ids", len(ids), "err", err)
		q.markJobsFailed(jobs, err.Error())
		return
	}
	q.markJobsDone(jobs)
}

// processSingles 逐筆呼叫 RunSingle；單一失敗只標記該筆 failed，不中斷其餘。
func (q *SyncJobQueue) processSingles(tenantID string, entity pkgsync.Entity, jobs []store.SyncJob) {
	for _, j := range jobs {
		if q.isClosed() {
			return
		}
		err := q.runner.RunSingle(q.baseCtx, tenantID, entity, j.XeroID)
		if err != nil {
			slog.Warn("單筆同步失敗", "tenant", tenantID, "entity", entity, "id", j.XeroID, "err", err)
			q.markJobFailed(j, err.Error())
			continue
		}
		q.markJobDone(j)
	}
}

// markJobsDone 把整批 jobs 標記為 done。
func (q *SyncJobQueue) markJobsDone(jobs []store.SyncJob) {
	for _, j := range jobs {
		q.markJobDone(j)
	}
}

// markJobDone 把單一 job 刪除（成功處理後不再保留，避免 UNIQUE KEY 擋住新的 INSERT）。
func (q *SyncJobQueue) markJobDone(j store.SyncJob) {
	err := q.store.WithTx(q.baseCtx, func(tx *sql.Tx) error {
		return q.store.DeleteSyncJob(q.baseCtx, tx, j.ID)
	})
	if err != nil {
		slog.Warn("刪除 sync_job 失敗", "id", j.ID, "err", err)
	}
}

// markJobsFailed 把整批 jobs 標記為 failed。
func (q *SyncJobQueue) markJobsFailed(jobs []store.SyncJob, errMsg string) {
	for _, j := range jobs {
		q.markJobFailed(j, errMsg)
	}
}

// markJobFailed 把單一 job 從 sync_jobs 刪除，並插入 failed_sync_jobs 供除錯。
func (q *SyncJobQueue) markJobFailed(j store.SyncJob, errMsg string) {
	err := q.store.WithTx(q.baseCtx, func(tx *sql.Tx) error {
		// 先刪除 sync_jobs
		if err := q.store.DeleteSyncJob(q.baseCtx, tx, j.ID); err != nil {
			return err
		}
		// 再插入 failed_sync_jobs
		return q.store.InsertFailedSyncJob(q.baseCtx, tx, store.FailedSyncJob{
			TenantID:  j.TenantID,
			Entity:    j.Entity,
			XeroID:    j.XeroID,
			Reason:    j.Reason,
			LastError: &errMsg,
			FailedAt:  time.Now().UTC(),
		})
	})
	if err != nil {
		slog.Warn("處理失敗 sync_job 失敗", "id", j.ID, "err", err)
	}
}
