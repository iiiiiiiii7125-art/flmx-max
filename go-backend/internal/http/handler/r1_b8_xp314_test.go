package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// X-P3-14 (L-03-3): 随机源失败三处统一 fail-closed。修复前：
// randomToken 降级为时间戳 hex（可预测）、订阅 token 两处忽略
// rand 错误（落全零凭据）。注入 rand 失败后三处均须报错且不落库。
func TestR1B8_XP314_RandFailureFailsClosed(t *testing.T) {
	orig := cryptoRandRead
	cryptoRandRead = func([]byte) (int, error) { return 0, errors.New("rand boom") }
	defer func() { cryptoRandRead = orig }()

	if _, err := randomToken(16); err == nil {
		t.Fatalf("randomToken 在 rand 失败时必须返回错误（不得降级时间戳）")
	}
	if _, err := randomHex(16); err == nil {
		t.Fatalf("randomHex 在 rand 失败时必须返回错误")
	}
	if _, err := newUUID(); err == nil {
		t.Fatalf("newUUID 在 rand 失败时必须返回错误")
	}

	r, err := repo.Open(filepath.Join(t.TempDir(), "xp314.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, "unit-test-secret")

	// 节点创建：secret 生成失败 → 报错且无节点行。
	body := []byte(`{"name":"xp314-node","serverIp":"10.0.3.9"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.nodeCreate(rec, req)
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Code == 0 {
		t.Fatalf("rand 失败时 nodeCreate 应报错, got %+v", out)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name='xp314-node'`); n != 0 {
		t.Fatalf("rand 失败时不应落节点行, got %d", n)
	}

	// 订阅创建：token 生成失败 → 报错且无订阅行、无全零 token 落库。
	body = []byte(`{"name":"xp314-sub","sourceAll":true}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/subscription/save", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	h.subscriptionSave(rec, req)
	out = response.R{}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Code == 0 {
		t.Fatalf("rand 失败时 subscriptionSave 应报错, got %+v", out)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription WHERE name='xp314-sub'`); n != 0 {
		t.Fatalf("rand 失败时不应落订阅行, got %d", n)
	}
}
