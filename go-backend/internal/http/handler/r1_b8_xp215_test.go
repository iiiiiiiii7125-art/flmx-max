package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// X-P2-15 (L-09-1): nodeCreate 第二步 persistNodeSSHConfig 失败时必须
// 补偿删除刚建节点行，不得留无 SSH 配置的半成品节点。
func TestR1B8_XP215_NodeCreateSSHConfigFailureLeavesNoNode(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "xp215.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, "unit-test-secret")

	body := []byte(`{"name":"xp215-node","serverIp":"10.0.0.9","forwardMode":"nftables","sshConfig":{"port":"not-a-port"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.nodeCreate(rec, req)

	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Code == 0 {
		t.Fatalf("expected failure response for invalid ssh port, got %+v", out)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name = 'xp215-node'`); n != 0 {
		t.Fatalf("半成品节点残留: node rows = %d, want 0", n)
	}

	// 防过修：合法创建仍成功落库。
	body = []byte(`{"name":"xp215-ok","serverIp":"10.0.0.10","forwardMode":"nftables","sshConfig":{"port":22,"username":"root","authType":"password","password":"x"}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/node/create", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	h.nodeCreate(rec, req)
	out = response.R{}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Code != 0 {
		t.Fatalf("valid create should succeed, got %+v", out)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name = 'xp215-ok'`); n != 1 {
		t.Fatalf("valid node rows = %d, want 1", n)
	}
}
