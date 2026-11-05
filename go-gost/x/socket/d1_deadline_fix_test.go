package socket

import (
	"context"
	"strings"
	"testing"
	"time"
)

// D1-06: read-path commands ran on the process-level context with no
// per-command deadline. When the panel timed out and the operator (or
// an automatic retry) re-sent the command, the first execution was
// still running agent-side and the retry executed a second time —
// Kernel/Tune side effects doubled. The read path now derives a
// per-command budget context (panel TimeoutMS when supplied, a named
// default otherwise); a command whose budget is already exhausted is
// refused instead of executed, and the budget is threaded into the
// Kernel/Tune handlers and the tune probe's network legs.

func TestD1ExpiredBudgetReadCommandRefused(t *testing.T) {
	reporter, panel := newD1TestReporter(t)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	reporter.routeCommand(ctx, CommandMessage{Type: "TuneStatus", RequestId: "expired-1"})

	// routeCommand answers synchronously on the read path's behalf via
	// sendResponse; read the receipt off the wire.
	panel.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp := d1ReadResponse(t, panel)
	if resp.Type != "TuneStatusResponse" || resp.Success {
		t.Fatalf("expired-budget read command must be refused, got %+v", resp)
	}
	if !strings.Contains(resp.Message, "预算已耗尽") {
		t.Fatalf("refusal must name the exhausted budget, got %q", resp.Message)
	}
	if resp.RequestId != "expired-1" {
		t.Fatalf("refusal must echo the requestId, got %q", resp.RequestId)
	}
}

func TestD1ReadCommandBudgetShape(t *testing.T) {
	if got := readCommandBudget(0); got != defaultReadCommandBudget {
		t.Fatalf("no panel budget must fall back to the named default, got %v", got)
	}
	if got := readCommandBudget(-5); got != defaultReadCommandBudget {
		t.Fatalf("negative panel budget must fall back to the named default, got %v", got)
	}
	if got := readCommandBudget(10_000); got != 10*time.Second-readCommandReplyMargin {
		t.Fatalf("panel budget must carry the reply margin, got %v", got)
	}
	if got := readCommandBudget(1); got != minReadCommandBudget {
		t.Fatalf("tiny panel budget must clamp to the minimum, got %v", got)
	}
	if got := readCommandBudget((24 * time.Hour).Milliseconds()); got != maxReadCommandBudget {
		t.Fatalf("absurd panel budget must clamp to the maximum, got %v", got)
	}
	// Both handler families refuse an already-spent budget at entry,
	// before touching the kernel manager or any sysctl state.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := handleTuneCommandContext(expired, "TuneStatus", nil); err == nil || !strings.Contains(err.Error(), "预算已耗尽") {
		t.Fatalf("tune handler must refuse a spent budget, got %v", err)
	}
	if _, err := handleKernelCommandContext(expired, "KernelStatus", nil); err == nil || !strings.Contains(err.Error(), "预算已耗尽") {
		t.Fatalf("kernel handler must refuse a spent budget, got %v", err)
	}
}

func TestD1LiveBudgetReadCommandExecutes(t *testing.T) {
	reporter, panel := newD1TestReporter(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	reporter.routeCommand(ctx, CommandMessage{Type: "TuneStatus", RequestId: "live-1"})

	panel.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp := d1ReadResponse(t, panel)
	if resp.Type != "TuneStatusResponse" || !resp.Success {
		t.Fatalf("live-budget read command must execute, got %+v", resp)
	}
}
