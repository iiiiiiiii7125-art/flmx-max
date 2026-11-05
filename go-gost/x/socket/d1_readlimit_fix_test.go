package socket

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// D1-03: the agent side of the command channel had no inbound frame
// limit at all (gorilla's default 0 = unlimited), while the panel caps
// its side at 10MB. One oversized frame from a compromised or broken
// sender could OOM the root agent on small nodes. connect() now applies
// the same 10MB read limit; an over-limit frame costs the connection
// (which the reconnect loop re-establishes), never the process.

func TestD1InboundFrameReadLimit(t *testing.T) {
	connections := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(rw, req, nil)
		if err != nil {
			return
		}
		connections <- conn
	}))
	t.Cleanup(server.Close)

	reporter := NewWebSocketReporter("", d1FixTestSecret)
	reporter.addr = "ws://" + strings.TrimPrefix(server.URL, "http://")
	reporter.secret = d1FixTestSecret
	reporter.version = "d1-readlimit-test"
	if err := reporter.connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(reporter.Stop)
	go reporter.receiveMessages()

	panelConn := <-connections
	t.Cleanup(func() { panelConn.Close() })

	// A large-but-legal frame (below the 10MB cap) is read and answered
	// (a ParseError receipt for this garbage payload) and the connection
	// stays up.
	legal := bytes.Repeat([]byte("a"), 9<<20)
	if err := panelConn.WriteMessage(websocket.TextMessage, legal); err != nil {
		t.Fatalf("write legal frame: %v", err)
	}
	panelConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := panelConn.ReadMessage(); err != nil {
		t.Fatalf("legal frame must be answered, connection alive: %v", err)
	}

	// An over-limit frame must terminate the read loop: the agent drops
	// the connection instead of buffering the frame. The write runs in
	// a goroutine: once the agent stops draining (read limit tripped),
	// a blocking write of an 11MB frame can never complete — which is
	// precisely the sender-side symptom of the connection being dropped.
	oversized := bytes.Repeat([]byte("b"), (10<<20)+1)
	go func() { _ = panelConn.WriteMessage(websocket.TextMessage, oversized) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		reporter.connMutex.Lock()
		connected := reporter.connected
		reporter.connMutex.Unlock()
		if !connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("oversized frame did not drop the connection: no inbound read limit is in effect")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
