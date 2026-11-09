package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// IRL-7 (fix wave 212, lane 5): the inbound list's relayCount must count
// only relays that can actually carry traffic — backing forward exists
// AND is enabled (status 1), the same predicate relayViews uses. Relays
// whose forward was deleted or paused go to relayInvalidCount so the UI
// can flag them instead of inflating the headline count. The pre-fix
// code counted every relay row (relayCount would be 3 here and the
// invalid field did not exist), so this test fails on the old shape.
func TestInboundListRelayCountsExcludeDeadForwards(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/irl7.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatal(err)
	}

	nodeID := seedRelayTargetNode(t, r, "irl7-node", "203.0.113.40")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	inbound := seedChainInbound(t, r, k, "irl7-in", kernel.Inbound{
		Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality},
	}, 0)

	frontID := seedRelayTargetNode(t, r, "irl7-front", "198.51.100.9")
	tunnelID := seedRelayTargetTunnel(t, r, "irl7-tun", frontID, 30000, nodeID)

	mkForward := func(name string, port int) int64 {
		id, err := r.CreateForwardTx(1, "admin", name, tunnelID, "203.0.113.40:443", "fifo", now, 1,
			[]int64{frontID}, port, "", nil, 0, 0, nil, 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	liveForward := mkForward("irl7-live", 30011)
	pausedForward := mkForward("irl7-paused", 30012)
	if err := r.UpdateForwardStatus(pausedForward, 0, now); err != nil {
		t.Fatal(err)
	}

	mkRelay := func(name string, forwardID int64) {
		if err := r.CreateInboundRelay(&model.NodeInboundRelay{
			InboundID: inbound.ID, ForwardID: forwardID, Name: name,
			Managed: 1, CreatedTime: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mkRelay("irl7-live", liveForward)
	mkRelay("irl7-paused", pausedForward)
	mkRelay("irl7-dangling", 999999) // forward row does not exist

	rec := httptest.NewRecorder()
	h.inboundList(rec, httptest.NewRequest(http.MethodPost, "/api/v1/inbound/list", strings.NewReader("{}")))
	var env struct {
		Code int `json:"code"`
		Data []struct {
			ID                int64 `json:"id"`
			RelayCount        int   `json:"relayCount"`
			RelayInvalidCount int   `json:"relayInvalidCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("inbound list: code=%d err=%v body=%s", env.Code, err, rec.Body.String())
	}
	for _, v := range env.Data {
		if v.ID != inbound.ID {
			continue
		}
		if v.RelayCount != 1 {
			t.Fatalf("relayCount = %d, want 1 (only the live forward's relay)", v.RelayCount)
		}
		if v.RelayInvalidCount != 2 {
			t.Fatalf("relayInvalidCount = %d, want 2 (paused + dangling)", v.RelayInvalidCount)
		}
		return
	}
	t.Fatal("seeded inbound missing from list")
}
