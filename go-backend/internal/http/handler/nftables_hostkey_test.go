package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
)

// C3-02 regression: the node 测连通 endpoint is the TOFU pinning path for
// the SSH host key fingerprint. First connect (unpinned) probes the
// presented key, verifies the full capability test against that probed
// pin, and only then persists it; a pinned node never re-probes (no
// silent re-pin); resetHostKey=true is the explicit operator re-pin.

type nftEnvelope struct {
	Code int                    `json:"code"`
	Msg  string                 `json:"msg"`
	Data map[string]interface{} `json:"data"`
}

func decodeNftEnvelope(t *testing.T, res *httptest.ResponseRecorder) nftEnvelope {
	t.Helper()
	var env nftEnvelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return env
}

func installProbeStub(h *Handler, manager *fakeNftablesManager, probe func(context.Context, runtimenft.SSHConfig) (string, error)) *serializedNftManager {
	sm := newSerializedNftManager(manager)
	sm.probeHostKey = probe
	h.nftablesManager = sm
	return sm
}

func TestNodeNftablesTestPinsHostKeyOnFirstConnect(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	manager := &fakeNftablesManager{}
	installProbeStub(h, manager, func(context.Context, runtimenft.SSHConfig) (string, error) {
		return "SHA256:probed-fingerprint", nil
	})

	res := postJSONToHandler(t, h.nodeNftablesTest, map[string]interface{}{"nodeId": fixture.nodeID})
	env := decodeNftEnvelope(t, res)
	if env.Code != 0 {
		t.Fatalf("expected success, got %+v", env)
	}
	if env.Data["hostKeyFingerprint"] != "SHA256:probed-fingerprint" || env.Data["hostKeyPinned"] != true {
		t.Fatalf("expected pinning receipt with the probed fingerprint, got %+v", env.Data)
	}
	if manager.lastConfig.HostKeyFingerprint != "SHA256:probed-fingerprint" {
		t.Fatalf("capability test must run against the probed pin, got %+v", manager.lastConfig)
	}
	cfg, err := h.repo.GetNodeSSHConfig(fixture.nodeID)
	if err != nil {
		t.Fatalf("get ssh config: %v", err)
	}
	if cfg.HostKeyFingerprint != "SHA256:probed-fingerprint" {
		t.Fatalf("pin must be persisted after a successful test, got %q", cfg.HostKeyFingerprint)
	}
}

func TestNodeNftablesTestPinnedNodeSkipsProbe(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	if err := h.repo.UpdateNodeSSHHostKeyFingerprint(fixture.nodeID, "SHA256:stored-pin", time.Now().UnixMilli()); err != nil {
		t.Fatalf("pin: %v", err)
	}
	manager := &fakeNftablesManager{}
	probeCalled := false
	installProbeStub(h, manager, func(context.Context, runtimenft.SSHConfig) (string, error) {
		probeCalled = true
		return "", errors.New("probe must not run for a pinned node")
	})

	res := postJSONToHandler(t, h.nodeNftablesTest, map[string]interface{}{"nodeId": fixture.nodeID})
	env := decodeNftEnvelope(t, res)
	if env.Code != 0 {
		t.Fatalf("expected success, got %+v", env)
	}
	if probeCalled {
		t.Fatalf("a pinned node must never be silently re-probed/re-pinned")
	}
	if manager.lastConfig.HostKeyFingerprint != "SHA256:stored-pin" {
		t.Fatalf("capability test must run against the stored pin, got %+v", manager.lastConfig)
	}
}

func TestNodeNftablesTestResetHostKeyRepins(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	if err := h.repo.UpdateNodeSSHHostKeyFingerprint(fixture.nodeID, "SHA256:old-pin", time.Now().UnixMilli()); err != nil {
		t.Fatalf("pin: %v", err)
	}
	manager := &fakeNftablesManager{}
	installProbeStub(h, manager, func(context.Context, runtimenft.SSHConfig) (string, error) {
		return "SHA256:new-pin", nil
	})

	res := postJSONToHandler(t, h.nodeNftablesTest, map[string]interface{}{"nodeId": fixture.nodeID, "resetHostKey": true})
	env := decodeNftEnvelope(t, res)
	if env.Code != 0 {
		t.Fatalf("expected success, got %+v", env)
	}
	if env.Data["hostKeyRepinned"] != true || env.Data["hostKeyFingerprint"] != "SHA256:new-pin" {
		t.Fatalf("expected re-pin receipt, got %+v", env.Data)
	}
	cfg, err := h.repo.GetNodeSSHConfig(fixture.nodeID)
	if err != nil {
		t.Fatalf("get ssh config: %v", err)
	}
	if cfg.HostKeyFingerprint != "SHA256:new-pin" {
		t.Fatalf("re-pin must overwrite the stored pin, got %q", cfg.HostKeyFingerprint)
	}
}

func TestNodeNftablesTestMismatchSurfacesAndDoesNotRepin(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	if err := h.repo.UpdateNodeSSHHostKeyFingerprint(fixture.nodeID, "SHA256:stored-pin", time.Now().UnixMilli()); err != nil {
		t.Fatalf("pin: %v", err)
	}
	manager := &fakeNftablesManager{testErr: &runtimenft.HostKeyMismatchError{Expected: "SHA256:stored-pin", Actual: "SHA256:attacker-key"}}
	probeCalled := false
	installProbeStub(h, manager, func(context.Context, runtimenft.SSHConfig) (string, error) {
		probeCalled = true
		return "SHA256:attacker-key", nil
	})

	res := postJSONToHandler(t, h.nodeNftablesTest, map[string]interface{}{"nodeId": fixture.nodeID})
	env := decodeNftEnvelope(t, res)
	if env.Code == 0 {
		t.Fatalf("a host key mismatch must fail the test request, got %+v", env)
	}
	if !strings.Contains(env.Msg, "指纹不匹配") {
		t.Fatalf("expected the mismatch message to surface, got %q", env.Msg)
	}
	if probeCalled {
		t.Fatalf("a mismatch must never trigger a silent re-probe/re-pin")
	}
	cfg, err := h.repo.GetNodeSSHConfig(fixture.nodeID)
	if err != nil {
		t.Fatalf("get ssh config: %v", err)
	}
	if cfg.HostKeyFingerprint != "SHA256:stored-pin" {
		t.Fatalf("stored pin must be untouched after a mismatch, got %q", cfg.HostKeyFingerprint)
	}
}

func TestNodeNftablesTestProbeFailureLeavesNodeUnpinned(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	manager := &fakeNftablesManager{}
	installProbeStub(h, manager, func(context.Context, runtimenft.SSHConfig) (string, error) {
		return "", errors.New("dial failed")
	})

	res := postJSONToHandler(t, h.nodeNftablesTest, map[string]interface{}{"nodeId": fixture.nodeID})
	env := decodeNftEnvelope(t, res)
	if env.Code == 0 {
		t.Fatalf("a failed probe must fail the test request, got %+v", env)
	}
	if manager.lastConfig.Host != "" {
		t.Fatalf("capability test must not run when the probe failed, got %+v", manager.lastConfig)
	}
	cfg, err := h.repo.GetNodeSSHConfig(fixture.nodeID)
	if err != nil {
		t.Fatalf("get ssh config: %v", err)
	}
	if cfg.HostKeyFingerprint != "" {
		t.Fatalf("a failed probe must leave the node unpinned, got %q", cfg.HostKeyFingerprint)
	}
}
