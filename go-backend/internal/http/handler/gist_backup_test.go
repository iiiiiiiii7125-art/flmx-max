package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/repo"
)

// PORT-3342/B1：Gist 上传链单测。全部用 httptest mock GitHub API，
// 不联网、不使用真实令牌（令牌均为测试假值）。

func setupGistTest(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/gist.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	return h
}

func gistTestRepo(t *testing.T, h *Handler) *repo.Repository {
	t.Helper()
	// New() 不暴露 repo；测试经 Handler 的 repo 字段取（同包）。
	return h.repo
}

func adminRequest(t *testing.T, method, path string, body []byte) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	return req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey,
		auth.Claims{Sub: "1", User: "admin@user", RoleID: 0}))
}

func decodeGistEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (int, string, json.RawMessage) {
	t.Helper()
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	return env.Code, env.Msg, env.Data
}

func TestGistEncryptRoundTrip(t *testing.T) {
	plain := []byte(`{"version":"1.0","users":[]}`)
	enc, err := gistEncrypt(plain, "pw-123")
	if err != nil {
		t.Fatalf("gistEncrypt: %v", err)
	}
	if bytes.Equal(enc, plain) {
		t.Fatal("encrypted output must differ from plaintext")
	}
	// 信封键名 "flyx" 是上游线格式，见 gist_backup.go 注释。
	var raw map[string]interface{}
	if err := json.Unmarshal(enc, &raw); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if raw["flyx"] != "encrypted-backup" || raw["kdf"] != "scrypt" {
		t.Fatalf("unexpected envelope header: %v", raw)
	}
	got, encrypted, err := gistOpen(enc, "pw-123")
	if err != nil || !encrypted {
		t.Fatalf("gistOpen correct password: got=%q encrypted=%v err=%v", got, encrypted, err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
	if _, _, err := gistOpen(enc, "wrong"); err == nil {
		t.Fatal("gistOpen wrong password must fail")
	}
	if _, _, err := gistOpen(enc, ""); err == nil {
		t.Fatal("gistOpen empty password on encrypted content must fail")
	}
	// 明文直通。
	got, encrypted, err = gistOpen(plain, "")
	if err != nil || encrypted || !bytes.Equal(got, plain) {
		t.Fatalf("gistOpen plaintext passthrough: got=%q encrypted=%v err=%v", got, encrypted, err)
	}
}

func TestGistDue(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		cfg  map[string]string
		want bool
	}{
		{"interval 0 is off", map[string]string{gistConfigToken: "tok", gistConfigInterval: "0"}, false},
		{"no token is off", map[string]string{gistConfigInterval: "24"}, false},
		{"never uploaded is due", map[string]string{gistConfigToken: "tok", gistConfigInterval: "24"}, true},
		{"recent upload not due", map[string]string{gistConfigToken: "tok", gistConfigInterval: "24", gistConfigLastAt: ""}, false},
		{"interval elapsed is due", map[string]string{gistConfigToken: "tok", gistConfigInterval: "24", gistConfigLastAt: ""}, true},
	}
	// 用 now 精确算 lastAt，避免写死时间戳漂移。
	cases[3].cfg[gistConfigLastAt] = strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10)
	cases[4].cfg[gistConfigLastAt] = strconv.FormatInt(now.Add(-25*time.Hour).UnixMilli(), 10)
	for _, c := range cases {
		if got := gistDue(c.cfg, now); got != c.want {
			t.Errorf("%s: gistDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestGistStatusTokenHint(t *testing.T) {
	if got := tokenHint("abcdefghijklmnop"); got != "abcd••••••••mnop" {
		t.Errorf("long token hint = %q", got)
	}
	if got := tokenHint("short"); got != "•••••" {
		t.Errorf("short token hint = %q", got)
	}
	if got := tokenHint(""); got != "" {
		t.Errorf("empty token hint = %q", got)
	}
	v := gistStatus(map[string]string{gistConfigToken: "abcdefghijklmnop", gistConfigID: "abc123", gistConfigInterval: "24"})
	if !v.Configured || v.GistURL != "https://gist.github.com/abc123" || v.IntervalHours != 24 {
		t.Fatalf("gistStatus = %+v", v)
	}
	if strings.Contains(v.TokenHint, "efgh") {
		t.Fatalf("tokenHint leaks middle of token: %q", v.TokenHint)
	}
}

// mockGistServer 录下每次请求，供断言请求体构造。
type mockGistServer struct {
	server *httptest.Server
	reqs   []mockGistRequest
}

type mockGistRequest struct {
	Method string
	Path   string
	Auth   string
	Body   map[string]interface{}
}

func newMockGistServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, rec *mockGistServer)) *mockGistServer {
	t.Helper()
	m := &mockGistServer{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		m.reqs = append(m.reqs, mockGistRequest{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
		handler(w, r, m)
	}))
	t.Cleanup(m.server.Close)
	t.Setenv("FLMX_GIST_API_BASE", m.server.URL)
	t.Setenv("FLVX_GIST_API_BASE", "")
	return m
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func TestGistUploadCreatesThenPatchesAndSkipsUnchanged(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{gistConfigToken: "test-token-1234567890", gistConfigPassword: "enc-pw", gistConfigInterval: "24"}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	m := newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/gists":
			writeJSON(w, 201, map[string]interface{}{"id": "gist-new-id", "html_url": "https://gist.github.com/gist-new-id"})
		case req.Method == http.MethodPatch && req.URL.Path == "/gists/gist-new-id":
			writeJSON(w, 200, map[string]interface{}{"id": "gist-new-id", "html_url": "https://gist.github.com/gist-new-id"})
		default:
			writeJSON(w, 404, map[string]interface{}{"message": "not found"})
		}
	})

	// 首次：新建 gist。
	res, err := h.gistUpload(context.Background(), false)
	if err != nil {
		t.Fatalf("first gistUpload: %v", err)
	}
	if !res.Uploaded {
		t.Fatalf("first upload result = %+v, want Uploaded=true", res)
	}
	if len(m.reqs) != 1 || m.reqs[0].Method != http.MethodPost || m.reqs[0].Path != "/gists" {
		t.Fatalf("first upload requests = %+v", m.reqs)
	}
	if m.reqs[0].Auth != "Bearer test-token-1234567890" {
		t.Fatalf("Authorization header = %q", m.reqs[0].Auth)
	}
	createBody := m.reqs[0].Body
	if createBody["public"] != false {
		t.Fatalf("create body public = %v, want false (private gist)", createBody["public"])
	}
	files, ok := createBody["files"].(map[string]interface{})
	if !ok {
		t.Fatalf("create body files = %v", createBody["files"])
	}
	file, ok := files[gistFileName].(map[string]interface{})
	if !ok || file["content"] == nil {
		t.Fatalf("create body missing file %q: %v", gistFileName, files)
	}
	content, _ := file["content"].(string)
	// T-4 起上传必须加密：先解密再断言备份内容。
	plain, encrypted, err := gistOpen([]byte(content), "enc-pw")
	if err != nil || !encrypted {
		t.Fatalf("uploaded content must be encrypted: encrypted=%v err=%v", encrypted, err)
	}
	var backup map[string]interface{}
	if err := json.Unmarshal(plain, &backup); err != nil || backup["version"] != "1.0" {
		t.Fatalf("uploaded content is not backup JSON: err=%v backup=%v", err, backup)
	}
	cfg, err := r.GetConfigsByNames([]string{gistConfigID, gistConfigLastHash, gistConfigLastUpload})
	if err != nil {
		t.Fatalf("read back configs: %v", err)
	}
	if cfg[gistConfigID] != "gist-new-id" || cfg[gistConfigLastHash] == "" || cfg[gistConfigLastUpload] == "" {
		t.Fatalf("stored gist state = %v", cfg)
	}

	// 内容不变：第二次跳过，不产生新请求。
	res, err = h.gistUpload(context.Background(), false)
	if err != nil {
		t.Fatalf("second gistUpload: %v", err)
	}
	if res.Uploaded {
		t.Fatalf("unchanged upload must be skipped, got %+v", res)
	}
	if len(m.reqs) != 1 {
		t.Fatalf("unchanged upload must not call GitHub, requests = %+v", m.reqs)
	}

	// 强制上传：走 PATCH 复用同一 gist。
	res, err = h.gistUpload(context.Background(), true)
	if err != nil {
		t.Fatalf("forced gistUpload: %v", err)
	}
	if !res.Uploaded || len(m.reqs) != 2 || m.reqs[1].Method != http.MethodPatch || m.reqs[1].Path != "/gists/gist-new-id" {
		t.Fatalf("forced upload result=%+v requests=%+v", res, m.reqs)
	}
}

func TestGistUploadEncryptedContent(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{
		gistConfigToken: "test-token-1234567890", gistConfigPassword: "enc-pw", gistConfigInterval: "0",
	}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	m := newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		if req.Method == http.MethodPost && req.URL.Path == "/gists" {
			writeJSON(w, 201, map[string]interface{}{"id": "gist-enc-id"})
			return
		}
		writeJSON(w, 404, map[string]interface{}{"message": "not found"})
	})
	res, err := h.gistUpload(context.Background(), false)
	if err != nil || !res.Uploaded {
		t.Fatalf("encrypted gistUpload: res=%+v err=%v", res, err)
	}
	if len(m.reqs) != 1 {
		t.Fatalf("requests = %+v", m.reqs)
	}
	files := m.reqs[0].Body["files"].(map[string]interface{})
	content := files[gistFileName].(map[string]interface{})["content"].(string)
	plain, encrypted, err := gistOpen([]byte(content), "enc-pw")
	if err != nil || !encrypted {
		t.Fatalf("uploaded content must decrypt with password: encrypted=%v err=%v", encrypted, err)
	}
	var backup map[string]interface{}
	if err := json.Unmarshal(plain, &backup); err != nil || backup["version"] != "1.0" {
		t.Fatalf("decrypted content is not backup JSON: %v", err)
	}

	// gist 在 GitHub 侧被删后重建的路径由 TestGistUploadDeletedGistRecreates
	// 独立覆盖（避免同一用例内两个 mock 覆盖 FLMX_GIST_API_BASE）。
	_ = m
}

func TestGistUploadDeletedGistRecreates(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{
		gistConfigToken: "test-token-1234567890", gistConfigPassword: "enc-pw", gistConfigID: "gist-deleted", gistConfigInterval: "0",
	}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	m := newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		switch {
		case req.Method == http.MethodPatch && req.URL.Path == "/gists/gist-deleted":
			writeJSON(w, 404, map[string]interface{}{"message": "not found"})
		case req.Method == http.MethodPost && req.URL.Path == "/gists":
			writeJSON(w, 201, map[string]interface{}{"id": "gist-recreated"})
		default:
			writeJSON(w, 404, map[string]interface{}{"message": "not found"})
		}
	})
	res, err := h.gistUpload(context.Background(), false)
	if err != nil || !res.Uploaded {
		t.Fatalf("recreate gistUpload: res=%+v err=%v", res, err)
	}
	if len(m.reqs) != 2 || m.reqs[0].Method != http.MethodPatch || m.reqs[1].Method != http.MethodPost {
		t.Fatalf("recreate requests = %+v", m.reqs)
	}
	cfg, _ := r.GetConfigsByNames([]string{gistConfigID})
	if cfg[gistConfigID] != "gist-recreated" {
		t.Fatalf("stored gist id = %q, want gist-recreated", cfg[gistConfigID])
	}
}

func TestGistUploadFailureRecordsLastError(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{gistConfigToken: "test-token-bad", gistConfigPassword: "enc-pw", gistConfigInterval: "0"}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		writeJSON(w, 401, map[string]interface{}{"message": "Bad credentials"})
	})
	if _, err := h.gistUpload(context.Background(), false); err == nil {
		t.Fatal("401 upload must fail")
	} else if !strings.Contains(err.Error(), "令牌无效") {
		t.Fatalf("401 error = %q, want token-invalid message", err.Error())
	}
	cfg, _ := r.GetConfigsByNames([]string{gistConfigLastAt, gistConfigLastError})
	if cfg[gistConfigLastAt] == "" || cfg[gistConfigLastError] == "" {
		t.Fatalf("failure must record lastAt/lastError: %v", cfg)
	}
	if strings.Contains(cfg[gistConfigLastError], "test-token-bad") {
		t.Fatal("lastError must not contain the token")
	}
}

func TestGistUploadWithoutTokenFails(t *testing.T) {
	h := setupGistTest(t)
	if _, err := h.gistUpload(context.Background(), false); err == nil || !strings.Contains(err.Error(), "请先填写 GitHub 令牌") {
		t.Fatalf("no-token upload err = %v", err)
	}
}

func TestGistSaveAndStatusHandlers(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		if req.Method == http.MethodGet && req.URL.Path == "/gists" {
			writeJSON(w, 200, []interface{}{})
			return
		}
		writeJSON(w, 404, map[string]interface{}{"message": "not found"})
	})

	// 非法间隔拒绝。
	rec := httptest.NewRecorder()
	h.gistBackupSave(rec, adminRequest(t, http.MethodPost, "/api/v1/backup/gist/save",
		[]byte(`{"token":"test-token-1234567890","intervalHours":7}`)))
	if code, _, _ := decodeGistEnvelope(t, rec); code == 0 {
		t.Fatal("interval 7 must be rejected")
	}

	// 非管理员拒绝。
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/gist/status", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey,
		auth.Claims{Sub: "2", User: "user", RoleID: 1}))
	h.gistBackupStatus(rec, req)
	if code, _, _ := decodeGistEnvelope(t, rec); code != 403 {
		t.Fatalf("non-admin status code = %d, want 403", code)
	}

	// 保存：令牌经 mock 校验后落库，status 只给 hint、不给令牌。
	rec = httptest.NewRecorder()
	h.gistBackupSave(rec, adminRequest(t, http.MethodPost, "/api/v1/backup/gist/save",
		[]byte(`{"token":"test-token-1234567890","password":"enc-pw","intervalHours":24}`)))
	code, _, data := decodeGistEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("save code = %d body=%s", code, rec.Body.String())
	}
	var st gistStatusView
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !st.Configured || !st.Encrypted || st.IntervalHours != 24 || st.TokenHint != "test••••••••7890" {
		t.Fatalf("saved status = %+v", st)
	}
	if strings.Contains(rec.Body.String(), "test-token-1234567890") || strings.Contains(rec.Body.String(), "enc-pw") {
		t.Fatal("save response must not contain token or password")
	}

	// status 端点同样不泄漏凭据；config/list 亦不含（管理员视角）。
	rec = httptest.NewRecorder()
	h.gistBackupStatus(rec, adminRequest(t, http.MethodPost, "/api/v1/backup/gist/status", nil))
	if code, _, _ := decodeGistEnvelope(t, rec); code != 0 {
		t.Fatalf("status code = %d body=%s", code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "test-token-1234567890") {
		t.Fatal("status response must not contain token")
	}
	rec = httptest.NewRecorder()
	h.getConfigs(rec, adminRequest(t, http.MethodPost, "/api/v1/config/list", nil))
	if strings.Contains(rec.Body.String(), "test-token-1234567890") || strings.Contains(rec.Body.String(), "enc-pw") {
		t.Fatal("config/list must not contain gist credentials even for admin")
	}
	// 备份 configs 段不含 gist 键：导出内容不带凭据与运行态。
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	for k := range backup.Configs {
		if strings.HasPrefix(k, "gist_") {
			t.Fatalf("backup configs must not contain %q", k)
		}
	}
}

// PORT-3342/G-backend（T-4，先红）：未设加密密码时上传必须拒绝，
// 不得产生任何 GitHub 请求（明文不得离境）。
func TestGistUploadPlaintextRefused(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{gistConfigToken: "test-token-1234567890", gistConfigInterval: "0"}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	m := newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		writeJSON(w, 201, map[string]interface{}{"id": "gist-must-not-happen"})
	})
	if _, err := h.gistUpload(context.Background(), false); err == nil || !strings.Contains(err.Error(), "加密密码") {
		t.Fatalf("plaintext upload err = %v, want encryption-password refusal", err)
	}
	if len(m.reqs) != 0 {
		t.Fatalf("plaintext upload must not call GitHub, requests = %+v", m.reqs)
	}
}

// PORT-3342/G-backend：versions 端点（最近 10 版本）+ 恢复侧 helper。
func TestGistVersionsListsRecent10(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{gistConfigToken: "test-token-1234567890", gistConfigID: "gist-v-id"}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	history := make([]interface{}, 0, 12)
	for i := 0; i < 12; i++ {
		history = append(history, map[string]interface{}{"version": "ver-" + strconv.Itoa(i), "committed_at": "2026-10-24T12:00:00Z"})
	}
	newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		if req.Method == http.MethodGet && req.URL.Path == "/gists/gist-v-id" {
			writeJSON(w, 200, map[string]interface{}{"id": "gist-v-id", "history": history})
			return
		}
		writeJSON(w, 404, map[string]interface{}{"message": "not found"})
	})
	rec := httptest.NewRecorder()
	h.gistBackupVersions(rec, adminRequest(t, http.MethodPost, "/api/v1/backup/gist/versions", nil))
	code, _, data := decodeGistEnvelope(t, rec)
	if code != 0 {
		t.Fatalf("versions code = %d body=%s", code, rec.Body.String())
	}
	var out struct {
		Versions []gistVersionView `json:"versions"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode versions: %v", err)
	}
	if len(out.Versions) != 10 || out.Versions[0].Version != "ver-0" || out.Versions[9].Version != "ver-9" {
		t.Fatalf("versions = %+v, want ver-0..ver-9 (10 entries)", out.Versions)
	}
	wantMs := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC).UnixMilli()
	if out.Versions[0].CommittedAt != wantMs {
		t.Fatalf("committedAt = %d, want %d", out.Versions[0].CommittedAt, wantMs)
	}
	if strings.Contains(rec.Body.String(), "test-token-1234567890") {
		t.Fatal("versions response must not contain token")
	}

	// 非管理员 403。
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/gist/versions", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey,
		auth.Claims{Sub: "2", User: "user", RoleID: 1}))
	h.gistBackupVersions(rec, req)
	if code, _, _ := decodeGistEnvelope(t, rec); code != 403 {
		t.Fatalf("non-admin versions code = %d, want 403", code)
	}

	// 无 gist_id 时明确报错，不调 GitHub。
	h2 := setupGistTest(t)
	r2 := gistTestRepo(t, h2)
	if err := r2.UpsertConfigs(map[string]string{gistConfigToken: "test-token-1234567890"}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	rec = httptest.NewRecorder()
	h2.gistBackupVersions(rec, adminRequest(t, http.MethodPost, "/api/v1/backup/gist/versions", nil))
	if code, msg, _ := decodeGistEnvelope(t, rec); code == 0 || !strings.Contains(msg, "尚未上传") {
		t.Fatalf("no-id versions code=%d msg=%q, want not-uploaded error", code, msg)
	}
}

func TestGistVersionContentForRestoreReuse(t *testing.T) {
	plain := []byte(`{"version":"1.0","users":[]}`)
	enc, err := gistEncrypt(plain, "restore-pw")
	if err != nil {
		t.Fatalf("gistEncrypt: %v", err)
	}
	var rawHits int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/gists/gist-v-id/ver-plain":
			writeJSON(w, 200, map[string]interface{}{"files": map[string]interface{}{gistFileName: map[string]interface{}{"content": string(plain)}}})
		case r.URL.Path == "/gists/gist-v-id/ver-enc":
			writeJSON(w, 200, map[string]interface{}{"files": map[string]interface{}{gistFileName: map[string]interface{}{"content": string(enc)}}})
		case r.URL.Path == "/gists/gist-v-id/ver-trunc":
			writeJSON(w, 200, map[string]interface{}{"files": map[string]interface{}{gistFileName: map[string]interface{}{"content": "stale", "truncated": true, "raw_url": srv.URL + "/raw/ver-trunc"}}})
		case r.URL.Path == "/raw/ver-trunc":
			rawHits++
			_, _ = w.Write(plain)
		case r.URL.Path == "/gists/gist-v-id/ver-nofile":
			writeJSON(w, 200, map[string]interface{}{"files": map[string]interface{}{}})
		default:
			writeJSON(w, 404, map[string]interface{}{"message": "not found"})
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FLMX_GIST_API_BASE", srv.URL)
	t.Setenv("FLVX_GIST_API_BASE", "")
	ctx := context.Background()

	got, encrypted, err := gistVersionContent(ctx, "test-token-1234567890", "gist-v-id", "ver-plain", "")
	if err != nil || encrypted || !bytes.Equal(got, plain) {
		t.Fatalf("plaintext version: got=%q encrypted=%v err=%v", got, encrypted, err)
	}
	got, encrypted, err = gistVersionContent(ctx, "test-token-1234567890", "gist-v-id", "ver-enc", "restore-pw")
	if err != nil || !encrypted || !bytes.Equal(got, plain) {
		t.Fatalf("encrypted version: got=%q encrypted=%v err=%v", got, encrypted, err)
	}
	if _, _, err := gistVersionContent(ctx, "test-token-1234567890", "gist-v-id", "ver-enc", ""); err == nil {
		t.Fatal("encrypted version without password must fail")
	}
	got, encrypted, err = gistVersionContent(ctx, "test-token-1234567890", "gist-v-id", "ver-trunc", "")
	if err != nil || encrypted || !bytes.Equal(got, plain) || rawHits != 1 {
		t.Fatalf("truncated version: got=%q encrypted=%v err=%v rawHits=%d", got, encrypted, err, rawHits)
	}
	if _, _, err := gistVersionContent(ctx, "test-token-1234567890", "gist-v-id", "ver-nofile", ""); err == nil || !strings.Contains(err.Error(), "没有备份文件") {
		t.Fatalf("missing file err = %v", err)
	}
	if _, _, err := gistVersionContent(ctx, "test-token-1234567890", "gist-v-id", "a/b", ""); err == nil || !strings.Contains(err.Error(), "版本不正确") {
		t.Fatalf("bad version err = %v", err)
	}
}

func TestGistOpenDualEnvelopeMarker(t *testing.T) {
	plain := []byte(`{"version":"1.0","users":[]}`)
	enc, err := gistEncrypt(plain, "pw-123")
	if err != nil {
		t.Fatalf("gistEncrypt: %v", err)
	}
	flmx := strings.Replace(string(enc), `"flyx": "encrypted-backup"`, `"flmx": "encrypted-backup"`, 1)
	if flmx == string(enc) {
		t.Fatal("test setup: envelope marker replacement did not apply")
	}
	got, encrypted, err := gistOpen([]byte(flmx), "pw-123")
	if err != nil || !encrypted || !bytes.Equal(got, plain) {
		t.Fatalf("flmx envelope: got=%q encrypted=%v err=%v", got, encrypted, err)
	}
	if _, _, err := gistOpen([]byte(flmx), "wrong"); err == nil {
		t.Fatal("flmx envelope wrong password must fail")
	}
}
