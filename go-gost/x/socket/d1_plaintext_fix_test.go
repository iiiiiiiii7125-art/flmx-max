package socket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/crypto"
	"github.com/gorilla/websocket"
)

// D1-01: a reporter configured with a node secret must refuse plaintext
// (unencrypted-envelope) commands outright. Every panel of this product
// line encrypts whenever the node secret is non-empty (the AES envelope
// exists on both ends since v1.0.0, and NewAESCrypto only fails on an
// empty secret), so a plaintext command frame can only come from an
// in-path injector or a misconfiguration — never from a real panel.
// Plaintext acceptance remains only for the no-secret local form.

const d1FixTestSecret = "d1-fix-test-secret"

func newD1TestReporter(t *testing.T) (*WebSocketReporter, *websocket.Conn) {
	t.Helper()
	connections := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	panel := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(rw, req, nil)
		if err != nil {
			return
		}
		connections <- conn
	}))
	t.Cleanup(panel.Close)
	agentConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(panel.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	panelConn := <-connections
	t.Cleanup(func() { panelConn.Close() })
	reporter := NewWebSocketReporter("", d1FixTestSecret)
	reporter.conn, reporter.connected = agentConn, true
	t.Cleanup(reporter.Stop)
	panelConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return reporter, panelConn
}

func d1ReadResponse(t *testing.T, panel *websocket.Conn) CommandResponse {
	t.Helper()
	_, raw, err := panel.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Encrypted bool   `json:"encrypted"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Encrypted {
		aes, err := crypto.NewAESCrypto(d1FixTestSecret)
		if err != nil {
			t.Fatal(err)
		}
		raw, err = aes.Decrypt(envelope.Data)
		if err != nil {
			t.Fatal(err)
		}
	}
	var resp CommandResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func d1PlaintextFrame(t *testing.T, cmd CommandMessage) []byte {
	t.Helper()
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestD1PlaintextCommandsRejectedWhenSecretConfigured(t *testing.T) {
	reporter, panel := newD1TestReporter(t)

	// Read-only command: TuneStatus would succeed if it were executed,
	// so a failure carrying the plaintext-rejection marker proves the
	// gate fired before dispatch.
	reporter.handleReceivedMessage(websocket.TextMessage, d1PlaintextFrame(t, CommandMessage{
		Type: "TuneStatus", RequestId: "plain-read-1",
	}))
	resp := d1ReadResponse(t, panel)
	if resp.Type != "TuneStatusResponse" || resp.Success {
		t.Fatalf("plaintext read command must be rejected, got %+v", resp)
	}
	if !strings.Contains(resp.Message, "明文") {
		t.Fatalf("rejection must name the plaintext cause, got %q", resp.Message)
	}
	if resp.RequestId != "plain-read-1" {
		t.Fatalf("rejection must echo the requestId, got %q", resp.RequestId)
	}

	// Mutation command: UpgradeAgent plaintext must never reach the
	// upgrade machinery.
	reporter.handleReceivedMessage(websocket.TextMessage, d1PlaintextFrame(t, CommandMessage{
		Type: "UpgradeAgent", RequestId: "plain-mut-1",
		Data: map[string]interface{}{"version": "1.9.9"},
	}))
	resp = d1ReadResponse(t, panel)
	if resp.Type != "UpgradeAgentResponse" || resp.Success {
		t.Fatalf("plaintext UpgradeAgent must be rejected, got %+v", resp)
	}
	if !strings.Contains(resp.Message, "明文") {
		t.Fatalf("rejection must name the plaintext cause, got %q", resp.Message)
	}

	// TcpPing plaintext must not even enter the probe queue.
	reporter.handleReceivedMessage(websocket.TextMessage, d1PlaintextFrame(t, CommandMessage{
		Type: "TcpPing", RequestId: "plain-ping-1", TimeoutMS: 5000,
		Data: TcpPingRequest{IP: "127.0.0.1", Port: 1, Count: 1, Timeout: 100},
	}))
	resp = d1ReadResponse(t, panel)
	if resp.Type != "TcpPingResponse" || resp.Success || !strings.Contains(resp.Message, "明文") {
		t.Fatalf("plaintext TcpPing must be rejected, got %+v", resp)
	}
	if n := len(reporter.tcpPingQueue); n != 0 {
		t.Fatalf("plaintext TcpPing entered the probe queue (%d tasks)", n)
	}
}

func TestD1PlaintextStillAcceptedWithoutSecret(t *testing.T) {
	// No-secret local form: the reporter has no AES encryptor, so the
	// plaintext gate must stay open (dispatch proceeds as before).
	reporter := NewWebSocketReporter("", "")
	t.Cleanup(reporter.Stop)
	if reporter.aesCrypto != nil {
		t.Fatal("empty secret must leave aesCrypto nil for this control case")
	}
	reporter.handleReceivedMessage(websocket.TextMessage, d1PlaintextFrame(t, CommandMessage{
		Type: "TcpPing", RequestId: "nosecret-ping-1", TimeoutMS: 5000,
		Data: TcpPingRequest{IP: "127.0.0.1", Port: 1, Count: 1, Timeout: 100},
	}))
	if n := len(reporter.tcpPingQueue); n != 1 {
		t.Fatalf("plaintext TcpPing without a configured secret must be dispatched, queue=%d", n)
	}
}
