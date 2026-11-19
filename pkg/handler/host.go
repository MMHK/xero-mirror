package handler

import (
	"net/http"
	"strings"
)

// ResolveBaseURL 從 HTTP request 推演出對外服務的 base URL（scheme + host）。
// 優先使用 X-Forwarded-Host 和 X-Forwarded-Proto（反向代理場景），
// 否則使用 r.Host 和 r.TLS 判斷。
// 回傳格式：`{scheme}://{host}`，例如 `https://example.com`。
func ResolveBaseURL(r *http.Request) string {
	// 優先使用 X-Forwarded-Host（反向代理場景）
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}

	// 優先使用 X-Forwarded-Proto
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		// 如果沒有 X-Forwarded-Proto，根據 r.TLS 判斷
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}

	// 確保 scheme 是小寫
	scheme = strings.ToLower(scheme)

	return scheme + "://" + host
}
