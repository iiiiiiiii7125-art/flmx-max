package socket

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// R2-X2-02: pre-parse error receipts carry the command's requestId
// whenever it is recoverable at the failure point, so the panel's
// tryResolvePending can deliver the agent's stated failure reason to
// the waiting caller instead of a bare timeout. Envelope-level
// failures (stale timestamp / decrypt) cannot know the requestId —
// it sits inside the undecrypted envelope — and keep the empty form;
// the panel covers those by logging the five types' messages.

func TestPreParseErrorReceiptCarriesRequestID(t *testing.T) {
	t.Run("decompress failure echoes the compressed frame requestId", func(t *testing.T) {
		reporter, panel := newD1TestReporter(t)
		frame := []byte(`{"type":"TuneStatus","compressed":true,"data":"not-a-gzip-stream","requestId":"req-decompress-1"}`)
		reporter.handleReceivedMessage(websocket.TextMessage, frame)
		resp := d1ReadResponse(t, panel)
		if resp.Type != "DecompressError" || resp.Success {
			t.Fatalf("expected DecompressError failure receipt, got %+v", resp)
		}
		if resp.RequestId != "req-decompress-1" {
			t.Fatalf("receipt must echo the frame requestId, got %q", resp.RequestId)
		}
	})

	t.Run("plain parse failure best-effort echoes the frame requestId", func(t *testing.T) {
		reporter, panel := newD1TestReporter(t)
		// type as a number breaks the CommandMessage parse while the
		// top-level requestId field stays readable.
		frame := []byte(`{"type":123,"requestId":"req-parse-1","data":{}}`)
		reporter.handleReceivedMessage(websocket.TextMessage, frame)
		resp := d1ReadResponse(t, panel)
		if resp.Type != "ParseError" || resp.Success {
			t.Fatalf("expected ParseError failure receipt, got %+v", resp)
		}
		if resp.RequestId != "req-parse-1" {
			t.Fatalf("receipt must echo the best-effort requestId, got %q", resp.RequestId)
		}
	})

	t.Run("stale envelope has no recoverable requestId", func(t *testing.T) {
		reporter, panel := newD1TestReporter(t)
		envelope, err := json.Marshal(map[string]interface{}{
			"encrypted": true,
			"data":      "irrelevant-never-decrypted",
			"timestamp": time.Now().Add(-time.Hour).UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}
		reporter.handleReceivedMessage(websocket.TextMessage, envelope)
		resp := d1ReadResponse(t, panel)
		if resp.Type != "StaleTimestamp" || resp.Success {
			t.Fatalf("expected StaleTimestamp failure receipt, got %+v", resp)
		}
		if resp.RequestId != "" {
			t.Fatalf("envelope-level receipt cannot carry a requestId, got %q", resp.RequestId)
		}
	})
}

// R2-X2-03: the legacy "call" frame type lost its silent-drop branch
// (and the zero-caller handleCall is deleted): an encrypted "call"
// frame now travels the standard gates and lands on routeCommand's
// fallback, producing an explicit UnknownCommandResponse receipt
// instead of silence.
func TestCallFrameGetsExplicitUnknownReceipt(t *testing.T) {
	reporter, panel := newD1TestReporter(t)
	reporter.handleReceivedMessage(websocket.TextMessage,
		d1EncryptEnvelope(t, CommandMessage{Type: "call", RequestId: "call-1"}))
	resp := d1ReadResponse(t, panel)
	if resp.Type != "UnknownCommandResponse" || resp.Success {
		t.Fatalf("call frame must get an explicit unknown-command receipt, got %+v", resp)
	}
	if resp.RequestId != "call-1" {
		t.Fatalf("receipt must echo the requestId, got %q", resp.RequestId)
	}
}
