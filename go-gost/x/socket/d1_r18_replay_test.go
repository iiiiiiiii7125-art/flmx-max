package socket

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/crypto"
	"github.com/gorilla/websocket"
)

// R18-D1-01: the encrypted command envelope's timestamp was
// unauthenticated plaintext outside the AES-GCM payload, so a
// captured envelope could be replayed forever by rewriting the
// outer timestamp once its requestId claim expired. The
// authoritative timestamp now lives INSIDE the payload; the agent
// rejects envelopes without one (fail-closed) and rejects a stale
// inner timestamp no matter what the outer one says. A replayed
// UpgradeAgent envelope also can no longer downgrade the node:
// the agent enforces the upgrade direction itself, with the
// panel's calibre (unknown current version does not block,
// uncomparable fails closed).

// d1R18Envelope builds an encrypted envelope the way an attacker
// holding a captured one would present it: the inner timestamp is
// whatever the captured payload carries, the outer timestamp is
// freely rewritten. includeInner=false builds the pre-fix shape
// (no timestamp inside the payload at all).
func d1R18Envelope(t *testing.T, cmd CommandMessage, innerTS time.Time, includeInner bool, outerTS time.Time) []byte {
	t.Helper()
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	if includeInner {
		obj["timestamp"] = innerTS.UnixMilli()
	}
	raw, err = json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	aes, err := crypto.NewAESCrypto(d1FixTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := aes.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(map[string]interface{}{
		"encrypted": true,
		"data":      encrypted,
		"timestamp": outerTS.UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

// d1R18ExpireClaim backdates a requestId claim past expiry, the way
// the 10-minute claim window expiring does in production.
func d1R18ExpireClaim(t *testing.T, reporter *WebSocketReporter, id string) {
	t.Helper()
	if reporter.seenCommands == nil {
		t.Fatal("claim set must exist after a first delivery")
	}
	reporter.seenCommands.mu.Lock()
	defer reporter.seenCommands.mu.Unlock()
	reporter.seenCommands.claims[id] = time.Now().Add(-time.Second)
}

func TestD1R18RewrittenOuterTimestampReplayRejected(t *testing.T) {
	reporter, panel := newD1TestReporter(t)
	now := time.Now()

	// A fresh envelope executes once and claims its requestId.
	reporter.handleReceivedMessage(websocket.TextMessage,
		d1R18Envelope(t, CommandMessage{Type: "TuneStatus", RequestId: "r18-replay-1"}, now, true, now))
	resp := d1ReadResponse(t, panel)
	if resp.Type != "TuneStatusResponse" || !resp.Success {
		t.Fatalf("first delivery must execute, got %+v", resp)
	}

	// The claim window expires (10 minutes in production).
	d1R18ExpireClaim(t, reporter, "r18-replay-1")

	// Attacker replays the captured envelope 11 minutes later with
	// the outer timestamp rewritten to now. The inner timestamp is
	// still the captured one and is stale — the rewrite must not
	// revive the envelope.
	staleInner := now.Add(-(commandReplayWindow + time.Minute))
	reporter.handleReceivedMessage(websocket.TextMessage,
		d1R18Envelope(t, CommandMessage{Type: "TuneStatus", RequestId: "r18-replay-1"}, staleInner, true, now))
	resp = d1ReadResponse(t, panel)
	if resp.Type != "StaleTimestamp" || resp.Success {
		t.Fatalf("replay with rewritten outer timestamp must be rejected as stale, got %+v", resp)
	}
}

func TestD1R18EnvelopeWithoutInnerTimestampRejected(t *testing.T) {
	reporter, panel := newD1TestReporter(t)
	now := time.Now()

	// Pre-fix envelope shape: fresh outer timestamp, no timestamp
	// inside the payload. Fail-closed: the agent must refuse it
	// outright instead of executing on the outer timestamp's word.
	reporter.handleReceivedMessage(websocket.TextMessage,
		d1R18Envelope(t, CommandMessage{Type: "TuneStatus", RequestId: "r18-no-inner-1"}, now, false, now))
	resp := d1ReadResponse(t, panel)
	if resp.Type != "MissingInnerTimestamp" || resp.Success {
		t.Fatalf("envelope without an inner timestamp must be rejected, got %+v", resp)
	}
}

// R18-R2-D1-03: the two post-decrypt rejections above used to carry
// an empty requestId, so the panel's tryResolvePending dropped the
// frame and the caller waited out the full command timeout (up to
// 300s for upgrades) before seeing a generic timeout. Decryption
// already succeeded at that point, so the requestId is right there
// in the decrypted payload — the rejection must carry it, and the
// caller must be woken at once with the named reason.
func TestD1R18InnerRejectionCarriesRequestID(t *testing.T) {
	reporter, panel := newD1TestReporter(t)
	now := time.Now()

	// MissingInnerTimestamp: no inner timestamp at all.
	reporter.handleReceivedMessage(websocket.TextMessage,
		d1R18Envelope(t, CommandMessage{Type: "TuneStatus", RequestId: "r18-no-inner-2"}, now, false, now))
	resp := d1ReadResponse(t, panel)
	if resp.Type != "MissingInnerTimestamp" || resp.Success {
		t.Fatalf("envelope without an inner timestamp must be rejected, got %+v", resp)
	}
	if resp.RequestId != "r18-no-inner-2" {
		t.Fatalf("MissingInnerTimestamp rejection must carry the requestId, got %+v", resp)
	}

	// StaleTimestamp (inner): stale inner timestamp, fresh outer one.
	staleInner := now.Add(-(commandReplayWindow + time.Minute))
	reporter.handleReceivedMessage(websocket.TextMessage,
		d1R18Envelope(t, CommandMessage{Type: "TuneStatus", RequestId: "r18-stale-inner-1"}, staleInner, true, now))
	resp = d1ReadResponse(t, panel)
	if resp.Type != "StaleTimestamp" || resp.Success {
		t.Fatalf("envelope with a stale inner timestamp must be rejected, got %+v", resp)
	}
	if resp.RequestId != "r18-stale-inner-1" {
		t.Fatalf("StaleTimestamp rejection must carry the requestId, got %+v", resp)
	}
}
