package handler

import (
	"testing"
)

// R2-F2-23: the agent's KernelStatus reply names a requested-but-
// absent kernel type via missingLogsFor; the panel must turn that
// into a distinguishable reply instead of a code-0 empty state.
// The live-agent HTTP leg needs a real node (see the c102 comment
// on TestC102KernelLogs); this pins the two halves that are
// testable in the sandbox — the wire field surviving remarshal, and
// the decision predicate itself.
func TestR2KernelLogsMissingReply(t *testing.T) {
	// Wire shape: an agent Status with no such kernel instance.
	var status agentKernelStatus
	if err := remarshal(map[string]interface{}{
		"kernels":        []interface{}{},
		"counters":       []interface{}{},
		"users":          []interface{}{},
		"missingLogsFor": "sing-box",
	}, &status); err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if status.MissingLogsFor != "sing-box" {
		t.Fatalf("MissingLogsFor = %q, want sing-box", status.MissingLogsFor)
	}
	if !kernelLogsMissingReply(status, "sing-box") {
		t.Fatal("missing reply must be recognized for the requested type")
	}
	if kernelLogsMissingReply(status, "xray") {
		t.Fatal("a different type must not be reported missing")
	}
	// No signal at all: present-but-no-logs stays on the old path.
	var empty agentKernelStatus
	if err := remarshal(map[string]interface{}{
		"kernels":  []interface{}{},
		"counters": []interface{}{},
		"users":    []interface{}{},
	}, &empty); err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if kernelLogsMissingReply(empty, "sing-box") {
		t.Fatal("without the signal the handler must keep the previous behavior")
	}
}
