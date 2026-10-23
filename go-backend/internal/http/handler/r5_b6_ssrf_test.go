package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"gorm.io/gorm"
)

// R5 批6 · R5X-010（R5-C-01）：导入节点编辑去重门查询失败必须
// fail-closed 中止保存（与 R4X-007 remote_source 去重门同口径）。
// 故障注入：GORM 查询回调只对 []NodeInbound 目标（ListNodeInbounds）
// 注入失败——GetNodeInbound（单行目标）不受影响，故障点精确落在
// 编辑面的去重查询上。校验注入有效性由用例内前置断言承担。

func failListNodeInboundsR5B6(t *testing.T, r *repo.Repository) {
	t.Helper()
	const cb = "r5b6_fail_list_node_inbounds"
	err := r.DB().Callback().Query().Before("gorm:query").Register(cb, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*[]model.NodeInbound); ok {
			ctx, cancel := context.WithCancel(tx.Statement.Context)
			tx.Statement.Context = ctx
			cancel()
			tx.AddError(errors.New("r5b6 injected list failure"))
		}
	})
	if err != nil {
		t.Fatalf("register fault callback: %v", err)
	}
	t.Cleanup(func() { _ = r.DB().Callback().Query().Remove(cb) })
	// 注入有效性前置断言：未生效则用例先行失败、不会假绿。
	if _, err := r.ListNodeInbounds(); err == nil {
		t.Fatal("injection sanity: ListNodeInbounds must fail after fault injection")
	}
}

func r5b6ExternalUpdate(t *testing.T, h *Handler, id int64, name, host string, port int) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"id": id, "name": name, "host": host, "port": port,
	})
	req := httptest.NewRequest(http.MethodPost, "/inbound/external/update", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.inboundExternalUpdate(w, req)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	return resp.Code, resp.Msg
}

func TestR5B6ExternalUpdateDedupQueryFailureFailClosed(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r5b6dedup.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	rowA := seedExternalRow(t, r, "vless://11111111-2222-3333-4444-555555555555@203.0.113.11:8388?security=none#node-a", 0, now)
	rowB := seedExternalRow(t, r, "vless://11111111-2222-3333-4444-555555555555@203.0.113.12:8388?security=none#node-b", 0, now)
	_ = rowA

	// 对照组（无注入）：把 B 改成 A 的地址 → 既有去重门拒绝、B 不动。
	if code, msg := r5b6ExternalUpdate(t, h, rowB.ID, "node-b", "203.0.113.11", 8388); code == 0 {
		t.Fatalf("对照组：重复地址编辑必须被拒，got code=0 msg=%q", msg)
	}
	if got, err := r.GetNodeInbound(rowB.ID); err != nil || got == nil || got.Listen != "203.0.113.12" {
		t.Fatalf("对照组：拒绝后 B 不得改动，got %+v err=%v", got, err)
	}

	// 对照组（无注入）：B 改到唯一地址 → 保存成功（防过修）。
	if code, msg := r5b6ExternalUpdate(t, h, rowB.ID, "node-b", "203.0.113.13", 8388); code != 0 {
		t.Fatalf("对照组：唯一地址编辑应成功，got code=%d msg=%q", code, msg)
	}
	if got, err := r.GetNodeInbound(rowB.ID); err != nil || got == nil || got.Listen != "203.0.113.13" {
		t.Fatalf("对照组：保存后 B 地址应为 203.0.113.13，got %+v err=%v", got, err)
	}

	failListNodeInboundsR5B6(t, r)

	// 注入组 1：把 B 改成 A 的地址——查询失败时必须 fail-closed
	// 中止（先红：修复前 code=0 且重复行落库）。
	if code, msg := r5b6ExternalUpdate(t, h, rowB.ID, "node-b", "203.0.113.11", 8388); code == 0 {
		t.Fatalf("R5X-010：去重查询失败时编辑必须中止，got code=0 msg=%q（重复行已落库）", msg)
	}
	if got, err := r.GetNodeInbound(rowB.ID); err != nil || got == nil || got.Listen != "203.0.113.13" {
		t.Fatalf("R5X-010：中止后 B 不得改动，got %+v err=%v", got, err)
	}

	// 注入组 2：改到唯一地址同样必须中止——fail-closed 的对象是
	// 「无法完成去重校验的保存」，不只是已知重复的保存。
	if code, msg := r5b6ExternalUpdate(t, h, rowB.ID, "node-b", "203.0.113.14", 8388); code == 0 {
		t.Fatalf("R5X-010：去重查询失败时唯一地址编辑也必须中止，got code=0 msg=%q", msg)
	}
	if got, err := r.GetNodeInbound(rowB.ID); err != nil || got == nil || got.Listen != "203.0.113.13" {
		t.Fatalf("R5X-010：中止后 B 不得改动（组2），got %+v err=%v", got, err)
	}
}

// R5 批6 · R5X-011（R5-C-02）：gh_mirror latest 标签 stale 回退与
// 文件面共用 ghMirrorMaxStaleAge（24h）上限 + 双分支日志（对齐
// F-137 形态）；超限返回查询错误，不得无上限静默回退。

func setupGHMirrorStaleTag(t *testing.T, tag string, age time.Duration) *bytes.Buffer {
	t.Helper()
	// 上游恒 500、无 Location → lookup 必失败，迫使走 stale 回退分支。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	ghMirrorTagMu.Lock()
	savedTags := ghMirrorTags
	ghMirrorTags = map[string]ghMirrorTag{
		"owner/r5b6-repo": {tag: tag, at: time.Now().Add(-age)},
	}
	ghMirrorTagMu.Unlock()
	savedBases := ghMirrorBases
	ghMirrorBases.github = srv.URL
	var logBuf bytes.Buffer
	savedLogOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() {
		log.SetOutput(savedLogOut)
		ghMirrorBases = savedBases
		ghMirrorTagMu.Lock()
		ghMirrorTags = savedTags
		ghMirrorTagMu.Unlock()
	})
	return &logBuf
}

func TestR5B6GHMirrorLatestTagStaleCap(t *testing.T) {
	// 超限组：72h 旧缓存 + 上游失败 → 必须返回查询错误，且日志
	// 明示超限不回退（先红：修复前返回 v0.0.0-stale / err=nil
	// 且无任何日志）。
	logBuf := setupGHMirrorStaleTag(t, "v0.0.0-stale", 72*time.Hour)
	tag, err := ghMirrorLatestTag("owner/r5b6-repo", "")
	if err == nil {
		t.Fatalf("R5X-011：超过 %s 的 stale 标签不得回退，got tag=%q err=nil", ghMirrorMaxStaleAge, tag)
	}
	if tag != "" {
		t.Fatalf("R5X-011：超限时不得返回 stale 标签，got %q", tag)
	}
	if !strings.Contains(logBuf.String(), "v0.0.0-stale") || !strings.Contains(logBuf.String(), "不回退") {
		t.Fatalf("R5X-011：超限分支必须打日志（标签 + 不回退），got log=%q", logBuf.String())
	}
}

func TestR5B6GHMirrorLatestTagStaleWithinCap(t *testing.T) {
	// 上限内组：2h 旧缓存（已过 10min 新鲜期、未过 24h 上限）+
	// 上游失败 → 回退缓存标签且打回退日志（防过修：不得把上限
	// 内回退一并砍掉）。
	logBuf := setupGHMirrorStaleTag(t, "v1.2.3", 2*time.Hour)
	tag, err := ghMirrorLatestTag("owner/r5b6-repo", "")
	if err != nil {
		t.Fatalf("上限内 stale 回退应成功，got err=%v", err)
	}
	if tag != "v1.2.3" {
		t.Fatalf("上限内应返回缓存标签 v1.2.3，got %q", tag)
	}
	if !strings.Contains(logBuf.String(), "v1.2.3") || !strings.Contains(logBuf.String(), "回退") {
		t.Fatalf("R5X-011：上限内回退分支必须打日志（标签 + 回退），got log=%q", logBuf.String())
	}
}

func TestR5B6GHMirrorLatestTagNoCacheStillErrors(t *testing.T) {
	// 无缓存组：上游失败且无缓存 → 返回查询错误（既有行为不变）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	ghMirrorTagMu.Lock()
	savedTags := ghMirrorTags
	ghMirrorTags = map[string]ghMirrorTag{}
	ghMirrorTagMu.Unlock()
	savedBases := ghMirrorBases
	ghMirrorBases.github = srv.URL
	t.Cleanup(func() {
		ghMirrorBases = savedBases
		ghMirrorTagMu.Lock()
		ghMirrorTags = savedTags
		ghMirrorTagMu.Unlock()
	})
	if tag, err := ghMirrorLatestTag("owner/r5b6-nocache", ""); err == nil {
		t.Fatalf("无缓存 + 上游失败必须报错，got tag=%q err=nil", tag)
	}
}
