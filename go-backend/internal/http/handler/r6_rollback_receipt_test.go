package handler

import (
	"strings"
	"testing"

	"go-backend/internal/ws"
)

// R6-05 receipt gates: the node rollback receipt must name the
// landing version whenever the agent reports it. The cross-path
// generation skip stayed invisible precisely because the old
// receipt forwarded only the agent's bare "OK".

func TestRollbackReceiptNamesLandingVersion(t *testing.T) {
	data := rollbackReceiptData(ws.CommandResult{
		Success: true,
		Message: "OK",
		Data: map[string]interface{}{
			"fromVersion":      "3.0.0",
			"toVersion":        "1.0.0",
			"backupGeneration": "previous",
		},
	})
	if data["toVersion"] != "1.0.0" || data["fromVersion"] != "3.0.0" {
		t.Fatalf("receipt must carry both versions, got %+v", data)
	}
	if data["backupGeneration"] != "previous" {
		t.Fatalf("generation must pass through, got %+v", data)
	}
	msg, _ := data["message"].(string)
	if !strings.Contains(msg, "1.0.0") {
		t.Fatalf("receipt message must name the landing version, got %q", msg)
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
	msg, _ := data["message"].(string)
	if !strings.Contains(msg, "未经紧邻上一代校验") {
		t.Fatalf("old-agent receipt must disclose the unverified generation, got %q", msg)
	}
	if msg == "OK" {
		t.Fatalf("old-agent receipt must not forward the bare agent message, got %q", msg)
	}
	if _, ok := data["toVersion"]; ok {
		t.Fatalf("old-agent receipt must not invent a landing version, got %+v", data)
	}
}
