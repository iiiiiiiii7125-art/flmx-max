package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- B05-001：升级家族解码上限全族对齐 ----

// 批量升级此前裸 json.NewDecoder 无上限：11MB 请求体被完整解码后走
// 业务校验（回 ids 不能为空）；收口后必须在解码层即被拒。
func TestK9R3BatchUpgradeRejectsOversizedBody(t *testing.T) {
	h := &Handler{}
	big := `{"ids":[],"version":"` + strings.Repeat("a", 11<<20) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/batch-upgrade", strings.NewReader(big))
	rec := httptest.NewRecorder()
	h.nodeBatchUpgrade(rec, req)
	var res response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Code == 0 || res.Msg != "请求参数错误" {
		t.Fatalf("oversized batch-upgrade body: code=%d msg=%q, want decode-layer 请求参数错误", res.Code, res.Msg)
	}

	// 对照：正常大小的空 ids 请求仍走业务校验（解码口径未误伤）。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/node/batch-upgrade", strings.NewReader(`{"ids":[],"version":"1.7.5"}`))
	rec = httptest.NewRecorder()
	h.nodeBatchUpgrade(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Msg != "ids不能为空" {
		t.Fatalf("normal batch-upgrade body: msg=%q, want ids不能为空", res.Msg)
	}
}

// 面板自升级共用解码此前 io.ReadAll 无上限：超限须在读取层报错，
// 且原有 DisallowUnknownFields 与空体语义保持不变。
func TestK9R3SystemUpgradeRequestDecodeLimit(t *testing.T) {
	big := `{"version":"` + strings.Repeat("a", 11<<20) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/upgrade", strings.NewReader(big))
	var parsed systemUpgradeRequest
	if err := decodeSystemUpgradeRequest(req, &parsed); err == nil {
		t.Fatal("oversized system-upgrade body decoded without error")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/system/upgrade", strings.NewReader(`{"version":"1.7.5","channel":"stable"}`))
	parsed = systemUpgradeRequest{}
	if err := decodeSystemUpgradeRequest(req, &parsed); err != nil {
		t.Fatalf("normal body: %v", err)
	}
	if parsed.Version != "1.7.5" || parsed.Channel != "stable" {
		t.Fatalf("normal body parsed = %+v", parsed)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/system/upgrade", strings.NewReader(`{"bogus":1}`))
	parsed = systemUpgradeRequest{}
	if err := decodeSystemUpgradeRequest(req, &parsed); err == nil {
		t.Fatal("unknown field accepted; DisallowUnknownFields semantics lost")
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/system/upgrade", strings.NewReader(""))
	parsed = systemUpgradeRequest{}
	if err := decodeSystemUpgradeRequest(req, &parsed); err != nil {
		t.Fatalf("empty body: %v", err)
	}
}

// ---- B05-002 + B14-004：升级状态文件三面同口径 ----

func k9WriteStatusFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, systemUpgradeStatusFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func k9TouchHeartbeat(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, systemUpgradeHeartbeatFileName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// 三格对照：完好+新鲜心跳→拦截；损坏+新鲜心跳→保守拦截（修复前零值
// 放行）；完好残留（2 小时前、无心跳）→放行且展示折算为已中断。
func TestK9R3UpgradeStatusAssessmentThreeCells(t *testing.T) {
	now := time.Now()

	// 格 1：完好进行中 + 心跳新鲜。
	dir := t.TempDir()
	exec := &systemUpgradeExecutor{deployDir: dir}
	k9WriteStatusFile(t, dir, `{"inProgress":true,"version":"1.7.6","startedAt":"`+now.UTC().Format(time.RFC3339)+`"}`)
	k9TouchHeartbeat(t, dir)
	a := exec.assessUpgradeStatus(now)
	if a.Corrupt || !a.Blocking || !a.Status.InProgress {
		t.Fatalf("cell1 (valid+fresh hb): %+v, want blocking in-progress", a)
	}

	// 格 2：状态文件损坏 + 心跳新鲜 → 保守拦截。
	dir = t.TempDir()
	exec = &systemUpgradeExecutor{deployDir: dir}
	k9WriteStatusFile(t, dir, `{not json`)
	k9TouchHeartbeat(t, dir)
	a = exec.assessUpgradeStatus(now)
	if !a.Corrupt || !a.Blocking || !a.Status.InProgress {
		t.Fatalf("cell2 (corrupt+fresh hb): %+v, want corrupt+blocking", a)
	}

	// 格 3：完好进行中但已残留（开始于 2 小时前、无心跳）→ 放行，
	// 展示折算 inProgress=false + result=interrupted，落盘原文不改。
	dir = t.TempDir()
	exec = &systemUpgradeExecutor{deployDir: dir}
	stale := now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	original := `{"inProgress":true,"version":"1.7.6","startedAt":"` + stale + `"}`
	k9WriteStatusFile(t, dir, original)
	a = exec.assessUpgradeStatus(now)
	if a.Corrupt || a.Blocking {
		t.Fatalf("cell3 (stale residual): %+v, want non-blocking", a)
	}
	if a.Status.InProgress || a.Status.Result != "interrupted" {
		t.Fatalf("cell3 display fold: %+v, want inProgress=false result=interrupted", a.Status)
	}
	if !a.Raw.InProgress {
		t.Fatalf("cell3 raw must stay inProgress=true: %+v", a.Raw)
	}
	data, err := os.ReadFile(filepath.Join(dir, systemUpgradeStatusFileName))
	if err != nil || string(data) != original {
		t.Fatalf("cell3 must not rewrite the status file on disk: %q err=%v", data, err)
	}

	// 格 4：损坏 + 无心跳 → 按残留放行、展示无进行中。
	dir = t.TempDir()
	exec = &systemUpgradeExecutor{deployDir: dir}
	k9WriteStatusFile(t, dir, `{not json`)
	a = exec.assessUpgradeStatus(now)
	if !a.Corrupt || a.Blocking || a.Status.InProgress {
		t.Fatalf("cell4 (corrupt+no hb): %+v, want corrupt+non-blocking", a)
	}

	// 格 5：无状态文件 → 全零。
	exec = &systemUpgradeExecutor{deployDir: t.TempDir()}
	a = exec.assessUpgradeStatus(now)
	if a.Corrupt || a.Blocking || a.Status.InProgress {
		t.Fatalf("cell5 (no file): %+v, want zero assessment", a)
	}
}

// 状态端点 HTTP 面：残留进行中状态折算后回吐（B14-004 本体）。
func TestK9R3SystemUpgradeStatusEndpointFoldsStale(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(panelDeployDirEnv, dir)
	stale := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	k9WriteStatusFile(t, dir, `{"inProgress":true,"version":"1.7.6","startedAt":"`+stale+`"}`)

	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/upgrade/status", nil)
	rec := httptest.NewRecorder()
	h.systemUpgradeStatus(rec, req)
	var res struct {
		Code int                     `json:"code"`
		Data systemUpgradeStatusData `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 {
		t.Fatalf("status endpoint code=%d", res.Code)
	}
	if res.Data.InProgress || res.Data.Result != "interrupted" {
		t.Fatalf("status endpoint must fold stale in-progress: %+v", res.Data)
	}
}

// Go 侧状态写入原子化：写后可完整解析、无临时文件残留、权限 0600。
func TestK9R3WriteUpgradeStatusAtomic(t *testing.T) {
	dir := t.TempDir()
	exec := &systemUpgradeExecutor{deployDir: dir}
	want := systemUpgradeStatusData{InProgress: true, Version: "1.7.6", StartedAt: "2026-10-31T00:00:00Z"}
	if err := exec.writeUpgradeStatus(want); err != nil {
		t.Fatal(err)
	}
	got, corrupt := exec.readUpgradeStatusDetailed()
	if corrupt || got != want {
		t.Fatalf("read back = %+v corrupt=%v, want %+v", got, corrupt, want)
	}
	info, err := os.Stat(filepath.Join(dir, systemUpgradeStatusFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("status file mode = %v, want 0600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file %s after atomic write", e.Name())
		}
	}
}

// ---- B10-001：订阅源删除存在性校验（裁定 (a)） ----

func TestK9R3RemoteSourceDeleteMissingID(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "k9.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := &Handler{repo: r}

	for _, keepNodes := range []bool{false, true} {
		body, _ := json.Marshal(map[string]interface{}{"id": 9999, "keepNodes": keepNodes})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/source/delete", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.remoteSourceDelete(rec, req)
		var res response.R
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res.Code == 0 || !strings.Contains(res.Msg, "订阅源不存在") {
			t.Fatalf("keepNodes=%v: missing id delete = code %d msg %q, want 订阅源不存在", keepNodes, res.Code, res.Msg)
		}
	}

	// 对照：存在的订阅源仍可正常删除（keepNodes=true 路径）。
	now := time.Now().UnixMilli()
	src := &model.RemoteSource{Name: "k9-src", Kind: model.RemoteSourceFile, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]interface{}{"id": src.ID, "keepNodes": true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/source/delete", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.remoteSourceDelete(rec, req)
	var res response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 {
		t.Fatalf("existing source delete: code=%d msg=%q", res.Code, res.Msg)
	}
	if got, _ := r.GetRemoteSource(src.ID); got != nil {
		t.Fatalf("source %d still present after delete", src.ID)
	}
}

// ---- B13-001：联邦改小端口段回收段外 runtime、失败点名 ----
