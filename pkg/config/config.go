package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	// DefaultDailyCallLimit 是 XERO_DAILY_CALL_LIMIT 未設定時，每 tenant 的每日呼叫上限。
	DefaultDailyCallLimit = 1000
	// DefaultSyncJobBatchSize 是 SYNC_JOB_BATCH_SIZE 未設定時的每次 poll 上限。
	DefaultSyncJobBatchSize = 100
	// DefaultSyncJobPollIntervalMs 是 SYNC_JOB_POLL_INTERVAL 未設定時的空閒等待時間（毫秒）。
	DefaultSyncJobPollIntervalMs = 5000
	// redirectPortMin、redirectPortMax 是 localhost callback 允許的 port 範圍。
	redirectPortMin = 3000
	redirectPortMax = 9000
)

// requiredEnvNames 是啟動時必須存在的環境變數清單。
var requiredEnvNames = []string{
	"XERO_CLIENT_ID",
	"XERO_CLIENT_SECRET",
	"XERO_REDIRECT_URI",
	"XERO_WEBHOOK_KEY",
	"XERO_TOKEN_ENC_KEY",
	"MYSQL_DSN",
	"SYNC_CRON",
	"HTTP_ADDR",
}

// Config 是服務啟動所需的設定。
type Config struct {
	ClientID       string
	ClientSecret   string
	RedirectURI    string
	WebhookKey     string
	TokenEncKey    []byte // base64 解出的 32 bytes AES-256-GCM 金鑰
	MySQLDSN       string
	SyncCron       string
	HTTPAddr       string
	DailyCallLimit int
	// SyncJobBatchSize 是每次 poll 拉取的 sync_jobs 上限。
	SyncJobBatchSize int
	// SyncJobPollInterval 是佇列為空時的等待間隔。
	SyncJobPollInterval time.Duration
	// LogLevel 是全域日誌等級，由 LOG_LEVEL 解析；未設定時為 info。
	LogLevel slog.Level
	// SwaggerEnabled 控制是否啟用 Swagger 文件端點，由 SWAGGER_ENABLED 解析；預設關閉。
	SwaggerEnabled bool
}

// Load 讀取並驗證設定；.env 存在才載入，不存在則直接使用環境變數。
func Load() (*Config, error) {
	if err := loadDotEnv(); err != nil {
		return nil, err
	}
	env, err := loadRequiredEnv()
	if err != nil {
		return nil, err
	}
	key, err := decodeTokenKey(env["XERO_TOKEN_ENC_KEY"])
	if err != nil {
		return nil, err
	}
	if err := validateRedirectURI(env["XERO_REDIRECT_URI"]); err != nil {
		return nil, err
	}
	if err := validateMySQLDSN(env["MYSQL_DSN"]); err != nil {
		return nil, err
	}
	opt, err := parseOptionalSettings()
	if err != nil {
		return nil, err
	}

	return &Config{
		ClientID:            env["XERO_CLIENT_ID"],
		ClientSecret:        env["XERO_CLIENT_SECRET"],
		RedirectURI:         env["XERO_REDIRECT_URI"],
		WebhookKey:          env["XERO_WEBHOOK_KEY"],
		TokenEncKey:         key,
		MySQLDSN:            env["MYSQL_DSN"],
		SyncCron:            env["SYNC_CRON"],
		HTTPAddr:            env["HTTP_ADDR"],
		DailyCallLimit:      opt.dailyCallLimit,
		SyncJobBatchSize:    opt.syncJobBatchSize,
		SyncJobPollInterval: time.Duration(opt.syncJobPollIntervalMs) * time.Millisecond,
		LogLevel:            opt.logLevel,
		SwaggerEnabled:      opt.swaggerEnabled,
	}, nil
}

// loadRequiredEnv 收集必填環境變數；缺任一個即回錯（訊息帶變數名稱）。
func loadRequiredEnv() (map[string]string, error) {
	env := make(map[string]string, len(requiredEnvNames))
	for _, name := range requiredEnvNames {
		v := os.Getenv(name)
		if v == "" {
			return nil, fmt.Errorf("缺少必填環境變數 %s", name)
		}
		env[name] = v
	}
	return env, nil
}

// optionalSettings 是選用環境變數解析後的結果。
type optionalSettings struct {
	dailyCallLimit        int
	syncJobBatchSize      int
	syncJobPollIntervalMs int
	logLevel              slog.Level
	swaggerEnabled        bool
}

// parseOptionalSettings 解析選用變數；任一格式錯誤即回錯，未設定則採預設。
func parseOptionalSettings() (optionalSettings, error) {
	var s optionalSettings
	var err error
	if s.dailyCallLimit, err = parseDailyCallLimit(os.Getenv("XERO_DAILY_CALL_LIMIT")); err != nil {
		return s, err
	}
	if s.syncJobBatchSize, err = parseSyncJobBatchSize(os.Getenv("SYNC_JOB_BATCH_SIZE")); err != nil {
		return s, err
	}
	if s.syncJobPollIntervalMs, err = parseSyncJobPollInterval(os.Getenv("SYNC_JOB_POLL_INTERVAL")); err != nil {
		return s, err
	}
	if s.logLevel, err = parseLogLevel(os.Getenv("LOG_LEVEL")); err != nil {
		return s, err
	}
	if s.swaggerEnabled, err = parseSwaggerEnabled(os.Getenv("SWAGGER_ENABLED")); err != nil {
		return s, err
	}
	return s, nil
}

// loadDotEnv 只在工作目錄存在 .env 時載入，正式環境可直接吃環境變數。
func loadDotEnv() error {
	if _, err := os.Stat(".env"); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("檢查 .env: %w", err)
	}
	if err := godotenv.Load(); err != nil {
		return fmt.Errorf("載入 .env: %w", err)
	}
	return nil
}

// decodeTokenKey 將 base64 金鑰解成 32 bytes；長度不符即回錯誤。
func decodeTokenKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("XERO_TOKEN_ENC_KEY 不是合法 base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("XERO_TOKEN_ENC_KEY 必須是 32 bytes（目前 %d bytes）", len(key))
	}
	return key, nil
}

// validateRedirectURI 驗證必須是 http://localhost:{3000-9000}/callback，禁 127.0.0.1。
func validateRedirectURI(raw string) error {
	if strings.Contains(raw, "127.0.0.1") {
		return fmt.Errorf("XERO_REDIRECT_URI 不可使用 127.0.0.1（Xero 註冊不匹配），請改用 localhost")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("XERO_REDIRECT_URI 無法解析: %w", err)
	}
	if u.Scheme != "http" || !strings.EqualFold(u.Hostname(), "localhost") || u.Path != "/callback" {
		return fmt.Errorf("XERO_REDIRECT_URI 必須是 http://localhost:{port}/callback")
	}
	if u.Port() == "" {
		return fmt.Errorf("XERO_REDIRECT_URI 必須包含 port（%d-%d）", redirectPortMin, redirectPortMax)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < redirectPortMin || port > redirectPortMax {
		return fmt.Errorf("XERO_REDIRECT_URI 的 port 必須介於 %d-%d", redirectPortMin, redirectPortMax)
	}
	return nil
}

// validateMySQLDSN 拒絕 clientFoundRows=true：webhook 去重依賴 affected-rows 語意。
func validateMySQLDSN(dsn string) error {
	if strings.Contains(strings.ToLower(dsn), "clientfoundrows=true") {
		return fmt.Errorf("MYSQL_DSN 不可開啟 clientFoundRows（webhook 去重依賴 affected-rows 語意）")
	}
	return nil
}

// parseDailyCallLimit 未設定時回預設值；有值則必須是正整數。
func parseDailyCallLimit(raw string) (int, error) {
	if raw == "" {
		return DefaultDailyCallLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("XERO_DAILY_CALL_LIMIT 必須是正整數（目前 %q）", raw)
	}
	return n, nil
}

// parseSyncJobBatchSize 未設定時回預設值；有值則必須是正整數。
func parseSyncJobBatchSize(raw string) (int, error) {
	if raw == "" {
		return DefaultSyncJobBatchSize, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("SYNC_JOB_BATCH_SIZE 必須是正整數（目前 %q）", raw)
	}
	return n, nil
}

// parseSyncJobPollInterval 未設定時回預設毫秒數；有值則必須是正整數毫秒。
func parseSyncJobPollInterval(raw string) (int, error) {
	if raw == "" {
		return DefaultSyncJobPollIntervalMs, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("SYNC_JOB_POLL_INTERVAL 必須是正整數毫秒（目前 %q）", raw)
	}
	return n, nil
}

// parseLogLevel 解析 LOG_LEVEL（大小寫不敏感、去頭尾空白）；未設定時預設 info。
func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL 必須是 debug|info|warn|error（目前 %q）", raw)
	}
}

// parseSwaggerEnabled 解析 SWAGGER_ENABLED（大小寫不敏感）；未設定時預設關閉。
func parseSwaggerEnabled(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("SWAGGER_ENABLED 必須是 true|false（目前 %q）", raw)
	}
}
