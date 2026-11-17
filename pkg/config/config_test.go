package config

import (
	"bytes"
	"encoding/base64"
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

func TestLoadWebhookBuffer(t *testing.T) {
	t.Run("未設定採預設值", func(t *testing.T) {
		setValidEnv(t)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, DefaultWebhookBufferSize, cfg.WebhookBufferSize)
		require.Equal(t, DefaultWebhookBufferDelayMs*time.Millisecond, cfg.WebhookBufferDelay)
	})

	cases := []struct {
		name    string
		apply   func(t *testing.T)
		wantErr string
	}{
		{"size 非法", func(t *testing.T) { t.Setenv("WEBHOOK_BUFFER_SIZE", "0") }, "WEBHOOK_BUFFER_SIZE 必須是正整數"},
		{"delay 非法", func(t *testing.T) { t.Setenv("WEBHOOK_BUFFER_DELAY", "x") }, "WEBHOOK_BUFFER_DELAY 必須是正整數毫秒"},
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
