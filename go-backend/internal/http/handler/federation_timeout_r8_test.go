package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R8-T-06 full-budget probes (x-verdicts-r8 §7 batch T3 regression
// requirement): a peer that legitimately uses MORE than the caller's
// command budget — but inside the fixed margin the client now adds —
// must still deliver its success to the caller. Before the fix the
// federation HTTP client timeout was constructed as exactly the command
// budget, so a full-budget peer item plus HTTP traversal overhead made
// the caller record failure while the peer succeeded.
func r8FederationPeerStub(t *testing.T, sleep time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/federation/runtime/command", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(sleep)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"","data":{"type":"TcpPing","success":true,"message":"ok","data":{}}}`))
	})
	mux.HandleFunc("/api/v1/federation/runtime/diagnose", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(sleep)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"","data":{"reachable":true}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func r8FederationHandler(t *testing.T) *Handler {
	t.Helper()
	// The stub peer is an httptest loopback server; like the other
	// federation tests (federation_consumer_cleanup_test.go), enable the
	// testing bypass for the outbound SSRF dial check — the subject of
	// these probes is client timeout patience, not address validation.
	DisableSafeRemoteAddrCheckForTesting = true
	t.Cleanup(func() { DisableSafeRemoteAddrCheckForTesting = false })
	r, err := repo.Open(filepath.Join(t.TempDir(), "r8-federation-timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	return New(r, "unit-test-secret")
}

// TestR8T06FederationClientTimeoutMargin pins the construction rule
// itself: for every threaded budget the client timeout is the budget
// plus the fixed margin — strictly greater than the peer's own work
// budget, which is the property whose absence made the chain
// zero-headroom by construction.
func TestR8T06FederationClientTimeoutMargin(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"default command budget", 6 * time.Second, 16 * time.Second},
		{"latency command budget", 22600 * time.Millisecond, 32600 * time.Millisecond},
		{"diagnosis command budget", 30 * time.Second, 40 * time.Second},
		{"kernel apply / upgrade budget", 300 * time.Second, 310 * time.Second},
		{"unthreaded falls back to default budget", 0, 16 * time.Second},
		{"negative falls back to default budget", -time.Second, 16 * time.Second},
	}
	for _, tc := range cases {
		got := federationClientTimeout(tc.in)
		if got != tc.want {
			t.Fatalf("%s: federationClientTimeout(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
		if tc.in > 0 && got <= tc.in {
			t.Fatalf("%s: client timeout %v must exceed the threaded budget %v", tc.name, got, tc.in)
		}
	}
	if federationRuntimeClientTimeout <= 10*time.Second {
		t.Fatalf("federationRuntimeClientTimeout = %v, must exceed the old 10s default client", federationRuntimeClientTimeout)
	}
}

func TestR8T06RemoteCommandPeerFullBudget(t *testing.T) {
	h := r8FederationHandler(t)
	const budget = 2 * time.Second
	// Peer answers at budget+3s: past the bare budget (old client
	// timeout), inside budget+margin (new client timeout).
	srv := r8FederationPeerStub(t, budget+3*time.Second)
	node := &nodeRecord{IsRemote: 1, RemoteURL: srv.URL, RemoteToken: "tok"}
	res, err := h.sendRemoteNodeCommandWithTimeout(node, "TcpPing", map[string]interface{}{"ip": "203.0.113.1"}, budget)
	if err != nil {
		t.Fatalf("remote command failed although the peer answered within budget+margin: %v", err)
	}
	if !res.Success {
		t.Fatalf("remote command result not successful: %+v", res)
	}
}

func TestR8T06RemoteDiagnosePeerFullBudget(t *testing.T) {
	h := r8FederationHandler(t)
	const budget = 2 * time.Second
	srv := r8FederationPeerStub(t, budget+3*time.Second)
	node := &nodeRecord{IsRemote: 1, RemoteURL: srv.URL, RemoteToken: "tok"}
	out, err := h.tcpPingViaRemoteNode(node, "203.0.113.1", 80, diagnosisExecOptions{
		commandTimeout: budget,
		pingTimeoutMS:  int(budget / time.Millisecond),
		pingCount:      4,
	})
	if err != nil {
		t.Fatalf("remote diagnose failed although the peer answered within budget+margin: %v", err)
	}
	if out == nil {
		t.Fatalf("remote diagnose returned no payload")
	}
}
