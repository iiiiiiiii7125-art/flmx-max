package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// F-084 (audit FINAL §5-B8 / A7-011): 隧道质量探测缓存只写不淘汰——
// 隧道删除/停用后其快照仍留在 cache 里，监控接口 cache 优先原样返回，
// 页面永远显示已不存在的隧道。修复：probeAll 每轮按 enabled 集合淘汰，
// 读取侧再按 enabled 集合过滤一道（双保险）；health.Checker 的
// latestResults 同型问题在 health 包内对应修复（见该包 F-084 测试）。
// 修复前本测试失败：GetAll 与接口响应都含停用隧道与幽灵 ID。
func TestTunnelQualityCacheEvictionF084(t *testing.T) {
	secret := "unit" + "-test-secret"
	r, err := repo.Open(filepath.Join(t.TempDir(), "b8-quality.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, secret)
	now := time.Now().UnixMilli()

	seed := func(name string, status int) int64 {
		if err := r.DB().Exec(`
			INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, status, created_time, updated_time, in_ip, inx, ip_preference)
			VALUES(?, 1.0, 1, 'tls', 1, ?, ?, ?, NULL, 0, '')
		`, name, status, now, now).Error; err != nil {
			t.Fatal(err)
		}
		return mustLastInsertID(t, r, name)
	}
	liveID := seed("f084-live", 1)
	goneID := seed("f084-gone", 0)

	p := h.qualityProber
	if p == nil {
		t.Fatal("quality prober must exist")
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	defer p.cancel()
	snap := func(id int64) *tunnelQualitySnapshot {
		return &tunnelQualitySnapshot{TunnelID: id, Timestamp: now}
	}
	p.cache.Store(liveID, snap(liveID))
	p.cache.Store(goneID, snap(goneID))
	p.cache.Store(int64(999), snap(999))

	p.probeAll()

	got := map[int64]bool{}
	for _, s := range p.GetAll() {
		got[s.TunnelID] = true
	}
	if !got[liveID] || got[goneID] || got[999] || len(got) != 1 {
		t.Fatalf("cache must hold only the enabled tunnel after a probe round, got %v", got)
	}

	// 读取侧：人为再塞回一条陈旧快照（模拟淘汰竞态），接口也不得返回。
	p.cache.Store(goneID, snap(goneID))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/monitor/tunnels/quality", nil)
	ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
		auth.Claims{Sub: "1", User: "admin@user", RoleID: 0})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.monitorTunnelQualityHandler(rec, req)
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Code != 0 {
		t.Fatalf("quality handler must succeed, got %+v", out)
	}
	raw, err := json.Marshal(out.Data)
	if err != nil {
		t.Fatal(err)
	}
	var items []tunnelQualitySnapshot
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	for _, s := range items {
		if s.TunnelID != liveID {
			t.Fatalf("read side must filter stale tunnels, got tunnel %d in %+v", s.TunnelID, items)
		}
	}
}
