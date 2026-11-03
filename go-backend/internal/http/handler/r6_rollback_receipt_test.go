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

func TestRollbackReceiptOldAgentPassthrough(t *testing.T) {
	data := rollbackReceiptData(ws.CommandResult{Success: true, Message: "OK"})
	if len(data) != 1 || data["message"] != "OK" {
		t.Fatalf("agent without pairing data must keep the previous receipt shape, got %+v", data)
	}
}
