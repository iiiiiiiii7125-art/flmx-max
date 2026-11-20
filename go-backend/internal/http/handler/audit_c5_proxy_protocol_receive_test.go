package handler

import (
	"strings"
	"testing"
	"time"
)

// R18-C5-01 (receipt leg): a forward with PROXY receive on but
// no panel-known trusted sender must not arm receive — and the
// sync receipt must say so instead of silently leaving the entry
// open to forged PROXY headers. These tests drive the pre-existing
// syncForwardServicesWithWarnings entry point, so they fail on old
// code with ordinary assertion failures (no new symbol involved).

func c5SeedReceiver(t *testing.T, h *Handler) *forwardRecord {
	t.Helper()
	seedK9Type2Tunnel(t, h, 88, "c5-tunnel", 36008, 35009)
	now := time.Now().UnixMilli()
	forwardID, err := h.repo.CreateForwardTx(
		1, "admin", "c5-receiver", 88, "1.1.1.1:443", "fifo", now, 1,
		[]int64{10}, 30005, "", nil, 0, 0, nil, 0, 1, 0,
	)
	if err != nil {
		t.Fatalf("create receiver forward: %v", err)
	}
	forward, err := h.getForwardRecord(forwardID)
	if err != nil {
		t.Fatalf("get receiver forward: %v", err)
	}
	return forward
}

// c5SeedSender inserts a second forward whose send side dials the
// receiver's entry (node 10, port 30005) from node 11, via raw SQL
// (its entry/tunnel pairing is irrelevant to the allowlist scan,
// which reads forward rows and port rows only).
func c5SeedSender(t *testing.T, h *Handler, remoteAddr string, senderEntryNodeID int64, senderPort int) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := h.repo.DB().Exec(
		`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, created_time, updated_time, status, inx, proxy_protocol, proxy_protocol_receive, proxy_protocol_send)
		 VALUES(9001, 1, 'admin', 'c5-sender', 88, ?, 'fifo', ?, ?, 1, 1, 0, 0, 2)`,
		remoteAddr, now, now,
	).Error; err != nil {
		t.Fatalf("insert sender forward: %v", err)
	}
	if err := h.repo.DB().Exec(
		`INSERT INTO forward_port(forward_id, node_id, port) VALUES(9001, ?, ?)`,
		senderEntryNodeID, senderPort,
	).Error; err != nil {
		t.Fatalf("insert sender forward port: %v", err)
	}
}

func TestAuditC5ProxyProtocolReceiveNoSenderWarns(t *testing.T) {
	h := setupK9Handler(t)
	forward := c5SeedReceiver(t, h)
	// Node 10 has no agent connected, so the sync itself skips it
	// as offline — but the receive-disabled warning must already
	// be in the receipt before that.
	warnings, err := h.syncForwardServicesWithWarnings(forward, "UpdateService", true)
	if err != nil {
		t.Fatalf("sync with offline entry must not fail, got %v", err)
	}
	joined := strings.Join(warnings, "；")
	if !strings.Contains(joined, "未找到面板已知的可信 PROXY 发送方") {
		t.Fatalf("receipt must warn that PROXY receive is disabled without a trusted sender, got warnings=%q", joined)
	}
	if !strings.Contains(joined, "c5-receiver") {
		t.Fatalf("receipt warning must name the forward, got warnings=%q", joined)
	}
	if !strings.Contains(joined, offlineSkipWarningMarker) {
		t.Fatalf("receipt must still carry the offline skip, got warnings=%q", joined)
	}
}

func TestAuditC5ProxyProtocolReceiveKnownSenderNoWarn(t *testing.T) {
	h := setupK9Handler(t)
	forward := c5SeedReceiver(t, h)
	// Sender on node 11 (10.0.0.2) dials the receiver entry
	// 10.0.0.1:30005 with PROXY send on: a panel-known sender.
	c5SeedSender(t, h, "10.0.0.1:30005", 11, 35005)

	warnings, err := h.syncForwardServicesWithWarnings(forward, "UpdateService", true)
	if err != nil {
		t.Fatalf("sync with offline entry must not fail, got %v", err)
	}
	joined := strings.Join(warnings, "；")
	if strings.Contains(joined, "未找到面板已知的可信 PROXY 发送方") {
		t.Fatalf("receipt must not claim no trusted sender when one dials this entry, got warnings=%q", joined)
	}
	if !strings.Contains(joined, offlineSkipWarningMarker) {
		t.Fatalf("receipt must still carry the offline skip, got warnings=%q", joined)
	}
}
