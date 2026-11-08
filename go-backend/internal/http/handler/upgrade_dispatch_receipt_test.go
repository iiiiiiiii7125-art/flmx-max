package handler

import (
	"strings"
	"testing"

	"go-backend/internal/ws"
)

// J6-F02: the node UPGRADE receipt must be the same dispatch form
// as the node ROLLBACK receipt (M-0051 doctrine). Before this fix
// the upgrade path answered {version, message: <agent's bare
// "OK">} — the target named as if it were the node's version,
// while the rollback path had already been corrected to
// {status:"dispatched", expectedVersion, message with verify-after-
// reconnect}. These tests pin both faces to the one shape so the
// two receipts cannot drift apart again.

func TestUpgradeAndRollbackReceiptsShareDispatchShape(t *testing.T) {
	upgrade := upgradeReceiptData("2.1.0", ws.CommandResult{Success: true, Message: "OK"})
	rollback := rollbackReceiptData(ws.CommandResult{
		Success: true,
		Message: "OK",
		Data: map[string]interface{}{
			"fromVersion":      "2.0.0",
			"toVersion":        "1.9.9",
			"backupGeneration": "previous",
		},
	})

	for name, data := range map[string]map[string]interface{}{"upgrade": upgrade, "rollback": rollback} {
		if data["status"] != "dispatched" {
			t.Fatalf("%s receipt must be a dispatch receipt, got %+v", name, data)
		}
		if _, ok := data["expectedVersion"]; !ok {
			t.Fatalf("%s receipt must name its target as expectedVersion, got %+v", name, data)
		}
		// No field may name a landed version: "version" (upgrade's
		// old bare target) and "toVersion" (rollback's pre-M-0051
		// field) are both landing-claim shapes.
		for _, landingField := range []string{"version", "toVersion"} {
			if _, ok := data[landingField]; ok {
				t.Fatalf("%s receipt must not carry landing-claim field %q, got %+v", name, landingField, data)
			}
		}
		msg, _ := data["message"].(string)
		if !strings.Contains(msg, "已下发") || !strings.Contains(msg, "核验") {
			t.Fatalf("%s receipt message must read as a dispatch with a verify-after-reconnect instruction, got %q", name, msg)
		}
		if msg == "OK" {
			t.Fatalf("%s receipt must not forward the bare agent message, got %q", name, msg)
		}
	}
}

func TestUpgradeReceiptNamesTargetAsExpected(t *testing.T) {
	data := upgradeReceiptData("2.1.0", ws.CommandResult{Success: true, Message: "OK"})
	if data["expectedVersion"] != "2.1.0" {
		t.Fatalf("upgrade receipt must name the target version, got %+v", data)
	}
	msg, _ := data["message"].(string)
	if !strings.Contains(msg, "2.1.0") {
		t.Fatalf("upgrade receipt message must name the target version, got %q", msg)
	}
	if !strings.Contains(msg, "落点待节点重连后在节点列表核验") {
		t.Fatalf("upgrade receipt message must state the landing is verified after reconnect, got %q", msg)
	}
}
