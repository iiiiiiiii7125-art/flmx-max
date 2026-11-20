package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R18-B7-01 (ADJ-7): a Type 2 tunnel whose entry nodes are all gone
// (e.g. the v6 federation-removal migration deleted its remote entry
// node) must fail closed at deploy/redeploy time instead of silently
// applying an empty entry set. Both the validation gate and the
// apply-time defensive guard reject it.
func TestR18B701Type2TunnelWithoutEntryNodesFailsClosed(t *testing.T) {
	if err := validateTunnelRuntimeBeforeApply(&tunnelCreateState{Type: 2}); err == nil {
		t.Fatal("validate must reject a Type 2 tunnel with no entry nodes")
	} else if !strings.Contains(err.Error(), "缺少入口节点") {
		t.Fatalf("validate must name the missing entry nodes, got %v", err)
	}

	r, err := repo.Open(t.TempDir() + "/b701-entry-gate.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	if _, _, err := h.applyTunnelRuntimeWithMode(&tunnelCreateState{Type: 2, Nodes: map[int64]*nodeRecord{}}, false); err == nil {
		t.Fatal("apply must reject a Type 2 tunnel with no entry nodes")
	} else if !strings.Contains(err.Error(), "缺少入口节点") {
		t.Fatalf("apply must name the missing entry nodes, got %v", err)
	}

	// Controls: a Type 1 tunnel has no entry gate on either path.
	if err := validateTunnelRuntimeBeforeApply(&tunnelCreateState{Type: 1}); err != nil {
		t.Fatalf("validate must not gate Type 1 tunnels, got %v", err)
	}
	if _, _, err := h.applyTunnelRuntimeWithMode(&tunnelCreateState{Type: 1}, false); err != nil {
		t.Fatalf("apply must not gate Type 1 tunnels, got %v", err)
	}
}

// R18-B7-01 (migration side, handler face): tunnelList flags tunnels
// recorded in the v6 orphan-todo key, and the flag clears per tunnel
// once handled (redeploy/delete); the key itself is deleted once empty.
func TestR18B701TunnelListFlagsOrphanAndClearRemovesIt(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b701-orphan-flag.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	if err := r.DB().Exec("INSERT INTO tunnel (name, type, flow, status, created_time, updated_time) VALUES ('flag-tun', 2, 0, 1, 1, 1)").Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	var tunnelID int64
	if err := r.DB().Table("tunnel").Where("name = ?", "flag-tun").Pluck("id", &tunnelID).Error; err != nil {
		t.Fatalf("read tunnel id: %v", err)
	}
	if err := r.UpsertConfig(repo.RMShareOrphanTunnelsKey, fmt.Sprintf("[%d,999]", tunnelID), now); err != nil {
		t.Fatalf("seed orphan key: %v", err)
	}

	rec := httptest.NewRecorder()
	h.tunnelList(rec, httptest.NewRequest("POST", "/api/v1/tunnel/list", nil))
	var env struct {
		Code int                      `json:"code"`
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode tunnel list: %v (body=%s)", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("tunnel list must succeed, got code=%d body=%s", env.Code, rec.Body.String())
	}
	flagged := false
	for _, item := range env.Data {
		if id, ok := item["id"].(float64); ok && int64(id) == tunnelID {
			if item["orphanedByFederationRemoval"] == true {
				flagged = true
			}
		}
	}
	if !flagged {
		t.Fatalf("tunnel %d must be flagged orphanedByFederationRemoval, got %+v", tunnelID, env.Data)
	}

	h.clearOrphanTunnelFlag(tunnelID)
	cfg, err := r.GetConfigByName(repo.RMShareOrphanTunnelsKey)
	if err != nil || cfg == nil || cfg.Value != "[999]" {
		t.Fatalf("after clearing one tunnel the key must be [999], got %+v err=%v", cfg, err)
	}
	h.clearOrphanTunnelFlag(999)
	cfg, err = r.GetConfigByName(repo.RMShareOrphanTunnelsKey)
	if err != nil || cfg != nil {
		t.Fatalf("after clearing the last tunnel the key must be deleted, got %+v err=%v", cfg, err)
	}
}
