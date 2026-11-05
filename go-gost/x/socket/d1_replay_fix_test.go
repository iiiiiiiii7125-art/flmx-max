package socket

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/crypto"
	"github.com/gorilla/websocket"
)

// D1-02: an encrypted command envelope is only bound by its ±5 minute
// timestamp window; without a seen-requestId set the identical envelope
// can be replayed verbatim inside the window and executes again. The
// reporter now claims each requestId once and rejects replays with an
// explicit failure receipt.

func d1EncryptEnvelope(t *testing.T, cmd CommandMessage) []byte {
	t.Helper()
	raw, err := json.Marshal(cmd)
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
		"timestamp": time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestD1EncryptedEnvelopeReplayRejected(t *testing.T) {
	reporter, panel := newD1TestReporter(t)

	envelope := d1EncryptEnvelope(t, CommandMessage{Type: "TuneStatus", RequestId: "replay-1"})
	reporter.handleReceivedMessage(websocket.TextMessage, envelope)
	resp := d1ReadResponse(t, panel)
	if resp.Type != "TuneStatusResponse" || !resp.Success {
		t.Fatalf("first delivery must execute, got %+v", resp)
	}

	// Byte-identical replay inside the timestamp window: the timestamp
	// check alone accepts it, so only the requestId claim can stop it.
	reporter.handleReceivedMessage(websocket.TextMessage, envelope)
	resp = d1ReadResponse(t, panel)
	if resp.Type != "TuneStatusResponse" || resp.Success {
		t.Fatalf("replayed envelope must be rejected, got %+v", resp)
	}
	if !strings.Contains(resp.Message, "重放") {
		t.Fatalf("replay rejection must name the cause, got %q", resp.Message)
	}
	if resp.RequestId != "replay-1" {
		t.Fatalf("rejection must echo the requestId, got %q", resp.RequestId)
	}

	// A fresh requestId in a fresh envelope still executes.
	reporter.handleReceivedMessage(websocket.TextMessage, d1EncryptEnvelope(t, CommandMessage{
		Type: "TuneStatus", RequestId: "replay-2",
	}))
	resp = d1ReadResponse(t, panel)
	if resp.Type != "TuneStatusResponse" || !resp.Success {
		t.Fatalf("fresh envelope must execute, got %+v", resp)
	}
}

func TestD1SeenCommandIDsClaimSemantics(t *testing.T) {
	set := newSeenCommandIDs()
	now := time.Now()
	if !set.claim("a", now) {
		t.Fatal("first claim must be fresh")
	}
	if set.claim("a", now.Add(time.Minute)) {
		t.Fatal("duplicate claim inside the window must be rejected")
	}
	if !set.claim("a", now.Add(commandReplayWindow+time.Second)) {
		t.Fatal("claim after the window must be fresh again (the envelope itself would be stale by then)")
	}
	if !set.claim("", now) || !set.claim("", now) {
		t.Fatal("empty requestIds cannot be deduped and must always pass")
	}
	// The set stays bounded under a flood of distinct ids.
	for i := 0; i < maxSeenCommandIDs+1024; i++ {
		set.claim(fmt.Sprintf("flood-%d", i), now)
	}
	if size := set.size(); size > maxSeenCommandIDs {
		t.Fatalf("claim set must stay bounded at %d, got %d", maxSeenCommandIDs, size)
	}
}
