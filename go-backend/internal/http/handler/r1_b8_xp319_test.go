package handler

import (
	"net/http/httptest"
	"testing"
)

// X-P3-19 (L-06-1 = F-O01): seq 解析——新头 X-Flmx-Seq 存在但解析
// 失败时必须继续尝试旧头/body，不得 break 后丢弃合法旧头 seq。
func TestR1B8_XP319_SeqHeaderFallback(t *testing.T) {
	mk := func(flmx, flvx string) *int64 {
		req := httptest.NewRequest("POST", "/flow/upload", nil)
		if flmx != "" {
			req.Header.Set("X-Flmx-Seq", flmx)
		}
		if flvx != "" {
			req.Header.Set("X-Flvx-Seq", flvx)
		}
		return parseFlowSeqFromHeaders(req)
	}
	if v := mk("garbage", "42"); v == nil || *v != 42 {
		t.Fatalf("新头垃圾值+旧头合法 → 应按旧头 42, got %v", v)
	}
	if v := mk("7", "42"); v == nil || *v != 7 {
		t.Fatalf("新头合法优先 → 7, got %v", v)
	}
	if v := mk("", "42"); v == nil || *v != 42 {
		t.Fatalf("仅旧头 → 42, got %v", v)
	}
	if v := mk("garbage", "junk"); v != nil {
		t.Fatalf("两头皆垃圾 → nil（回落 body）, got %v", *v)
	}
	if v := mk("", ""); v != nil {
		t.Fatalf("无头 → nil, got %v", *v)
	}
}
