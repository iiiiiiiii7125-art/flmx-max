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

// TestTunnelTLSAgentPathsMatchContract pins the agent-relative render
// paths; the agent resolves the same relative paths under its
// workdir (go-gost/x/socket/tunnel_tls.go tunnelTLSPathsForRender).
func TestTunnelTLSAgentPathsMatchContract(t *testing.T) {
	ca, cert, key := tunnelTLSAgentPaths()
	if ca != "tls/ca.pem" || cert != "tls/node-cert.pem" || key != "tls/node-key.pem" {
		t.Fatalf("agent paths = %q/%q/%q, want tls/ca.pem/tls/node-cert.pem/tls/node-key.pem", ca, cert, key)
	}
}
