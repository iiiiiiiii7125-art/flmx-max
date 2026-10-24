package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R16-X-02：userList 分页乘法溢出可致 slice panic（handler.go:740）。
// 溢出形与对照形逐字取自 R16-L3 探针（R16-L3-sentinel-a2.md:196-198）：
//   - 溢出形 1 {"current":4611686018427387904,"size":3}：(2^62-1)*3 回绕为负
//   - 溢出形 2 {"current":2,"size":9223372036854775807}：start 被钳制为 len 后
//     end = 1 + MaxInt64 回绕为负
//
// 修复前两溢出形均 panic（Recover 兜底回 code -2）；修复后须 code 0 空页。
func TestR16X02UserListPaginationOverflow(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("probe1", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create probe1: %v", err)
	}

	call := func(body string) (panicked bool, env envelope) {
		defer func() {
			if rec := recover(); rec != nil {
				panicked = true
			}
		}()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user/list", strings.NewReader(body))
		h.userList(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("body %s: http status = %d, want 200", body, w.Code)
		}
		return false, decodeEnvelope(t, w)
	}

	dataLen := func(env envelope) int {
		var items []json.RawMessage
		if err := json.Unmarshal(env.Data, &items); err != nil {
			t.Fatalf("decode data: %v (data=%s)", err, string(env.Data))
		}
		return len(items)
	}

	// 对照 1：正常第一页，1 条用户。
	if panicked, env := call(`{"current":1,"size":10}`); panicked || env.Code != 0 || dataLen(env) != 1 {
		t.Fatalf("control 1: panicked=%v code=%d data=%s, want no panic, code 0, 1 item", panicked, env.Code, string(env.Data))
	}
	// 对照 2：大值但不溢出，空页。
	if panicked, env := call(`{"current":2147483648,"size":2147483648}`); panicked || env.Code != 0 || dataLen(env) != 0 {
		t.Fatalf("control 2: panicked=%v code=%d data=%s, want no panic, code 0, 0 items", panicked, env.Code, string(env.Data))
	}
	// 溢出形 1：乘法回绕，修复前 panic。
	if panicked, env := call(`{"current":4611686018427387904,"size":3}`); panicked || env.Code != 0 || dataLen(env) != 0 {
		t.Fatalf("overflow 1: panicked=%v code=%d data=%s, want no panic, code 0, 0 items", panicked, env.Code, string(env.Data))
	}
	// 溢出形 2：end 加法回绕，修复前 panic。
	if panicked, env := call(`{"current":2,"size":9223372036854775807}`); panicked || env.Code != 0 || dataLen(env) != 0 {
		t.Fatalf("overflow 2: panicked=%v code=%d data=%s, want no panic, code 0, 0 items", panicked, env.Code, string(env.Data))
	}
	// 正常分页行为不变：第二页为空。
	if panicked, env := call(`{"current":2,"size":1}`); panicked || env.Code != 0 || dataLen(env) != 0 {
		t.Fatalf("paging: panicked=%v code=%d data=%s, want no panic, code 0, 0 items", panicked, env.Code, string(env.Data))
	}
}
