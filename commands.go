package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/mmhk/xero-mirror/pkg/config"
	"github.com/mmhk/xero-mirror/pkg/handler"
	"github.com/mmhk/xero-mirror/pkg/store"
	"github.com/mmhk/xero-mirror/pkg/sync"
	"github.com/mmhk/xero-mirror/pkg/xero"
)

const syncUsageText = "用法: xero-mirror sync [-month YYYY-MM] [-entity NAME]\n" +
	"  --month YYYY-MM  回補單月（預設逐 tenant 跑五實體；可與 --entity 併用）\n" +
	"  --entity NAME    只同步指定實體（contacts|accounts|invoices|creditnotes|payments；可與 --month 併用）\n"

// setupLogging 以設定的日誌等級重建全域 logger；在 config.Load 成功後、連 DB 前呼叫。
func setupLogging(level slog.Level) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

// readHeaderTimeout 是 HTTP server 讀請求 header 的上限，避免無 timeout 的常駐 server。
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout 是優雅關機流程的總等待上限，cron／HTTP server／queue 關閉共用同一預算。
const shutdownTimeout = 30 * time.Second

// runSync 依 --month／--entity 走月度回補或 cursor 增量：載入設定、連上 MySQL 後跑全部 tenant。
func runSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, syncUsageText) }
	month := fs.String("month", "", "回補月份，格式 YYYY-MM")
	entity := fs.String("entity", "", "只同步指定實體")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("解析 sync 參數: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("未知參數 %q", fs.Arg(0))
	}
	w, err := syncWindow(*month)
	if err != nil {
		return err
	}
	entityID, hasEntity, err := parseSyncEntity(*entity)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("載入設定失敗: %w", err)
	}
	setupLogging(cfg.LogLevel)
	db, err := store.OpenMigrated(ctx, cfg.MySQLDSN)
	if err != nil {
		return fmt.Errorf("初始化資料庫失敗: %w", err)
	}
	defer db.Close()
	runner, err := buildRunner(cfg, store.NewStore(db))
	if err != nil {
		return err
	}
	return runSyncMode(ctx, runner, w, *month, entityID, hasEntity)
}

// parseSyncEntity 解析 --entity；空字串代表未指定，不合法值在連 DB 前就回錯。
func parseSyncEntity(raw string) (sync.Entity, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return "", false, nil
	}
	e, ok := sync.ParseEntity(raw)
	if !ok {
		return "", false, fmt.Errorf("--entity 不合法: %q（可用 contacts|accounts|invoices|creditnotes|payments）", raw)
	}
	return e, true, nil
}

// runSyncMode 依視窗與 entity 分派四種組合，成功後記錄完成 log（month 帶 window、entity 模式帶 entity）。
func runSyncMode(ctx context.Context, runner *sync.Runner, w *sync.Window, month string,
	entity sync.Entity, hasEntity bool) error {
	var err error
	if w == nil {
		if hasEntity {
			err = runner.RunEntityIncremental(ctx, entity)
		} else {
			err = runner.RunAllIncremental(ctx)
		}
	} else {
		if hasEntity {
			err = runner.RunEntityMonth(ctx, *w.MonthStart, *w.MonthEnd, entity)
		} else {
			err = runner.RunAllMonth(ctx, *w.MonthStart, *w.MonthEnd)
		}
	}
	if err != nil {
		return err
	}
	switch {
	case w != nil && hasEntity:
		slog.Info("sync completed", "mode", "month", "window", month, "entity", entity)
	case w != nil:
		slog.Info("sync completed", "mode", "month", "window", month)
	case hasEntity:
		slog.Info("sync completed", "mode", "incremental", "entity", entity)
	default:
		slog.Info("sync completed", "mode", "incremental")
	}
	return nil
}

// syncWindow 驗證 --month 並組 UTC 半開視窗 [該月 1 日, 下月 1 日)；
// 空字串代表走 cursor 增量，回 nil。
func syncWindow(month string) (*sync.Window, error) {
	if month == "" {
		return nil, nil
	}
	start, err := time.Parse("2006-01", month)
	if err != nil || len(month) != 7 {
		return nil, fmt.Errorf("--month 格式必須是 YYYY-MM: %q", month)
	}
	end := start.AddDate(0, 1, 0)
	return &sync.Window{MonthStart: &start, MonthEnd: &end}, nil
}

// buildRunner 組出同步執行器（cipher → OAuth → TokenManager → Client → Runner）；
// sync 與 serve 共用同一套組裝，避免兩處各建一份 OAuth／client。
// Client 在這裡設定 rate limit callback，確保所有模式（sync/serve）都能收集 Xero API 配額。
func buildRunner(cfg *config.Config, st *store.Store) (*sync.Runner, error) {
	cipher, err := xero.NewCipher(cfg.TokenEncKey)
	if err != nil {
		return nil, fmt.Errorf("建立 token 加密器失敗: %w", err)
	}
	oauth := &xero.OAuth{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURI: cfg.RedirectURI}
	tm := xero.NewTokenManager(st, cipher, oauth)
	client := xero.NewClient(tm)
	client.SetDailyCallLimit(cfg.DailyCallLimit)
	// 設定 rate limit callback：Xero client 每次收到 response 時非同步更新 tenant 的 rate limit 快照。
	client.SetRateLimitCallback(func(tenantID string, info xero.RateLimitInfo) {
		if err := st.UpdateTenantRateLimit(context.Background(), tenantID, store.RateLimitInfo{
			DayLimitRemaining:       info.DayLimitRemaining,
			MinuteLimitRemaining:    info.MinuteLimitRemaining,
			AppMinuteLimitRemaining: info.AppMinuteLimitRemaining,
		}); err != nil {
			slog.Warn("update tenant rate limit failed", "tenant", tenantID, "err", err)
		}
	})
	return sync.NewRunner(client, st), nil
}

// serveDeps 集中 serve 啟動後需要一起關閉的資源；cancelCron 控制獨立於 signal ctx
// 的生命週期，避免關機信號一到就讓在飛同步失效。
type serveDeps struct {
	db         *sql.DB
	queue      *handler.SyncJobQueue
	srv        *http.Server
	cron       *cron.Cron
	cancelCron context.CancelFunc
}

// runServe 載入設定、接線 cron + webhook + 健康檢查並常駐，收到 ctx 取消後依序優雅關閉。
// cron 與 queue 使用獨立於 signal ctx 的 context，確保關機流程仍能控制取消時機。
func runServe(ctx context.Context, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("載入設定失敗: %w", err)
	}
	setupLogging(cfg.LogLevel)
	// 兩者皆派生自 context.Background()，不隨 signal ctx 連動；取消時機由 shutdown 掌控。
	cronCtx, cancelCron := context.WithCancel(context.Background())
	queueCtx, cancelQueue := context.WithCancel(context.Background())
	defer cancelCron()
	defer cancelQueue()
	deps, err := buildServeDeps(ctx, cfg, cronCtx, queueCtx, cancelCron)
	if err != nil {
		return err
	}
	return serveLoop(ctx, deps)
}

// buildServeDeps 連線、migration 並組出 serve 所需的全部元件；失敗時關閉已建立資源。
// cronCtx 供 cron 同步使用、queueCtx 作為 SyncJobQueue 背景 goroutine 的基底，兩者皆不隨 signal ctx 取消。
func buildServeDeps(ctx context.Context, cfg *config.Config, cronCtx, queueCtx context.Context,
	cancelCron context.CancelFunc) (*serveDeps, error) {
	db, err := store.OpenMigrated(ctx, cfg.MySQLDSN)
	if err != nil {
		return nil, fmt.Errorf("初始化資料庫失敗: %w", err)
	}
	st := store.NewStore(db)
	runner, err := buildRunner(cfg, st)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	queue := handler.NewSyncJobQueue(queueCtx, runner, st, cfg.SyncJobBatchSize, cfg.SyncJobPollInterval)
	mux := handler.NewMux(
		handler.NewWebhookHandler(st, []byte(cfg.WebhookKey)),
		handler.NewHealthHandler(st),
		handler.NewStatusHandler(st),
		handler.NewPageHandler(cfg.SwaggerEnabled),
	)
	// SkipIfStillRunning：上一輪同步還在跑時跳過本輪，避免長同步與下一輪 cron 重疊。
	c := cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))
	if _, err := c.AddFunc(cfg.SyncCron, func() { runCronSync(cronCtx, runner) }); err != nil {
		_ = queue.Shutdown(context.Background())
		_ = db.Close()
		return nil, fmt.Errorf("註冊 cron 排程失敗: %w", err)
	}
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	return &serveDeps{db: db, queue: queue, srv: srv, cron: c, cancelCron: cancelCron}, nil
}

// serveLoop 啟動 cron 與 HTTP server，等待關機訊號或 server 錯誤後優雅關閉。
func serveLoop(ctx context.Context, deps *serveDeps) error {
	deps.cron.Start()
	slog.Info("cron scheduler started")

	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP server started", "addr", deps.srv.Addr)
		if err := deps.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var serveErr error
	select {
	case <-ctx.Done():
		slog.Info("received shutdown signal, starting graceful shutdown")
	case serveErr = <-errCh:
		slog.Error("HTTP server error, shutting down", "err", serveErr)
	}
	deps.shutdown()
	return serveErr
}

// shutdown 依固定順序關閉：cron → HTTP server → SyncJobQueue → DB；
// 全程以 shutdownTimeout 作為總等待上限，單步失敗只 Warn 續行，queue 的當前任務不得被跳過。
// cronCtx 只在等待 cron job 逾時後取消。
func (d *serveDeps) shutdown() {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// cron.Stop 停止排新 job，回傳的 stopCtx 會在所有執行中 job 結束時 done；
	// 只在總關機預算內等待，逾時就取消 cronCtx，讓在飛的 cron／HTTP／DB 呼叫收斂。
	stopCtx := d.cron.Stop()
	slog.Info("cron scheduler stopped, no new jobs will be triggered")
	select {
	case <-stopCtx.Done():
		slog.Info("running cron jobs completed")
	case <-shutdownCtx.Done():
		slog.Warn("timeout waiting for cron jobs, canceling cron context and continuing shutdown")
		d.cancelCron()
	}

	if err := d.srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("HTTP server shutdown failed", "err", err)
	} else {
		slog.Info("HTTP server closed")
	}

	// 等待當前處理中的 job 完成（或逾時）；shutdownCtx 只控制等待上限。
	if err := d.queue.Shutdown(shutdownCtx); err != nil {
		slog.Warn("sync job queue close failed", "err", err)
	} else {
		slog.Info("sync job queue closed")
	}

	if err := d.db.Close(); err != nil {
		slog.Warn("database connection close failed", "err", err)
	} else {
		slog.Info("database connection closed")
	}
}

// runCronSync 是 cron 回呼：跑一輪全 tenant 五實體增量，失敗只 Warn 不中斷排程。
func runCronSync(ctx context.Context, runner *sync.Runner) {
	slog.Info("cron sync started")
	if err := runner.RunAllIncremental(ctx); err != nil {
		slog.Warn("cron sync not all successful", "err", err)
	}
}

// runAuth 執行一次性 OAuth2 授權：載入設定、連上 MySQL 後交給 xero.Authorize。
func runAuth(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("auth 不接受參數 %q", args[0])
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("載入設定失敗: %w", err)
	}
	setupLogging(cfg.LogLevel)
	db, err := store.OpenMigrated(ctx, cfg.MySQLDSN)
	if err != nil {
		return fmt.Errorf("初始化資料庫失敗: %w", err)
	}
	defer db.Close()
	cipher, err := xero.NewCipher(cfg.TokenEncKey)
	if err != nil {
		return fmt.Errorf("建立 token 加密器失敗: %w", err)
	}
	oauth := &xero.OAuth{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURI: cfg.RedirectURI}
	return xero.Authorize(ctx, store.NewStore(db), oauth, cipher, os.Stdout)
}
