package handler

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	repo "go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// fakeTunnelTLSSender is the R18-F1-01 test seam for the panel→agent
// AES channel: it pretends to be an agent that stores whatever the
// panel issues (computing fingerprints the way the real agent does),
// with knobs for the failure modes.
type fakeTunnelTLSSender struct {
	issueCalls  int
	statusCalls int

	issueErr        error
	issueAckWrongFP bool
	statusErr       error
	statusReady     bool
	statusCA        string
	statusCert      string

	lastIssueData map[string]interface{}
}

func (f *fakeTunnelTLSSender) send(nodeID int64, cmdType string, data interface{}, timeout time.Duration) (ws.CommandResult, error) {
	switch cmdType {
	case ws.TunnelTLSIssueCommand:
		f.issueCalls++
		if f.issueErr != nil {
			return ws.CommandResult{}, f.issueErr
		}
		m, _ := data.(map[string]interface{})
		f.lastIssueData = m
		caSHA := sha256OfCertPEM(m["caCertPem"])
		certSHA := sha256OfCertPEM(m["nodeCertPem"])
		if f.issueAckWrongFP {
			caSHA, certSHA = "wrong-ca", "wrong-cert"
		}
		return ws.CommandResult{Success: true, Data: map[string]interface{}{
			"ready": true, "serverName": m["serverName"], "caSha256": caSHA, "certSha256": certSHA,
		}}, nil
	case ws.TunnelTLSStatusCommand:
		f.statusCalls++
		if f.statusErr != nil {
			return ws.CommandResult{}, f.statusErr
		}
		return ws.CommandResult{Success: true, Data: map[string]interface{}{
			"ready": f.statusReady, "caSha256": f.statusCA, "certSha256": f.statusCert,
		}}, nil
	default:
		return ws.CommandResult{}, errors.New("unknown command")
	}
}

func sha256OfCertPEM(v interface{}) string {
	s, _ := v.(string)
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

func openTunnelTLSHandler(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/tunnel-tls.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "unit-test-secret")
}

// TestIssueNodeTunnelTLSCertVerifies: the panel-issued node
// certificate must verify against the panel CA for the node server
// name, with a matching key.
func TestIssueNodeTunnelTLSCertVerifies(t *testing.T) {
	h := openTunnelTLSHandler(t)
	_, certPEM, keyPEM, caSHA, certSHA, serverName, err := h.issueNodeTunnelTLSCert(7)
	if err != nil {
		t.Fatalf("issueNodeTunnelTLSCert: %v", err)
	}
	if serverName != "node-7.flmx.internal" {
		t.Fatalf("serverName = %q, want node-7.flmx.internal", serverName)
	}
	ca, err := h.tunnelTLSAuthority()
	if err != nil {
		t.Fatalf("tunnelTLSAuthority: %v", err)
	}
	if caSHA != ca.CASha256() {
		t.Fatalf("caSHA = %q, want CA fingerprint %q", caSHA, ca.CASha256())
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("node cert PEM does not decode")
	}
	nodeCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse node cert: %v", err)
	}
	sum := sha256.Sum256(block.Bytes)
	if certSHA != hex.EncodeToString(sum[:]) {
		t.Fatalf("certSHA = %q, want sha256 of issued cert", certSHA)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	if _, err := nodeCert.Verify(x509.VerifyOptions{Roots: pool, DNSName: serverName, CurrentTime: time.Now()}); err != nil {
		t.Fatalf("issued node cert must verify against the panel CA: %v", err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("issued node key must match the certificate: %v", err)
	}
}

// TestDistributeNodeTunnelTLSMarksReadyOnAck: a fingerprint-matching
// ack marks the node Ready in the repo.
func TestDistributeNodeTunnelTLSMarksReadyOnAck(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{}
	h.tunnelTLSCommandSender = fake.send
	if err := h.distributeNodeTunnelTLS(7); err != nil {
		t.Fatalf("distributeNodeTunnelTLS: %v", err)
	}
	st, err := h.repo.GetNodeTunnelTLS(7)
	if err != nil || st == nil || st.Ready != 1 {
		t.Fatalf("after distribute: state=%+v err=%v, want ready", st, err)
	}
	if st.ServerName != "node-7.flmx.internal" {
		t.Fatalf("stored serverName = %q", st.ServerName)
	}
	if got := sha256OfCertPEM(fake.lastIssueData["caCertPem"]); got != st.CASha256 {
		t.Fatalf("stored CA fingerprint = %q, want %q", st.CASha256, got)
	}
}

// TestDistributeNodeTunnelTLSLeavesNotReadyOnAckMismatch: an ack whose
// fingerprints do not match what was sent must NOT mark ready
// (fail-closed on the panel side too).
func TestDistributeNodeTunnelTLSLeavesNotReadyOnAckMismatch(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{issueAckWrongFP: true}
	h.tunnelTLSCommandSender = fake.send
	if err := h.distributeNodeTunnelTLS(7); err == nil {
		t.Fatal("fingerprint-mismatched ack must fail distribution")
	}
	if st, _ := h.repo.GetNodeTunnelTLS(7); st != nil && st.Ready == 1 {
		t.Fatalf("mismatched ack marked node ready: %+v", st)
	}
}

// TestDistributeNodeTunnelTLSLeavesNotReadyOnSendError: an offline
// node (or old agent) leaves the node not-ready — deploy keeps the
// legacy rendering.
func TestDistributeNodeTunnelTLSLeavesNotReadyOnSendError(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{issueErr: errFakeTunnelTLSNodeOffline}
	h.tunnelTLSCommandSender = fake.send
	if err := h.distributeNodeTunnelTLS(7); err == nil {
		t.Fatal("send error must fail distribution")
	}
	if st, _ := h.repo.GetNodeTunnelTLS(7); st != nil && st.Ready == 1 {
		t.Fatalf("failed distribution marked node ready: %+v", st)
	}
}

var errFakeTunnelTLSNodeOffline = errors.New("节点不在线")

// TestEnsureNodeTunnelTLSReadyReissuesWhenStatusDisagrees: a node
// marked Ready whose agent no longer holds the material (status
// ready=false) is re-issued, not trusted.
func TestEnsureNodeTunnelTLSReadyReissuesWhenStatusDisagrees(t *testing.T) {
	h := openTunnelTLSHandler(t)
	_, _, _, caSHA, certSHA, serverName, err := h.issueNodeTunnelTLSCert(7)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := h.repo.MarkNodeTunnelTLSReady(7, caSHA, certSHA, serverName, time.Now().Unix()); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	fake := &fakeTunnelTLSSender{statusReady: false}
	h.tunnelTLSCommandSender = fake.send
	if !h.ensureNodeTunnelTLSReady(7) {
		t.Fatal("ensure must re-issue and return true when the agent disagrees")
	}
	if fake.statusCalls != 1 || fake.issueCalls != 1 {
		t.Fatalf("statusCalls=%d issueCalls=%d, want 1/1 (status check then re-issue)", fake.statusCalls, fake.issueCalls)
	}
}

// TestEnsureNodeTunnelTLSReadyKeepsReadyWhenStatusAgrees: a node
// marked Ready whose agent confirms matching fingerprints is trusted
// without a re-issue.
func TestEnsureNodeTunnelTLSReadyKeepsReadyWhenStatusAgrees(t *testing.T) {
	h := openTunnelTLSHandler(t)
	_, _, _, caSHA, certSHA, serverName, err := h.issueNodeTunnelTLSCert(7)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := h.repo.MarkNodeTunnelTLSReady(7, caSHA, certSHA, serverName, time.Now().Unix()); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	fake := &fakeTunnelTLSSender{statusReady: true, statusCA: caSHA, statusCert: certSHA}
	h.tunnelTLSCommandSender = fake.send
	if !h.ensureNodeTunnelTLSReady(7) {
		t.Fatal("ensure must return true when status agrees")
	}
	if fake.issueCalls != 0 {
		t.Fatalf("issueCalls=%d, want 0 (no re-issue when status agrees)", fake.issueCalls)
	}
}

// TestEnsureNodeTunnelTLSReadyOfflineStaysLegacy: an offline node
// never becomes ready and ensure returns false (deploy keeps legacy).
func TestEnsureNodeTunnelTLSReadyOfflineStaysLegacy(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{issueErr: errFakeTunnelTLSNodeOffline, statusErr: errFakeTunnelTLSNodeOffline}
	h.tunnelTLSCommandSender = fake.send
	if h.ensureNodeTunnelTLSReady(7) {
		t.Fatal("ensure must return false for an offline node")
	}
	if st, _ := h.repo.GetNodeTunnelTLS(7); st != nil && st.Ready == 1 {
		t.Fatalf("offline node marked ready: %+v", st)
	}
}

// dialerTLSBlockOf extracts the "tls" block of one dialer leg from a
// chain config (nil when the leg renders legacy, i.e. no "tls" key).
func dialerTLSBlockOf(t *testing.T, chainCfg map[string]interface{}, idx int) map[string]interface{} {
	t.Helper()
	hops, ok := chainCfg["hops"].([]map[string]interface{})
	if !ok || len(hops) == 0 {
		t.Fatalf("chain has no hops: %#v", chainCfg)
	}
	nodes, ok := hops[0]["nodes"].([]map[string]interface{})
	if !ok || idx < 0 || idx >= len(nodes) {
		t.Fatalf("chain has no node %d: %#v", idx, chainCfg)
	}
	dialer, _ := nodes[idx]["dialer"].(map[string]interface{})
	if dialer == nil {
		t.Fatalf("node %d has no dialer: %#v", idx, nodes[idx])
	}
	tls, _ := dialer["tls"].(map[string]interface{})
	return tls
}

// listenerTLSBlockOf extracts the "tls" block of a chain service
// listener (nil when the listener renders legacy).
func listenerTLSBlockOf(t *testing.T, services []map[string]interface{}) map[string]interface{} {
	t.Helper()
	if len(services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(services))
	}
	listener, _ := services[0]["listener"].(map[string]interface{})
	if listener == nil {
		t.Fatalf("service has no listener: %#v", services[0])
	}
	tls, _ := listener["tls"].(map[string]interface{})
	return tls
}

// TestTunnelTLSDialerFlipsOnlyWhenWholeDialerGroupReady (R18-F1-01
// C3): a dialer leg renders verified TLS only when the target
// listener will flip too — i.e. every node dialing that listener is
// ready, not just the two endpoints. A verified dialer pointed at a
// still-legacy listener fails the handshake (random self-signed peer
// cert), so both-endpoints-ready alone must keep the leg legacy.
func TestTunnelTLSDialerFlipsOnlyWhenWholeDialerGroupReady(t *testing.T) {
	h := &Handler{jwtSecret: "unit-test-secret"}
	nodes := map[int64]*nodeRecord{
		1: {ID: 1, TCPListenAddr: "192.0.2.1"},
		2: {ID: 2, TCPListenAddr: "192.0.2.2"},
		3: {ID: 3, TCPListenAddr: "192.0.2.3"},
	}
	targets := []tunnelRuntimeNode{{NodeID: 2, Protocol: "tls", Port: 24001, ConnectIP: "192.0.2.2"}}
	ready := map[int64]bool{1: true, 2: true}

	cfg, err := h.buildTunnelChainConfig(99, 1, targets, nodes, "", ready, []int64{1, 2}, "tls")
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}
	tlsBlock := dialerTLSBlockOf(t, cfg, 0)
	if tlsBlock == nil {
		t.Fatal("dialer must render verified TLS when the whole dialer group is ready")
	}
	if tlsBlock["secure"] != true || tlsBlock["caFile"] != "tls/ca.pem" ||
		tlsBlock["certFile"] != "tls/node-cert.pem" || tlsBlock["keyFile"] != "tls/node-key.pem" ||
		tlsBlock["serverName"] != "node-2.flmx.internal" {
		t.Fatalf("dialer tls block = %v, want secure/caFile/certFile/keyFile/serverName for node 2", tlsBlock)
	}

	// Same endpoints ready, but a sibling dialer (node 3) of the same
	// listener is not ready: the listener stays legacy, so the dialer
	// must stay legacy too.
	cfg, err = h.buildTunnelChainConfig(99, 1, targets, nodes, "", ready, []int64{1, 2, 3}, "tls")
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}
	if got := dialerTLSBlockOf(t, cfg, 0); got != nil {
		t.Fatalf("dialer must stay legacy when a sibling dialer is not ready, got tls=%v", got)
	}

	// Unknown dialer group never flips the dialer.
	cfg, err = h.buildTunnelChainConfig(99, 1, targets, nodes, "", ready, nil, "tls")
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}
	if got := dialerTLSBlockOf(t, cfg, 0); got != nil {
		t.Fatalf("dialer must stay legacy when the dialer group is unknown, got tls=%v", got)
	}

	// Non-TLS-family segments never get a tls block, even fully ready.
	tcpTargets := []tunnelRuntimeNode{{NodeID: 2, Protocol: "tcp", Port: 24001, ConnectIP: "192.0.2.2"}}
	cfg, err = h.buildTunnelChainConfig(99, 1, tcpTargets, nodes, "", ready, []int64{1, 2}, "tcp")
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}
	if got := dialerTLSBlockOf(t, cfg, 0); got != nil {
		t.Fatalf("tcp dialer must never carry a tls block, got tls=%v", got)
	}
}

// TestTunnelTLSListenerFlipsOnlyWhenAllDialersReady (R18-F1-01 C3):
// a listener renders verified TLS only when it AND every node that
// dials it are ready — a verified listener requires client
// certificates, so one legacy dialer would be rejected and its leg
// would break.
func TestTunnelTLSListenerFlipsOnlyWhenAllDialersReady(t *testing.T) {
	h := &Handler{jwtSecret: "unit-test-secret"}
	node := &nodeRecord{ID: 2, TCPListenAddr: "192.0.2.2", UDPListenAddr: "192.0.2.2"}
	chainNode := tunnelRuntimeNode{NodeID: 2, Protocol: "tls", Port: 24001, ChainType: 2}

	svcs := h.buildTunnelChainServiceConfig(99, chainNode, node, 1, map[int64]bool{1: true, 2: true, 3: true}, []int64{1, 3}, "tls")
	tlsBlock := listenerTLSBlockOf(t, svcs)
	if tlsBlock == nil {
		t.Fatal("listener must render verified TLS when it and all its dialers are ready")
	}
	if tlsBlock["certFile"] != "tls/node-cert.pem" || tlsBlock["keyFile"] != "tls/node-key.pem" || tlsBlock["caFile"] != "tls/ca.pem" {
		t.Fatalf("listener tls block = %v, want certFile/keyFile/caFile", tlsBlock)
	}

	svcs = h.buildTunnelChainServiceConfig(99, chainNode, node, 1, map[int64]bool{1: true, 2: true}, []int64{1, 3}, "tls")
	if got := listenerTLSBlockOf(t, svcs); got != nil {
		t.Fatalf("listener must stay legacy when one dialer is not ready, got tls=%v", got)
	}

	svcs = h.buildTunnelChainServiceConfig(99, chainNode, node, 1, map[int64]bool{1: true, 2: true, 3: true}, nil, "tls")
	if got := listenerTLSBlockOf(t, svcs); got != nil {
		t.Fatalf("listener must stay legacy when its dialer set is unknown, got tls=%v", got)
	}
}

// TestTunnelTLSSegmentEndsFlipTogether (R18-F1-01 C3): the two ends
// of one leg flip as a unit. With a sibling dialer not ready, the
// dialer AND the listener both stay legacy; with all ready, both
// render verified TLS. A mixed state in either direction breaks the
// leg (verified dialer vs self-signed listener, or legacy dialer vs
// client-cert-requiring listener).
func TestTunnelTLSSegmentEndsFlipTogether(t *testing.T) {
	h := &Handler{jwtSecret: "unit-test-secret"}
	nodes := map[int64]*nodeRecord{
		1: {ID: 1, TCPListenAddr: "192.0.2.1"},
		2: {ID: 2, TCPListenAddr: "192.0.2.2"},
		3: {ID: 3, TCPListenAddr: "192.0.2.3"},
	}
	targets := []tunnelRuntimeNode{{NodeID: 2, Protocol: "tls", Port: 24001, ConnectIP: "192.0.2.2"}}
	listenerNode := nodes[2]
	chainNode := tunnelRuntimeNode{NodeID: 2, Protocol: "tls", Port: 24001, ChainType: 2}

	partial := map[int64]bool{1: true, 2: true}
	cfg, err := h.buildTunnelChainConfig(99, 1, targets, nodes, "", partial, []int64{1, 2, 3}, "tls")
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}
	svcs := h.buildTunnelChainServiceConfig(99, chainNode, listenerNode, 1, partial, []int64{1, 3}, "tls")
	if got := dialerTLSBlockOf(t, cfg, 0); got != nil {
		t.Fatalf("partial ready: dialer must stay legacy, got tls=%v", got)
	}
	if got := listenerTLSBlockOf(t, svcs); got != nil {
		t.Fatalf("partial ready: listener must stay legacy, got tls=%v", got)
	}

	full := map[int64]bool{1: true, 2: true, 3: true}
	cfg, err = h.buildTunnelChainConfig(99, 1, targets, nodes, "", full, []int64{1, 2, 3}, "tls")
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}
	svcs = h.buildTunnelChainServiceConfig(99, chainNode, listenerNode, 1, full, []int64{1, 3}, "tls")
	if got := dialerTLSBlockOf(t, cfg, 0); got == nil {
		t.Fatal("full ready: dialer must render verified TLS")
	}
	if got := listenerTLSBlockOf(t, svcs); got == nil {
		t.Fatal("full ready: listener must render verified TLS")
	}
}

// TestTunnelTLSReadyForStateFailsClosedAfterPriorIssue (R18-F1-01
// C3): a node that was issued certificates before (its
// node_tunnel_tls row exists) but still has none after the re-issue
// attempt refuses the deploy — the chain is never silently
// downgraded to unverified rendering.
func TestTunnelTLSReadyForStateFailsClosedAfterPriorIssue(t *testing.T) {
	h := openTunnelTLSHandler(t)
	_, _, _, caSHA, certSHA, serverName, err := h.issueNodeTunnelTLSCert(7)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := h.repo.MarkNodeTunnelTLSReady(7, caSHA, certSHA, serverName, time.Now().Unix()); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	fake := &fakeTunnelTLSSender{issueErr: errFakeTunnelTLSNodeOffline, statusErr: errFakeTunnelTLSNodeOffline}
	h.tunnelTLSCommandSender = fake.send
	state := &tunnelCreateState{
		TunnelID: 99, Type: 2, TunnelProtocol: "tls",
		InNodes:  []tunnelRuntimeNode{{NodeID: 7}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 8}},
	}
	ready, err := h.tunnelTLSReadyForState(state)
	if err == nil {
		t.Fatalf("previously-issued node without a certificate must fail the deploy closed, got ready=%v", ready)
	}
	if len(ready) != 0 {
		t.Fatalf("fail-closed deploy must return an empty ready set, got %v", ready)
	}
}

// TestTunnelTLSReadyForStateNeverIssuedOfflineStaysLegacy (R18-F1-01
// C3): nodes that were never issued certificates keep the legacy
// rendering when they cannot be reached — the deploy proceeds and
// the running chain is untouched.
func TestTunnelTLSReadyForStateNeverIssuedOfflineStaysLegacy(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{issueErr: errFakeTunnelTLSNodeOffline, statusErr: errFakeTunnelTLSNodeOffline}
	h.tunnelTLSCommandSender = fake.send
	state := &tunnelCreateState{
		TunnelID: 99, Type: 2, TunnelProtocol: "tls",
		InNodes:  []tunnelRuntimeNode{{NodeID: 7}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 8}},
	}
	ready, err := h.tunnelTLSReadyForState(state)
	if err != nil {
		t.Fatalf("never-issued offline nodes must keep legacy rendering, got error: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready = %v, want empty", ready)
	}
}

// TestTunnelTLSReadyForStateReturnsReadySet (R18-F1-01 C3):
// reachable never-issued nodes are distributed to on deploy and
// come back in the ready set.
func TestTunnelTLSReadyForStateReturnsReadySet(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{}
	h.tunnelTLSCommandSender = fake.send
	state := &tunnelCreateState{
		TunnelID: 99, Type: 2, TunnelProtocol: "tls",
		InNodes:  []tunnelRuntimeNode{{NodeID: 7}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 8}},
	}
	ready, err := h.tunnelTLSReadyForState(state)
	if err != nil {
		t.Fatalf("tunnelTLSReadyForState: %v", err)
	}
	if !ready[7] || !ready[8] {
		t.Fatalf("ready = %v, want both nodes ready", ready)
	}
}

// TestTunnelTLSReadyForStateSkipsNonTLSTunnels (R18-F1-01 C3):
// tunnels with no TLS-family segment send no certificate commands.
func TestTunnelTLSReadyForStateSkipsNonTLSTunnels(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{}
	h.tunnelTLSCommandSender = fake.send
	state := &tunnelCreateState{
		TunnelID: 99, Type: 2, TunnelProtocol: "tcp",
		InNodes:  []tunnelRuntimeNode{{NodeID: 7}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 8}},
	}
	ready, err := h.tunnelTLSReadyForState(state)
	if err != nil {
		t.Fatalf("non-TLS tunnel must not fail: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready = %v, want empty", ready)
	}
	if fake.issueCalls != 0 || fake.statusCalls != 0 {
		t.Fatalf("issueCalls=%d statusCalls=%d, want 0/0 (no commands for non-TLS tunnels)", fake.issueCalls, fake.statusCalls)
	}
}

// TestTunnelTLSAgentPathsMatchContract pins the agent-relative render
// paths; the agent resolves the same relative paths under its
// workdir (go-gost/x/socket/tunnel_tls.go tunnelTLSPathsForRender).
func TestTunnelTLSAgentPathsMatchContract(t *testing.T) {
	ca, cert, key := tunnelTLSAgentPaths()
	if ca != "tls/ca.pem" || cert != "tls/node-cert.pem" || key != "tls/node-key.pem" {
		t.Fatalf("agent paths = %q/%q/%q, want tls/ca.pem/tls/node-cert.pem/tls/node-key.pem", ca, cert, key)
	}
}
