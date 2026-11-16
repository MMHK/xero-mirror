// pkg/xero 的 Accounting API client。分頁、增量與篩選能力依端點而異
// （見 doc/spec.md §5 矩陣）：client 不寫死端點差異，由呼叫端決定是否用
// GetPages 或帶 If-Modified-Since；未核對到支援增量的端點一律全量 fetch +
// 冪等 upsert。
package xero

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/mmhk/xero-mirror/pkg/config"
)

const (
	// defaultBaseURL 是 Accounting API 根路徑；/connections 例外，不在此 client。
	defaultBaseURL = "https://api.xero.com/api.xro/2.0"
	// requestTimeout 是所有對外請求的上限，禁無 timeout 的 client。
	requestTimeout = 30 * time.Second
	// maxConcurrentPerTenant 是 per-tenant 併發上限。
	maxConcurrentPerTenant = 5
	// callsPerMinute 是 per-tenant 平均呼叫速率（rate limiter 用）。
	callsPerMinute = 60
	// maxRetries 是 429／5xx 的最大重試次數（不含首次請求）。
	maxRetries = 3
	// pageSize 是 Xero 分頁每頁筆數；不假設總頁數。
	pageSize = 100
	// errorBodyLimit 是錯誤訊息保留的回應 body 長度上限。
	errorBodyLimit = 200
	// maxBodySize 是單次回應可讀入的位元組上限（8MB），避免異常回應吃爆記憶體。
	maxBodySize = 8 << 20
	// maxRetryAfter 是 Retry-After 可接受的最大退避秒數。
	maxRetryAfter = 30 * time.Second
	// 錯誤訊息固定前綴，供 IsRateLimited／IsNotFound 判斷；不自訂 error 型別。
	errPrefixRateLimited = "限流"
	errPrefixNotFound    = "未找到"
)

// dailyCalls 是單一 tenant 的每日呼叫計數，day 為 UTC 日期。
type dailyCalls struct {
	day string
	n   int
}

// Client 呼叫 Xero Accounting API；token 取得、限流、重試與分頁集中在此。
// tokenSource 讓測試可注入固定 token，正式流程由 TokenManager 包裝。
type Client struct {
	tokenSource func(ctx context.Context, tenantID string) (string, error)
	http        *http.Client
	baseURL     string
	mu          sync.Mutex
	sem         map[string]chan struct{}
	limiters    map[string]*rate.Limiter
	dailyLimit  int
	daily       map[string]*dailyCalls
}

// NewClient 建立 client；tm 可為 nil（測試自行注入 tokenSource）。
func NewClient(tm *TokenManager) *Client {
	c := &Client{
		http:       &http.Client{Timeout: requestTimeout},
		baseURL:    defaultBaseURL,
		sem:        make(map[string]chan struct{}),
		limiters:   make(map[string]*rate.Limiter),
		dailyLimit: config.DefaultDailyCallLimit,
		daily:      make(map[string]*dailyCalls),
	}
	if tm != nil {
		c.tokenSource = tm.AccessToken
	}
	return c
}

// BaseURL 回傳目前使用的 API 根路徑（測試斷言用）。
func (c *Client) BaseURL() string {
	return c.baseURL
}

// NewClientWithTokenSource 建立指向 baseURL、以 tokenSource 取 token 的 client；
// 供測試與自訂端點使用，正式流程仍用 NewClient + TokenManager。
func NewClientWithTokenSource(baseURL string, tokenSource func(ctx context.Context, tenantID string) (string, error)) (*Client, error) {
	c := NewClient(nil)
	c.tokenSource = tokenSource
	if err := c.setBaseURL(baseURL); err != nil {
		return nil, err
	}
	return c, nil
}

// setBaseURL 覆寫 API 根路徑並驗證格式；測試指向 httptest.Server 時使用。
// 只接受 http/https 且 host 非空，否則回繁中錯誤。
func (c *Client) setBaseURL(u string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("解析 API 根路徑 %q: %w", u, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("API 根路徑 %q 的 scheme 必須是 http 或 https", u)
	}
	if parsed.Host == "" {
		return fmt.Errorf("API 根路徑 %q 缺少 host", u)
	}
	c.baseURL = u
	return nil
}

// SetDailyCallLimit 設定每 tenant 每日呼叫上限；<= 0 代表不限制。
func (c *Client) SetDailyCallLimit(n int) {
	c.dailyLimit = n
}

// IsRateLimited 判斷錯誤是否為 429 限流（以固定前綴辨識）。
func IsRateLimited(err error) bool {
	return err != nil && strings.Contains(err.Error(), errPrefixRateLimited)
}

// IsNotFound 判斷錯誤是否為 404 未找到（以固定前綴辨識）。
func IsNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), errPrefixNotFound)
}

// Get 取得單一頁資料；同一 tenant 最多 5 併發、約 60 次/分，每日上限在 fetch 逐次計入。
// 429 依 Retry-After 重試、5xx 退避重試，各最多 maxRetries 次。
func (c *Client) Get(ctx context.Context, tenantID, path string, query url.Values, headers map[string]string) ([]byte, error) {
	token, err := c.accessToken(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	release, err := c.acquire(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer release()

	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return c.fetch(ctx, tenantID, path, endpoint, token, headers)
}

// fetch 執行請求並處理 429／5xx 重試；其他 4xx 直接回錯誤。
// 每次 attempt 開頭扣一次每日額度，重試也計入。
func (c *Client) fetch(ctx context.Context, tenantID, path, endpoint, token string, headers map[string]string) ([]byte, error) {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.reserveDailyCall(tenantID); err != nil {
			return nil, err
		}
		if err := c.waitRate(ctx, tenantID); err != nil {
			return nil, err
		}
		body, status, retryAfter, err := c.doOnce(ctx, endpoint, tenantID, token, headers)
		if err != nil {
			return nil, err
		}
		if status == http.StatusTooManyRequests {
			if attempt == maxRetries {
				return nil, c.statusError(path, status, body)
			}
			if err := sleepCtx(ctx, retryDelay(attempt, retryAfter)); err != nil {
				return nil, err
			}
			continue
		}
		if status >= 500 {
			if attempt == maxRetries {
				return nil, c.statusError(path, status, body)
			}
			if err := sleepCtx(ctx, retryDelay(attempt, "")); err != nil {
				return nil, err
			}
			continue
		}
		if status >= 400 {
			return nil, c.statusError(path, status, body)
		}
		return body, nil
	}
	return nil, fmt.Errorf("呼叫 %s 失敗：重試次數已用盡", path)
}

// GetPages 從 page=1 逐頁取得資料；fn 回傳本頁筆數，< 100 即停止。
func (c *Client) GetPages(ctx context.Context, tenantID, path string, query url.Values, headers map[string]string, fn func(page int, body []byte) (int, error)) error {
	for page := 1; ; page++ {
		q := cloneValues(query)
		q.Set("page", strconv.Itoa(page))
		body, err := c.Get(ctx, tenantID, path, q, headers)
		if err != nil {
			return err
		}
		n, err := fn(page, body)
		if err != nil {
			return fmt.Errorf("處理 %s 第 %d 頁: %w", path, page, err)
		}
		if n < pageSize {
			return nil
		}
	}
}

// cloneValues 複製 query，避免 GetPages 覆寫呼叫端傳入的參數。
func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v)+1)
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// accessToken 取得該 tenant 的 access token；正式流程由 TokenManager 提供。
func (c *Client) accessToken(ctx context.Context, tenantID string) (string, error) {
	if c.tokenSource == nil {
		return "", fmt.Errorf("tenant %s：尚未設定 token 來源", tenantID)
	}
	token, err := c.tokenSource(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("取得 tenant %s 的 access token: %w", tenantID, err)
	}
	if token == "" {
		return "", fmt.Errorf("tenant %s 取不到 access token", tenantID)
	}
	return token, nil
}

// acquire 取得該 tenant 的併發名額，回傳釋放函式；mu 只保護 map，不持鎖等待。
func (c *Client) acquire(ctx context.Context, tenantID string) (func(), error) {
	c.mu.Lock()
	sem, ok := c.sem[tenantID]
	if !ok {
		sem = make(chan struct{}, maxConcurrentPerTenant)
		c.sem[tenantID] = sem
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("等待 tenant %s 的併發名額: %w", tenantID, ctx.Err())
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	}
}

// reserveDailyCall 累計當日（UTC）呼叫次數；超過每日上限回帶限流前綴的錯誤。
func (c *Client) reserveDailyCall(tenantID string) error {
	day := time.Now().UTC().Format("2006-01-02")
	c.mu.Lock()
	defer c.mu.Unlock()

	d, ok := c.daily[tenantID]
	if !ok || d.day != day {
		d = &dailyCalls{day: day}
		c.daily[tenantID] = d
	}
	if c.dailyLimit > 0 && d.n >= c.dailyLimit {
		return fmt.Errorf("%s：tenant %s 已達每日呼叫上限 %d 次", errPrefixRateLimited, tenantID, c.dailyLimit)
	}
	d.n++
	return nil
}

// waitRate 等待該 tenant 的 per-tenant limiter 放行；mu 只保護 map。
func (c *Client) waitRate(ctx context.Context, tenantID string) error {
	c.mu.Lock()
	l, ok := c.limiters[tenantID]
	if !ok {
		l = rate.NewLimiter(rate.Every(time.Second), callsPerMinute)
		c.limiters[tenantID] = l
	}
	c.mu.Unlock()

	if err := l.Wait(ctx); err != nil {
		return fmt.Errorf("等待 tenant %s 的限流額度: %w", tenantID, err)
	}
	return nil
}

// doOnce 發出單次 GET 請求並讀回 body；Authorization 只在這裡設定，其他函式不得組出 token。
func (c *Client) doOnce(ctx context.Context, endpoint, tenantID, token string, headers map[string]string) ([]byte, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("建立 Xero 請求: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Xero-tenant-id", tenantID)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("呼叫 Xero: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return nil, 0, "", fmt.Errorf("讀取 Xero 回應: %w", err)
	}
	if len(body) > maxBodySize {
		return nil, resp.StatusCode, "", fmt.Errorf("Xero 回應超過 %d bytes 上限", maxBodySize)
	}
	return body, resp.StatusCode, resp.Header.Get("Retry-After"), nil
}

// retryDelay 依 Retry-After 決定退避秒數，缺值時用 attempt 遞增退避（上限 30s）。
func retryDelay(attempt int, retryAfter string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && n >= 0 {
		d := time.Duration(n) * time.Second
		if d > maxRetryAfter {
			return maxRetryAfter
		}
		return d
	}
	return time.Duration(attempt+1) * time.Second
}

// sleepCtx 等待指定時間；ctx 取消時立即回 ctx.Err()。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// statusError 把 HTTP 狀態轉成錯誤；429／404 帶固定前綴供 IsRateLimited／IsNotFound 判定。
func (c *Client) statusError(path string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > errorBodyLimit {
		msg = msg[:errorBodyLimit] + "…"
	}
	prefix := ""
	switch status {
	case http.StatusTooManyRequests:
		prefix = errPrefixRateLimited + "："
	case http.StatusNotFound:
		prefix = errPrefixNotFound + "："
	}
	return fmt.Errorf("%sXero %s 回應 HTTP %d: %s", prefix, path, status, msg)
}
