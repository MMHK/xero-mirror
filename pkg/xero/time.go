package xero

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// xeroDatePrefix 與 suffix 是 .NET JSON 日期格式的外框：/Date(ms+offset)/。
const (
	xeroDatePrefix = "/Date("
	xeroDateSuffix = ")/"
	// isoWithoutZone 是少數回應省略 Z 時的備援 layout，一律視為 UTC。
	isoWithoutZone = "2006-01-02T15:04:05.999999999"
)

// XeroTime 是 Xero 時間欄位型別；解析後一律轉 UTC，零值代表 null。
// 內嵌 time.Time 讓呼叫端可直接使用 Before／After／Equal。
type XeroTime struct {
	time.Time
}

// UnmarshalJSON 支援 /Date(ms+offset)/ 與 ISO8601；null 轉零值。
// 毫秒欄位以 UnixMilli 還原，時區偏移顯式解析後仍以 UTC 表示同一瞬間。
func (xt *XeroTime) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		xt.Time = time.Time{}
		return nil
	}
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("解析 Xero 時間: %w", err)
	}
	// 部分回應會把斜線轉義成 \/。
	raw = strings.ReplaceAll(raw, `\/`, `/`)

	if strings.HasPrefix(raw, xeroDatePrefix) && strings.HasSuffix(raw, xeroDateSuffix) {
		t, err := parseMSDate(raw[len(xeroDatePrefix) : len(raw)-len(xeroDateSuffix)])
		if err != nil {
			return err
		}
		xt.Time = t
		return nil
	}

	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		xt.Time = t.UTC()
		return nil
	}
	if t, err := time.Parse(isoWithoutZone, raw); err == nil {
		xt.Time = t.UTC()
		return nil
	}
	return fmt.Errorf("無法解析 Xero 時間: %s", raw)
}

// parseMSDate 解析 "1234567890000+0000"：毫秒 + 可省略的時區偏移。
func parseMSDate(inner string) (time.Time, error) {
	offset := 0 // 秒
	if i := strings.IndexAny(inner[1:], "+-"); i >= 0 {
		i++ // 還原相對於 inner 的索引；首字元可能是負號（1970 前）
		off, err := parseOffset(inner[i:])
		if err != nil {
			return time.Time{}, err
		}
		offset = off
		inner = inner[:i]
	}
	ms, err := strconv.ParseInt(inner, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("解析 Xero 毫秒時間 %q: %w", inner, err)
	}
	return time.UnixMilli(ms).In(time.FixedZone("", offset)).UTC(), nil
}

// parseOffset 把 "+0000"／"-0500" 轉成秒數。
func parseOffset(raw string) (int, error) {
	if len(raw) != 5 || (raw[0] != '+' && raw[0] != '-') {
		return 0, fmt.Errorf("解析 Xero 時區偏移 %q: 格式應為 ±hhmm", raw)
	}
	h, err := strconv.Atoi(raw[1:3])
	if err != nil {
		return 0, fmt.Errorf("解析 Xero 時區偏移 %q: %w", raw, err)
	}
	m, err := strconv.Atoi(raw[3:5])
	if err != nil {
		return 0, fmt.Errorf("解析 Xero 時區偏移 %q: %w", raw, err)
	}
	sec := h*3600 + m*60
	if raw[0] == '-' {
		sec = -sec
	}
	return sec, nil
}

// MarshalJSON 輸出 RFC3339 UTC；零值輸出 null，僅供測試比對。
func (xt XeroTime) MarshalJSON() ([]byte, error) {
	if xt.Time.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(xt.UTC().Format(time.RFC3339))
}
