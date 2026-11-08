package repo

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// C3-02 regression: the pinned SSH host key fingerprint lifecycle at the
// repository layer. A pin survives config saves that keep the endpoint
// identity (host/port/username), is cleared when the endpoint changes
// (the new endpoint must re-TOFU), and pinning a nonexistent config row
// is an error rather than a silent no-op.
func TestNodeSSHHostKeyFingerprintPinLifecycle(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	if _, err := r.CreateNode(
		"nft-hostkey-node",
		"secret",
		"203.0.113.10",
		nil,
		nil,
		"10000-20000",
		nil,
		nil,
		nil,
		nil,
		nil,
		0,
		0,
		0,
		now,
		1,
		"[::]",
		"[::]",
		1,
		nil,
		"nftables",
	); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	nodes, err := r.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	nodeID := nodes[0]["id"].(int64)

	cfg := NftSSHConfigInput{
		Host:     "203.0.113.10",
		Port:     22,
		Username: "root",
		AuthType: "password",
		Password: "secret",
		SudoMode: "none",
	}
	if err := r.UpsertNodeSSHConfig(nodeID, cfg, now); err != nil {
		t.Fatalf("UpsertNodeSSHConfig: %v", err)
	}
	loaded, err := r.GetNodeSSHConfig(nodeID)
	if err != nil {
		t.Fatalf("GetNodeSSHConfig: %v", err)
	}
	if loaded.HostKeyFingerprint != "" {
		t.Fatalf("fresh config must start unpinned, got %q", loaded.HostKeyFingerprint)
	}

	const pin = "SHA256:AAAABBBBCCCCDDDDEEEEFFFFGGGGHHHHIIIIJJJJKKK"
	if err := r.UpdateNodeSSHHostKeyFingerprint(nodeID, pin, now+1); err != nil {
		t.Fatalf("UpdateNodeSSHHostKeyFingerprint: %v", err)
	}
	loaded, err = r.GetNodeSSHConfig(nodeID)
	if err != nil {
		t.Fatalf("GetNodeSSHConfig after pin: %v", err)
	}
	if loaded.HostKeyFingerprint != pin {
		t.Fatalf("expected pin %q, got %q", pin, loaded.HostKeyFingerprint)
	}

	// Re-saving the same endpoint (credential rotation) preserves the pin.
	rotated := cfg
	rotated.Password = "rotated-secret"
	if err := r.UpsertNodeSSHConfig(nodeID, rotated, now+2); err != nil {
		t.Fatalf("UpsertNodeSSHConfig same endpoint: %v", err)
	}
	loaded, err = r.GetNodeSSHConfig(nodeID)
	if err != nil {
		t.Fatalf("GetNodeSSHConfig after re-save: %v", err)
	}
	if loaded.HostKeyFingerprint != pin {
		t.Fatalf("pin must survive a same-endpoint save, got %q", loaded.HostKeyFingerprint)
	}

	// Changing the endpoint clears the pin: the new endpoint must re-TOFU.
	moved := cfg
	moved.Host = "203.0.113.11"
	if err := r.UpsertNodeSSHConfig(nodeID, moved, now+3); err != nil {
		t.Fatalf("UpsertNodeSSHConfig moved endpoint: %v", err)
	}
	loaded, err = r.GetNodeSSHConfig(nodeID)
	if err != nil {
		t.Fatalf("GetNodeSSHConfig after move: %v", err)
	}
	if loaded.HostKeyFingerprint != "" {
		t.Fatalf("pin must be cleared when the endpoint changes, got %q", loaded.HostKeyFingerprint)
	}

	if err := r.UpdateNodeSSHHostKeyFingerprint(nodeID+9999, pin, now+4); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("pinning a nonexistent config must report not-found, got %v", err)
	}
}

// C3-02 follow-up (PV-8): the node list DTO must carry the pinned
// fingerprint in sshConfig so a reopened edit form can show the pin
// (the frontend reads node.sshConfig.hostKeyFingerprint; the
// test-connection receipt already exposed the same key). Before the
// fix the map had no hostKeyFingerprint key at all.
func TestListNodesExposesPinnedHostKeyFingerprint(t *testing.T) {
	r, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	if _, err := r.CreateNode(
		"nft-listdto-node",
		"secret",
		"203.0.113.20",
		nil,
		nil,
		"10000-20000",
		nil,
		nil,
		nil,
		nil,
		nil,
		0,
		0,
		0,
		now,
		1,
		"[::]",
		"[::]",
		1,
		nil,
		"nftables",
	); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	nodes, err := r.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	nodeID := nodes[0]["id"].(int64)
	cfg := NftSSHConfigInput{
		Host:     "203.0.113.20",
		Port:     22,
		Username: "root",
		AuthType: "password",
		Password: "pw",
		SudoMode: "none",
	}
	if err := r.UpsertNodeSSHConfig(nodeID, cfg, now); err != nil {
		t.Fatalf("UpsertNodeSSHConfig: %v", err)
	}

	sshConfigOf := func() map[string]interface{} {
		t.Helper()
		items, err := r.ListNodes()
		if err != nil {
			t.Fatalf("ListNodes: %v", err)
		}
		sc, ok := items[0]["sshConfig"].(map[string]interface{})
		if !ok {
			t.Fatalf("sshConfig missing or wrong type: %T", items[0]["sshConfig"])
		}
		return sc
	}

	if got, ok := sshConfigOf()["hostKeyFingerprint"]; !ok || got != "" {
		t.Fatalf("unpinned node: hostKeyFingerprint must be present and empty, got %v (present=%v)", got, ok)
	}

	const pin = "SHA256:ZZZZYYYYXXXXWWWWVVVVUUUUTTTTSSSSRRRRQQQQPPP"
	if err := r.UpdateNodeSSHHostKeyFingerprint(nodeID, pin, now+1); err != nil {
		t.Fatalf("UpdateNodeSSHHostKeyFingerprint: %v", err)
	}
	if got := sshConfigOf()["hostKeyFingerprint"]; got != pin {
		t.Fatalf("pinned node: ListNodes sshConfig must carry the pin %q, got %v", pin, got)
	}
}
