package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/repo"
)

// setupHandlerTestRepo builds a Handler backed by a throwaway repo in
// a temp dir.
func setupHandlerTestRepo(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	return h
}

func handlerTestRepo(t *testing.T, h *Handler) *repo.Repository {
	t.Helper()
	// New() 不暴露 repo；测试经 Handler 的 repo 字段取（同包）。
	return h.repo
}

// adminRequest builds a request carrying admin claims in context.
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

func mustLastInsertID(t *testing.T, r *repo.Repository, label string) int64 {
	t.Helper()
	var id int64
	if err := r.DB().Raw("SELECT last_insert_rowid()").Row().Scan(&id); err != nil {
		t.Fatalf("read last_insert_rowid for %s: %v", label, err)
	}
	if id <= 0 {
		t.Fatalf("invalid last_insert_rowid for %s: %d", label, id)
	}
	return id
}

func mustQueryInt(t *testing.T, r *repo.Repository, query string, args ...interface{}) int {
	t.Helper()
	var v int
	if err := r.DB().Raw(query, args...).Row().Scan(&v); err != nil {
		t.Fatalf("query int failed: %v (query=%q)", err, query)
	}
	return v
}

func mustQueryInt64Int64String(t *testing.T, r *repo.Repository, query string, args ...interface{}) (int64, int64, string) {
	t.Helper()
	var a int64
	var b int64
	var c string
	if err := r.DB().Raw(query, args...).Row().Scan(&a, &b, &c); err != nil {
		t.Fatalf("query int64+int64+string failed: %v (query=%q)", err, query)
	}
	return a, b, c
}

func mustQueryInt64Int64Int(t *testing.T, r *repo.Repository, query string, args ...interface{}) (int64, int64, int) {
	t.Helper()
	var a int64
	var b int64
	var c int
	if err := r.DB().Raw(query, args...).Row().Scan(&a, &b, &c); err != nil {
		t.Fatalf("query int64+int64+int failed: %v (query=%q)", err, query)
	}
	return a, b, c
}
