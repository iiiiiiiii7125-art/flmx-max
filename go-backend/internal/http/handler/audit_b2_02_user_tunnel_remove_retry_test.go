package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go-backend/internal/store/repo"

	"gorm.io/gorm"
)

var errB202Injected = errors.New("b2-02 injected delete failure")

// failDeletesMatching fails the first failCount Delete operations whose
// statement matches, then lets later deletes through — the Delete-side
// analogue of failQueriesMatching (same cancel-context + AddError
// mechanism). It returns a remover; t.Cleanup removes the callback.
func failDeletesMatching(t *testing.T, r *repo.Repository, failCount int, match func(tx *gorm.DB) bool) func() {
	t.Helper()
	name := "b202_fail_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	var count atomic.Int32
	err := r.DB().Callback().Delete().Before("gorm:delete").Register(name, func(tx *gorm.DB) {
		if !match(tx) {
			return
		}
		if count.Add(1) <= int32(failCount) {
			ctx, cancel := context.WithCancel(tx.Statement.Context)
			tx.Statement.Context = ctx
			cancel()
			tx.AddError(errB202Injected)
		}
	})
	if err != nil {
		t.Fatalf("register delete fault callback: %v", err)
	}
	remove := func() { _ = r.DB().Callback().Delete().Remove(name) }
	t.Cleanup(remove)
	return remove
}

func seedB202UserTunnel(t *testing.T, r *repo.Repository) int64 {
	t.Helper()
	if err := r.DB().Exec("INSERT INTO tunnel (name, type, flow, status, created_time, updated_time) VALUES ('b2-02-tun', 2, 0, 1, 1, 1)").Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	var tunnelID int64
	if err := r.DB().Table("tunnel").Where("name = ?", "b2-02-tun").Pluck("id", &tunnelID).Error; err != nil {
		t.Fatalf("read tunnel id: %v", err)
	}
	if err := r.DB().Exec("INSERT INTO user_tunnel (user_id, tunnel_id, num, flow, flow_reset_time, exp_time, status) VALUES (1, ?, 1, 0, 0, 0, 1)", tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	var id int64
	if err := r.DB().Table("user_tunnel").Where("tunnel_id = ?", tunnelID).Pluck("id", &id).Error; err != nil {
		t.Fatalf("read user_tunnel id: %v", err)
	}
	return id
}

func callUserTunnelRemove(t *testing.T, h *Handler, id int64) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.userTunnelRemove(rec, httptest.NewRequest("POST", "/api/v1/user_tunnel/remove", strings.NewReader(`{"id":`+itoaB202(id)+`}`)))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode remove response: %v (body=%s)", err, rec.Body.String())
	}
	return env.Code, env.Msg
}

func itoaB202(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func b202RowExists(t *testing.T, r *repo.Repository, id int64) bool {
	t.Helper()
	var n int64
	if err := r.DB().Table("user_tunnel").Where("id = ?", id).Count(&n).Error; err != nil {
		t.Fatalf("count user_tunnel: %v", err)
	}
	return n > 0
}

// R18-B2-02: userTunnelRemove cleans the data-plane forwards before
// deleting the auth row; a delete-row failure used to return a bare
// "Internal error" while the forwards were already gone. The delete
// is retried once, and a persistent failure names the partial
// completion in the receipt.
func TestR18B202UserTunnelRemoveDeleteRetry(t *testing.T) {
	t.Run("transient delete failure heals on retry", func(t *testing.T) {
		r, err := repo.Open(t.TempDir() + "/b202-retry.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		h := New(r, "test-secret")
		id := seedB202UserTunnel(t, r)
		failDeletesMatching(t, r, 1, isTable("user_tunnel"))
		code, msg := callUserTunnelRemove(t, h, id)
		if code != 0 {
			t.Fatalf("retry must heal a transient delete failure, got code=%d msg=%q", code, msg)
		}
		if b202RowExists(t, r, id) {
			t.Fatal("auth row must be gone after the retried delete")
		}
	})

	t.Run("persistent delete failure names partial completion", func(t *testing.T) {
		r, err := repo.Open(t.TempDir() + "/b202-partial.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		h := New(r, "test-secret")
		id := seedB202UserTunnel(t, r)
		failDeletesMatching(t, r, 100, isTable("user_tunnel"))
		code, msg := callUserTunnelRemove(t, h, id)
		if code == 0 {
			t.Fatalf("persistent delete failure must not report success, got code=%d msg=%q", code, msg)
		}
		if !strings.Contains(msg, "转发已清理，授权行删除失败，请重试") {
			t.Fatalf("receipt must name the partial completion, got msg=%q", msg)
		}
		if !b202RowExists(t, r, id) {
			t.Fatal("auth row must still exist after a failed delete")
		}
	})
}
