package handler

import (
	"strings"
	"testing"

	"go-backend/internal/ws"
)

// R6-05 receipt gates, as revised by M-0051: the node rollback
// receipt is a DISPATCH receipt, not a landing certificate. The
// agent handler returns as soon as the detached rollback script is
// spawned (the handler process dies in the restart), so at dispatch
// time nobody can know whether the swap landed — the pre-M-0051
// receipt named the target as "toVersion" under a success toast and
// a silently failed script stayed invisible. The receipt must name
// the target as expectedVersion, carry status "dispatched", and tell
// the operator to verify the landed version in the node list after
// reconnect. The cross-path generation skip (R6-05's original
// concern) stays visible: the target version is still named, only
// as a target, not as a landing.

func TestRollbackReceiptNamesTargetAsExpected(t *testing.T) {
	data := rollbackReceiptData(ws.CommandResult{
		Success: true,
		Message: "OK",
		Data: map[string]interface{}{
			"fromVersion":      "3.0.0",
			"toVersion":        "1.0.0",
			"backupGeneration": "previous",
		},
	})
	if data["status"] != "dispatched" {
		t.Fatalf("receipt must be a dispatch receipt, got %+v", data)
	}
	if data["expectedVersion"] != "1.0.0" || data["fromVersion"] != "3.0.0" {
		t.Fatalf("receipt must carry target and source versions, got %+v", data)
	}
	if _, ok := data["toVersion"]; ok {
		t.Fatalf("receipt must not name a landing version, got %+v", data)
	}
	if data["backupGeneration"] != "previous" {
		t.Fatalf("generation must pass through, got %+v", data)
	}
	msg, _ := data["message"].(string)
	if !strings.Contains(msg, "1.0.0") {
		t.Fatalf("receipt message must name the target version, got %q", msg)
	}
	if !strings.Contains(msg, "核验") {
		t.Fatalf("receipt message must tell the operator to verify the landing, got %q", msg)
	}
	if strings.Contains(msg, "未经紧邻上一代校验") {
		t.Fatalf("verified generation must not be flagged unverified, got %q", msg)
	}
}

func TestRollbackReceiptDisclosesLegacyBackup(t *testing.T) {
	data := rollbackReceiptData(ws.CommandResult{
		Success: true,
		Message: "OK",
		Data: map[string]interface{}{
			"toVersion":        "1.0.0",
			"backupGeneration": "legacy-unknown",
		},
	})
	if data["status"] != "dispatched" || data["expectedVersion"] != "1.0.0" {
		t.Fatalf("legacy receipt must be a dispatch receipt naming the target, got %+v", data)
	}
	msg, _ := data["message"].(string)
	if !strings.Contains(msg, "1.0.0") || !strings.Contains(msg, "未经紧邻上一代校验") {
		t.Fatalf("legacy backup receipt must name the version and disclose the unverified generation, got %q", msg)
	}
}

func TestRollbackReceiptOldAgentDisclosesUnverified(t *testing.T) {
	// R7-13: an agent from before the pairing mechanism reports no
	// data at all — its rollback ran with no generation check, so
	// the panel receipt must carry the same unverified disclosure
	// as the legacy-backup cell instead of the bare agent message.
	data := rollbackReceiptData(ws.CommandResult{Success: true, Message: "OK"})
	if data["status"] != "dispatched" {
		t.Fatalf("old-agent receipt must be a dispatch receipt, got %+v", data)
	}
	msg, _ := data["message"].(string)
	if !strings.Contains(msg, "未经紧邻上一代校验") {
		t.Fatalf("old-agent receipt must disclose the unverified generation, got %q", msg)
	}
	if msg == "OK" {
		t.Fatalf("old-agent receipt must not forward the bare agent message, got %q", msg)
	}
	if _, ok := data["expectedVersion"]; ok {
		t.Fatalf("old-agent receipt must not invent a target version, got %+v", data)
	}
	if _, ok := data["toVersion"]; ok {
		t.Fatalf("old-agent receipt must not invent a landing version, got %+v", data)
	}
}
