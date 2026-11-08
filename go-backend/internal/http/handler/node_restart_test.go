package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"go-backend/internal/store/repo"
)

func TestValidateRestartSchedule(t *testing.T) {
	valid := []struct {
		enabled int
		time    string
		jitter  int
	}{
		{1, "04:00", 30},
		{0, "00:00", 0},
		{1, "23:59", 720},
	}
	for _, c := range valid {
		if err := validateRestartSchedule(c.enabled, c.time, c.jitter); err != nil {
			t.Fatalf("expected valid %+v, got %v", c, err)
		}
	}
	invalid := []struct {
		enabled int
		time    string
		jitter  int
	}{
		{2, "04:00", 30},
		{-1, "04:00", 30},
		{1, "24:00", 30},
		{1, "4:00", 30},
		{1, "04:60", 30},
		{1, "", 30},
		{1, "04:00", -1},
		{1, "04:00", 721},
	}
	for _, c := range invalid {
		if err := validateRestartSchedule(c.enabled, c.time, c.jitter); err == nil {
			t.Fatalf("expected invalid %+v to be rejected", c)
		}
	}
}

func TestRestartSchedulePayloadShape(t *testing.T) {
	p := restartSchedulePayload(1, "04:00", 30)
	if p["enabled"] != true || p["time"] != "04:00" || p["jitterMinutes"] != 30 {
		t.Fatalf("payload shape drifted: %v", p)
	}
	p = restartSchedulePayload(0, "04:00", 30)
	if p["enabled"] != false {
		t.Fatalf("disabled must render enabled=false: %v", p)
	}
}

func seedRestartNode(t *testing.T, r *repo.Repository, id int64, isRemote int, forwardMode string) {
	t.Helper()
	err := r.DB().Exec(
		`INSERT INTO node(id, name, secret, server_ip, port, created_time, status, is_remote, forward_mode)
		 VALUES(?, ?, 'secret', '192.0.2.1', '1000-65535', 1000, 0, ?, ?)`,
		id, "restart-test-node", isRemote, forwardMode,
	).Error
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}
}

func postRestartSchedule(t *testing.T, h *Handler, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/restart-schedule", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	h.nodeRestartScheduleSet(rec, req)
	var env map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, rec.Body.String())
	}
	return env
}

func TestNodeRestartScheduleSetOfflineNodePersists(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "restart.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()
	seedRestartNode(t, r, 41, 0, "agent")
	h := New(r, "test-secret")

	// Offline node: the setting must persist and report pushed=false
	// (the reconnect hook re-pushes; see pushNodeRestartSchedule).
	env := postRestartSchedule(t, h, map[string]interface{}{
		"id": 41, "enabled": 1, "time": "03:30", "jitterMinutes": 45,
	})
	if env["code"].(float64) != 0 {
		t.Fatalf("expected success envelope, got %v", env)
	}
	data := env["data"].(map[string]interface{})
	if data["pushed"] != false {
		t.Fatalf("offline node must report pushed=false, got %v", data)
	}
	node, err := r.GetNodeByID(41)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.RestartEnabled != 1 || node.RestartTime != "03:30" || node.RestartJitterMinutes != 45 {
		t.Fatalf("setting not persisted: %+v", node)
	}

	// Turning it off persists too (default-off is a stored state,
	// not an absence the agent could misread).
	env = postRestartSchedule(t, h, map[string]interface{}{
		"id": 41, "enabled": 0, "time": "03:30", "jitterMinutes": 45,
	})
	if env["code"].(float64) != 0 {
		t.Fatalf("disable must succeed, got %v", env)
	}
	node, _ = r.GetNodeByID(41)
	if node.RestartEnabled != 0 {
		t.Fatalf("disable not persisted: %+v", node)
	}
}

func TestNodeRestartScheduleSetRejects(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "restart-reject.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()
	seedRestartNode(t, r, 42, 0, "agent")
	seedRestartNode(t, r, 43, 1, "agent")
	seedRestartNode(t, r, 44, 0, "nftables")
	h := New(r, "test-secret")

	cases := []map[string]interface{}{
		{"id": 42, "enabled": 1, "time": "24:00", "jitterMinutes": 30},  // bad time
		{"id": 42, "enabled": 1, "time": "04:00", "jitterMinutes": 721}, // jitter too large
		{"id": 42, "enabled": 2, "time": "04:00", "jitterMinutes": 30},  // bad switch
		{"id": 43, "enabled": 1, "time": "04:00", "jitterMinutes": 30},  // remote node
		{"id": 44, "enabled": 1, "time": "04:00", "jitterMinutes": 30},  // nftables node
		{"id": 999, "enabled": 1, "time": "04:00", "jitterMinutes": 30}, // missing node
	}
	for i, body := range cases {
		env := postRestartSchedule(t, h, body)
		if env["code"].(float64) == 0 {
			t.Fatalf("case %d must be rejected, got success: %v", i, env)
		}
	}
	// Nothing may have been written by the rejected calls.
	node, _ := r.GetNodeByID(42)
	if node.RestartEnabled != 0 {
		t.Fatalf("rejected writes must not persist: %+v", node)
	}
}
