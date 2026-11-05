package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// B8-13：PATCH 成功但 GitHub 响应未带 Gist 编号时，上传必须与
// 新建分支同口径失败，且不得以空 ID 覆盖已存的 gist_id、不得
// 把 hash/上传时间记成成功态。
func TestGistUploadPatchEmptyIDKeepsStoredID(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{
		gistConfigToken:    "test-token-1234567890",
		gistConfigPassword: "enc-pw",
		gistConfigID:       "gist-existing",
	}, now); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	m := newMockGistServer(t, func(w http.ResponseWriter, req *http.Request, rec *mockGistServer) {
		if req.Method == http.MethodPatch && req.URL.Path == "/gists/gist-existing" {
			// 200 但响应体不带 id（异常/代理改写形态）。
			writeJSON(w, 200, map[string]interface{}{"html_url": "https://gist.github.com/gist-existing"})
			return
		}
		writeJSON(w, 404, map[string]interface{}{"message": "not found"})
	})

	res, err := h.gistUpload(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "没有返回 Gist 编号") {
		t.Fatalf("gistUpload err = %v, want 没有返回 Gist 编号", err)
	}
	if res.Uploaded {
		t.Fatalf("result = %+v, want Uploaded=false", res)
	}
	if len(m.reqs) != 1 || m.reqs[0].Method != http.MethodPatch {
		t.Fatalf("requests = %+v, want exactly one PATCH", m.reqs)
	}
	cfg, err := r.GetConfigsByNames([]string{gistConfigID, gistConfigLastHash, gistConfigLastUpload})
	if err != nil {
		t.Fatalf("read configs: %v", err)
	}
	if cfg[gistConfigID] != "gist-existing" {
		t.Fatalf("gist_id = %q, want gist-existing (must not be overwritten with empty)", cfg[gistConfigID])
	}
	if cfg[gistConfigLastHash] != "" || cfg[gistConfigLastUpload] != "" {
		t.Fatalf("last_hash/last_upload = %q/%q, want both empty (no success state recorded)", cfg[gistConfigLastHash], cfg[gistConfigLastUpload])
	}
}
