package config

import (
	"encoding/base64"
	"fmt"
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
	// DefaultWebhookBufferSize 是 WEBHOOK_BUFFER_SIZE 未設定時的單群組 batch size。
	DefaultWebhookBufferSize = 30
	// DefaultWebhookBufferDelayMs 是 WEBHOOK_BUFFER_DELAY 未設定時的 flush delay（毫秒）。
	DefaultWebhookBufferDelayMs = 3000
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
	// WebhookBufferSize 是單一 (tenant, entity) 群組累積達此筆數即立即 flush。
	WebhookBufferSize int
	// WebhookBufferDelay 是收到首筆事件後的最長等待時間，到期即 flush。
	WebhookBufferDelay time.Duration
}

// Load 讀取並驗證設定；.env 存在才載入，不存在則直接使用環境變數。
func Load() (*Config, error) {
	if err := loadDotEnv(); err != nil {
		return nil, err
	}

	env := make(map[string]string, len(requiredEnvNames))
	for _, name := range requiredEnvNames {
		v := os.Getenv(name)
		if v == "" {
			return nil, fmt.Errorf("缺少必填環境變數 %s", name)
		}
		env[name] = v
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
	limit, err := parseDailyCallLimit(os.Getenv("XERO_DAILY_CALL_LIMIT"))
	if err != nil {
		return nil, err
	}
	bufferSize, err := parseWebhookBufferSize(os.Getenv("WEBHOOK_BUFFER_SIZE"))
	if err != nil {
		return nil, err
	}
	bufferDelayMs, err := parseWebhookBufferDelay(os.Getenv("WEBHOOK_BUFFER_DELAY"))
	if err != nil {
		return nil, err
	}

	return &Config{
		ClientID:           env["XERO_CLIENT_ID"],
		ClientSecret:       env["XERO_CLIENT_SECRET"],
		RedirectURI:        env["XERO_REDIRECT_URI"],
		WebhookKey:         env["XERO_WEBHOOK_KEY"],
		TokenEncKey:        key,
		MySQLDSN:           env["MYSQL_DSN"],
		SyncCron:           env["SYNC_CRON"],
		HTTPAddr:           env["HTTP_ADDR"],
		DailyCallLimit:     limit,
		WebhookBufferSize:  bufferSize,
		WebhookBufferDelay: time.Duration(bufferDelayMs) * time.Millisecond,
	}, nil
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

// parseWebhookBufferSize 未設定時回預設值；有值則必須是正整數。
func parseWebhookBufferSize(raw string) (int, error) {
	if raw == "" {
		return DefaultWebhookBufferSize, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("WEBHOOK_BUFFER_SIZE 必須是正整數（目前 %q）", raw)
	}
	return n, nil
}

// parseWebhookBufferDelay 未設定時回預設毫秒數；有值則必須是正整數毫秒。
func parseWebhookBufferDelay(raw string) (int, error) {
	if raw == "" {
		return DefaultWebhookBufferDelayMs, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("WEBHOOK_BUFFER_DELAY 必須是正整數毫秒（目前 %q）", raw)
	}
	return n, nil
}
