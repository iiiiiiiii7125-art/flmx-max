package handler

import (
	"fmt"
	"testing"
)

// F2-03 (HTTP face, mirroring live-d scenario 5): the same legacy
// (no-seq) batch POSTed twice verbatim must book only once. Both
// posts return "ok" — the replay is acknowledged so the agent stops
// retrying — but the second one is recognised by its content hash in
// the accounting transaction and not booked again.
func TestFlowUploadLegacyVerbatimReplayBookedOnceF203(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	body := []byte(fmt.Sprintf(`[{"N":"%d_1_0","U":70,"D":50}]`, fwdID))

	if got := f01PostFlowUpload(t, h, f01TestSecret, body, nil); got != "ok" {
		t.Fatalf("first legacy post must return ok, got %q", got)
	}
	if got := f01PostFlowUpload(t, h, f01TestSecret, body, nil); got != "ok" {
		t.Fatalf("replayed legacy post must return ok (ack, no rebook), got %q", got)
	}
	if in, out := c101ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
		t.Fatalf("F2-03: verbatim replay double-booked: in=%d out=%d, want 50/70", in, out)
	}

	// A genuinely different batch from the same node still books.
	body2 := []byte(fmt.Sprintf(`[{"N":"%d_1_0","U":30,"D":20}]`, fwdID))
	if got := f01PostFlowUpload(t, h, f01TestSecret, body2, nil); got != "ok" {
		t.Fatalf("distinct legacy post must return ok, got %q", got)
	}
	if in, out := c101ForwardFlow(t, h, fwdID); in != 70 || out != 100 {
		t.Fatalf("F2-03: distinct batch not booked: in=%d out=%d, want 70/100", in, out)
	}
}
