package handler

import (
	"strings"
	"testing"

	"go-backend/internal/kernel"
)

// R8-005: Xray below 26.5.9 has no freedom defaultBlockPrivateRule,
// so private destinations pass silently. The fix names the risk at
// install time through the envelope warning — deliberately NOT a
// kernelVersionGate hard gate, which would subsume the ENC 25.9.5
// floor and strip every pre-26.5.9 node of all dispatch ability.
// These tests pin both halves: the warning fires exactly for
// explicit pre-26.5.9 Xray versions, and the hard gate stays silent
// for a plain inbound on those versions.
func TestR8XrayPrivateBlockWarning(t *testing.T) {
	for _, v := range []string{"25.9.5", "26.4.17", "v26.4.17", "24.12.31"} {
		msg := xrayPrivateBlockWarning(kernel.TypeXray, v)
		if msg == "" || !strings.Contains(msg, "26.5.9") || !strings.Contains(msg, "私网拦截") {
			t.Fatalf("Xray %s must carry the private-block warning naming 26.5.9, got %q", v, msg)
		}
	}
	for _, v := range []string{"26.5.9", "26.7.11", "27.1.1", "latest", "garbage"} {
		if msg := xrayPrivateBlockWarning(kernel.TypeXray, v); msg != "" {
			t.Fatalf("Xray %s must not warn, got %q", v, msg)
		}
	}
	if msg := xrayPrivateBlockWarning(kernel.TypeSingbox, "1.11.0"); msg != "" {
		t.Fatalf("sing-box must not warn, got %q", msg)
	}
	// The hard gate must stay feature-conditional: a plain Xray
	// inbound on 25.9.5 (the ENC floor) still passes it.
	in := &kernel.Inbound{Protocol: kernel.ProtocolVLESS}
	if msg := kernelVersionGate(kernel.TypeXray, "25.9.5", in); msg != "" {
		t.Fatalf("plain inbound on Xray 25.9.5 must pass the hard gate, got %q", msg)
	}
}
