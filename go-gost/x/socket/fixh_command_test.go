package socket

import (
	"strings"
	"testing"
)

// D1-01 (agent half): the full command body used to be printed to stdout
// (journal), including credential fields of service configs. The log line
// must identify the command without leaking secret material.
func TestRouteCommandLogRedactsSecrets(t *testing.T) {
	reporter := NewWebSocketReporter("ws://127.0.0.1:1", "node-secret")

	out := captureStdout(t, func() {
		reporter.routeCommand(CommandMessage{
			Type:      "BogusCommandForTest",
			RequestId: "req-1",
			Data: map[string]interface{}{
				"name":     "svc",
				"password": "TOPSECRET-PASSWORD-VALUE",
				"secret":   "TOPSECRET-SECRET-VALUE",
				"auth":     "TOPSECRET-AUTH-VALUE",
			},
		})
	})

	for _, leaked := range []string{"TOPSECRET-PASSWORD-VALUE", "TOPSECRET-SECRET-VALUE", "TOPSECRET-AUTH-VALUE"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("command log leaked %q:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "BogusCommandForTest") {
		t.Fatalf("command log should still identify the command type:\n%s", out)
	}
}

// D1-02: accepting plaintext (unencrypted) commands must produce a loud,
// one-time security warning instead of proceeding silently.
func TestPlaintextCommandWarningOnlyOnce(t *testing.T) {
	reporter := NewWebSocketReporter("ws://127.0.0.1:1", "node-secret")

	out := captureStdout(t, func() {
		reporter.warnPlaintextCommandOnce()
		reporter.warnPlaintextCommandOnce()
		reporter.warnPlaintextCommandOnce()
	})

	if n := strings.Count(out, "安全警告"); n != 1 {
		t.Fatalf("expected exactly one plaintext warning, got %d:\n%s", n, out)
	}
}
