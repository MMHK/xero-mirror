package config

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// setValidEnv 設定一組合法必填環境變數，供各案例覆寫。
func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XERO_CLIENT_ID", "client-id")
	t.Setenv("XERO_CLIENT_SECRET", "client-secret")
	t.Setenv("XERO_REDIRECT_URI", "http://localhost:8080/callback")
	t.Setenv("XERO_WEBHOOK_KEY", "webhook-key")
	t.Setenv("XERO_TOKEN_ENC_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), 32)))
	t.Setenv("MYSQL_DSN", "user:pass@tcp(127.0.0.1:3306)/xero_mirror?parseTime=true&loc=UTC")
	t.Setenv("SYNC_CRON", "0 */2 * * *")
	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("XERO_DAILY_CALL_LIMIT", "")
	t.Setenv("LOG_LEVEL", "")
}

func TestLoad(t *testing.T) {
	cases := []struct {
		name    string
		apply   func(t *testing.T)
		wantErr string
	}{
		{"必填缺一", func(t *testing.T) { t.Setenv("HTTP_ADDR", "") }, "缺少必填環境變數 HTTP_ADDR"},
		{"金鑰非 32 bytes", func(t *testing.T) { t.Setenv("XERO_TOKEN_ENC_KEY", base64.StdEncoding.EncodeToString([]byte("short"))) }, "必須是 32 bytes"},
		{"redirect 用 127.0.0.1", func(t *testing.T) { t.Setenv("XERO_REDIRECT_URI", "http://127.0.0.1:8080/callback") }, "不可使用 127.0.0.1"},
		{"DSN 開 clientFoundRows", func(t *testing.T) {
			t.Setenv("MYSQL_DSN", "user:pass@tcp(127.0.0.1:3306)/xero_mirror?parseTime=true&clientFoundRows=true")
		}, "不可開啟 clientFoundRows"},
		{"LOG_LEVEL 非法", func(t *testing.T) { t.Setenv("LOG_LEVEL", "loud") }, "LOG_LEVEL 必須是 debug|info|warn|error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setValidEnv(t)
			tc.apply(t)
			_, err := Load()
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestLoadSyncJobQueue(t *testing.T) {
	t.Run("未設定採預設值", func(t *testing.T) {
		setValidEnv(t)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, DefaultSyncJobBatchSize, cfg.SyncJobBatchSize)
		require.Equal(t, DefaultSyncJobPollIntervalMs*time.Millisecond, cfg.SyncJobPollInterval)
	})

	cases := []struct {
		name    string
		apply   func(t *testing.T)
		wantErr string
	}{
		{"batch size 非法", func(t *testing.T) { t.Setenv("SYNC_JOB_BATCH_SIZE", "0") }, "SYNC_JOB_BATCH_SIZE 必須是正整數"},
		{"poll interval 非法", func(t *testing.T) { t.Setenv("SYNC_JOB_POLL_INTERVAL", "x") }, "SYNC_JOB_POLL_INTERVAL 必須是正整數毫秒"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setValidEnv(t)
			tc.apply(t)
			_, err := Load()
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestParseLogLevel 驗證 LOG_LEVEL 解析：大小寫不敏感、去頭尾空白、未設定預設 info。
func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    slog.Level
		wantErr string
	}{
		{"空字串採預設 info", "", slog.LevelInfo, ""},
		{"大寫 DEBUG", "DEBUG", slog.LevelDebug, ""},
		{"info 去空白", " info ", slog.LevelInfo, ""},
		{"warn", "warn", slog.LevelWarn, ""},
		{"大寫 ERROR", "ERROR", slog.LevelError, ""},
		{"非法值", "verbose", 0, "LOG_LEVEL 必須是 debug|info|warn|error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			level, err := parseLogLevel(tc.raw)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.ErrorContains(t, err, tc.raw)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, level)
		})
	}
}

// TestLoadLogLevel 驗證 Load 確實接上 LOG_LEVEL：合法值填入 Config，未設定走預設。
func TestLoadLogLevel(t *testing.T) {
	t.Run("debug 填入 Config", func(t *testing.T) {
		setValidEnv(t)
		t.Setenv("LOG_LEVEL", "debug")
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, slog.LevelDebug, cfg.LogLevel)
	})
	t.Run("未設定預設 info", func(t *testing.T) {
		setValidEnv(t)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, slog.LevelInfo, cfg.LogLevel)
	})
}

// TestParseSwaggerEnabled 驗證 SWAGGER_ENABLED 解析：大小寫不敏感、去空白、非法值報錯。
func TestParseSwaggerEnabled(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    bool
		wantErr bool
	}{
		{"空字串預設關閉", "", false, false},
		{"true", "true", true, false},
		{"大寫 TRUE", "TRUE", true, false},
		{"去空白 true", " true ", true, false},
		{"false", "false", false, false},
		{"大寫 FALSE", "FALSE", false, false},
		{"非法值 yes", "yes", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSwaggerEnabled(tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorContains(t, err, "SWAGGER_ENABLED")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestLoadSwaggerEnabled 驗證 Load 確實接上 SWAGGER_ENABLED：true 開啟、未設定預設關閉。
func TestLoadSwaggerEnabled(t *testing.T) {
	t.Run("true 開啟", func(t *testing.T) {
		setValidEnv(t)
		t.Setenv("SWAGGER_ENABLED", "true")
		cfg, err := Load()
		require.NoError(t, err)
		require.True(t, cfg.SwaggerEnabled)
	})
	t.Run("未設定預設關閉", func(t *testing.T) {
		setValidEnv(t)
		cfg, err := Load()
		require.NoError(t, err)
		require.False(t, cfg.SwaggerEnabled)
	})
}
