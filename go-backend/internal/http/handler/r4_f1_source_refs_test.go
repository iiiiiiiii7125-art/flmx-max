package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R4-F1-01 回归钉（审计探针四步场景固化）：订阅对订阅源的引用完整性。
// 修复前：保存面不验源存在（幽灵 ID 落库且计入「至少选一个」）、
// 删源面不清理不点名（订阅留死 ID、渲染静默变空）。修复后：保存
// 硬拒点名；删源清理引用并在回执 warning 点名受影响订阅。
// （2.1.2：原步骤 2 的 Options 链式按源引用已随订阅链式整体删除——
// 只带已删链式键的保存退化为「什么都没选」，仍被硬拒。）

func callSubscriptionSaveR4(t *testing.T, h *Handler, payload map[string]interface{}) response.R {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.subscriptionSave(rec, req)
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode save response %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestR4F1SubscriptionSourceRefIntegrity(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r4f1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	// 步骤 1：sourceIds 指向从未存在过的源 → 硬拒并点名，不落库。
	out := callSubscriptionSaveR4(t, h, map[string]interface{}{
		"name": "ghost-source-sub", "sourceIds": []int64{999999},
	})
	if out.Code == 0 {
		t.Fatal("step1: save with nonexistent sourceIds must be rejected")
	}
	if !strings.Contains(out.Msg, "999999") {
		t.Fatalf("step1: rejection must name the unknown source id, msg=%q", out.Msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription WHERE name='ghost-source-sub'`); n != 0 {
		t.Fatalf("step1: rejected save must not persist, rows=%d", n)
	}

	// 步骤 2：只带已删的链式按源键（无任何真实来源）→ 仍被硬拒
	// （链式删除后它不再计入「至少选一个」，空订阅不得落库）。
	out = callSubscriptionSaveR4(t, h, map[string]interface{}{
		"name":    "ghost-chain-sub",
		"options": map[string]interface{}{"chainLandingSources": []int64{888888}},
	})
	if out.Code == 0 {
		t.Fatal("step2: save with only removed chain options must be rejected")
	}

	// 步骤 3：真源订阅 → keepNodes 删源 → 回执点名该订阅，且订阅
	// 的 SourceIDs 被清理（不留死 ID）。
	now := time.Now().UnixMilli()
	src := &model.RemoteSource{Name: "r4f1-src", Kind: model.RemoteSourceFile, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatal(err)
	}
	out = callSubscriptionSaveR4(t, h, map[string]interface{}{
		"name":      "real-source-sub",
		"sourceIds": []int64{src.ID},
	})
	if out.Code != 0 {
		t.Fatalf("step3: save with a real source must succeed, code=%d msg=%q", out.Code, out.Msg)
	}
	subs, err := r.ListSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	var subID int64
	for _, s := range subs {
		if s.Name == "real-source-sub" {
			subID = s.ID
		}
	}
	if subID == 0 {
		t.Fatal("step3: saved subscription not found")
	}

	body, _ := json.Marshal(map[string]interface{}{"id": src.ID, "keepNodes": true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/source/delete", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.remoteSourceDelete(rec, req)
	var delOut response.R
	if err := json.NewDecoder(rec.Body).Decode(&delOut); err != nil {
		t.Fatalf("step3: decode delete response %q: %v", rec.Body.String(), err)
	}
	if delOut.Code != 0 {
		t.Fatalf("step3: delete source: code=%d msg=%q", delOut.Code, delOut.Msg)
	}
	if !strings.Contains(delOut.Warning, "real-source-sub") {
		t.Fatalf("step3: delete receipt must name the affected subscription, warning=%q", delOut.Warning)
	}
	stored, err := r.GetSubscription(subID)
	if err != nil || stored == nil {
		t.Fatalf("step3: reload subscription: %v", err)
	}
	for _, id := range parseIDList(stored.SourceIDs) {
		if id == src.ID {
			t.Fatalf("step3: subscription SourceIDs still holds deleted source %d: %q", src.ID, stored.SourceIDs)
		}
	}

	// 步骤 4：删源后以原样 sourceIds 重新保存该订阅 → 硬拒点名
	// （修复前 code=0 接受、订阅继续渲染为空且无任何提示）。
	out = callSubscriptionSaveR4(t, h, map[string]interface{}{
		"id": subID, "name": "real-source-sub", "sourceIds": []int64{src.ID},
	})
	if out.Code == 0 {
		t.Fatal("step4: re-save with the deleted source id must be rejected")
	}
	if !strings.Contains(out.Msg, strconv.FormatInt(src.ID, 10)) {
		t.Fatalf("step4: rejection must name the dead source id, msg=%q", out.Msg)
	}
}
