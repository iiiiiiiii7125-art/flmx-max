package handler

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go-backend/internal/config"
	"go-backend/internal/ws"
)

// R18-F1-01 (D-3): panel side of the panel-issued tunnel TLS scheme.
//
// Flow:
//  1. CA: one per-install CA, loaded (or created once) from the panel
//     data dir by config.LoadOrCreateTunnelTLSCA (cached on the
//     Handler, never silently regenerated).
//  2. Issue: the panel signs a per-node certificate (SAN
//     node-<id>.flmx.internal) from the CA.
//  3. Distribute: the certificate material rides the existing AES
//     control channel as a TunnelTLSIssue command; the agent verifies
//     it and persists it at 0600, then acks with fingerprints. Only
//     a fingerprint-matching ack marks the node Ready in
//     node_tunnel_tls.
//  4. Ensure (migration gate input): before a tunnel deploy renders
//     verified TLS, ensureNodeTunnelTLSReady confirms the agent still
//     holds the issued material (TunnelTLSStatus) and re-issues when
//     it does not. Any failure here returns false — the render keeps
//     the legacy (unverified) form and the running chain is never
//     touched. Old agents answer "unknown command" and therefore
//     stay not-ready (legacy) until upgraded.
//
// The agent-relative render paths (tls/ca.pem etc.) must match the
// agent's tunnelTLSPathsForRender (go-gost/x/socket/tunnel_tls.go);
// the two modules do not share code, so the contract is pinned by
// the render tests in audit_r18_f1_01_d3_test.go and recorded in
// audit-out/FIX-R18-F1-01-D3.md.

// tunnelTLSAgentPaths returns the agent-relative paths the panel
// renders into gost tunnel configs.
func tunnelTLSAgentPaths() (ca, cert, key string) {
	return filepath.Join("tls", "ca.pem"),
		filepath.Join("tls", "node-cert.pem"),
		filepath.Join("tls", "node-key.pem")
}

// tunnelTLSDataDir returns the panel data directory holding the tunnel
// CA files: <DBPath 同级> for SQLite, DB_PATH's directory (or
// /app/data) for PostgreSQL — the same two branches as
// repo.defaultInitialAdminDataDir.
func (h *Handler) tunnelTLSDataDir() string {
	if h != nil && h.repo != nil {
		if p := strings.TrimSpace(h.repo.DBPath()); p != "" {
			return filepath.Dir(p)
		}
	}
	if p := strings.TrimSpace(os.Getenv("DB_PATH")); p != "" {
		return filepath.Dir(p)
	}
	return "/app/data"
}

// tunnelTLSAuthority returns the cached per-install tunnel CA,
// loading (or creating it once) on first use. A load failure is not
// cached: the next call retries, so a transient disk error does not
// wedge distribution forever.
func (h *Handler) tunnelTLSAuthority() (*config.TunnelTLSCA, error) {
	if h == nil {
		return nil, fmt.Errorf("handler 未初始化")
	}
	h.tunnelTLSMu.Lock()
	defer h.tunnelTLSMu.Unlock()
	if h.tunnelTLSCA != nil {
		return h.tunnelTLSCA, nil
	}
	ca, err := config.LoadOrCreateTunnelTLSCA(h.tunnelTLSDataDir())
	if err != nil {
		return nil, err
	}
	h.tunnelTLSCA = ca
	return ca, nil
}

// issueNodeTunnelTLSCert signs a per-node certificate from the panel
// CA. It is pure (no network, no DB write): the caller decides when
// to distribute it. It returns the PEM material plus the CA and node
// certificate fingerprints and the node server name.
func (h *Handler) issueNodeTunnelTLSCert(nodeID int64) (caPEM, certPEM, keyPEM []byte, caSHA, certSHA, serverName string, err error) {
	ca, err := h.tunnelTLSAuthority()
	if err != nil {
		return nil, nil, nil, "", "", "", err
	}
	certPEM, keyPEM, certSHA, err = ca.IssueNodeCertificate(nodeID)
	if err != nil {
		return nil, nil, nil, "", "", "", err
	}
	return ca.CertPEM, certPEM, keyPEM, ca.CASha256(), certSHA, config.TunnelTLSNodeServerName(nodeID), nil
}

// sendTunnelTLSCommand sends one tunnel-TLS command to a node. Tests
// inject tunnelTLSCommandSender; production uses sendNodeCommand
// (default 6s timeout, AES channel).
func (h *Handler) sendTunnelTLSCommand(nodeID int64, cmdType string, data interface{}) (ws.CommandResult, error) {
	if h.tunnelTLSCommandSender != nil {
		return h.tunnelTLSCommandSender(nodeID, cmdType, data, defaultNodeCommandTimeout)
	}
	return h.sendNodeCommand(nodeID, cmdType, data, false, false)
}

// distributeNodeTunnelTLS issues a node certificate and pushes it to
// the node. It marks the node Ready only after the agent acks with
// fingerprints matching exactly what the panel sent. Any failure
// (CA unavailable, node offline, old agent, agent-side verification
// failure, fingerprint mismatch) returns an error and leaves the
// node not-ready — the caller renders legacy and the running chain
// is unaffected.
func (h *Handler) distributeNodeTunnelTLS(nodeID int64) error {
	if h == nil || h.repo == nil {
		return fmt.Errorf("handler 未初始化")
	}
	if nodeID <= 0 {
		return fmt.Errorf("节点 ID 非法")
	}
	caPEM, certPEM, keyPEM, caSHA, certSHA, serverName, err := h.issueNodeTunnelTLSCert(nodeID)
	if err != nil {
		return fmt.Errorf("签发节点 %d 的隧道证书失败: %w", nodeID, err)
	}
	result, err := h.sendTunnelTLSCommand(nodeID, ws.TunnelTLSIssueCommand, map[string]interface{}{
		"nodeId":            nodeID,
		"serverName":        serverName,
		"caCertPem":         string(caPEM),
		"nodeCertPem":       string(certPEM),
		"nodePrivateKeyPem": string(keyPEM),
	})
	if err != nil {
		return fmt.Errorf("下发节点 %d 的隧道证书失败: %w", nodeID, err)
	}
	if !result.Success {
		return fmt.Errorf("节点 %d 拒绝隧道证书下发: %s", nodeID, result.Message)
	}
	ackCA, _ := result.Data["caSha256"].(string)
	ackCert, _ := result.Data["certSha256"].(string)
	if ackCA != caSHA || ackCert != certSHA {
		return fmt.Errorf("节点 %d 的隧道证书回执指纹不匹配（下发 %s/%s，回执 %s/%s），拒绝标记就绪", nodeID, caSHA, certSHA, ackCA, ackCert)
	}
	if err := h.repo.MarkNodeTunnelTLSReady(nodeID, caSHA, certSHA, serverName, time.Now().Unix()); err != nil {
		return fmt.Errorf("记录节点 %d 的隧道证书就绪态失败: %w", nodeID, err)
	}
	return nil
}

// checkNodeTunnelTLSStatus asks the agent whether the on-disk tunnel
// material is present and still verifies, and whether its
// fingerprints match the panel's record. It returns false on any
// failure (offline node, old agent, missing/expired/mismatched
// material) — the caller treats that as "re-distribute".
func (h *Handler) checkNodeTunnelTLSStatus(nodeID int64) (ready bool, caSHA, certSHA string) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return false, "", ""
	}
	result, err := h.sendTunnelTLSCommand(nodeID, ws.TunnelTLSStatusCommand, map[string]interface{}{
		"nodeId": nodeID,
	})
	if err != nil || !result.Success {
		return false, "", ""
	}
	ok, _ := result.Data["ready"].(bool)
	if !ok {
		return false, "", ""
	}
	caSHA, _ = result.Data["caSha256"].(string)
	certSHA, _ = result.Data["certSha256"].(string)
	return true, caSHA, certSHA
}

// ensureNodeTunnelTLSReady makes a best effort to get a node to the
// TLS-ready state before a tunnel deploy renders verified TLS. It
// never fails a deploy: every failure path logs and returns false,
// and the render falls back to the legacy form for the affected
// segments. A node already marked Ready is re-confirmed against the
// agent (status + fingerprints) before it is trusted; a mismatch
// triggers a re-issue.
func (h *Handler) ensureNodeTunnelTLSReady(nodeID int64) bool {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return false
	}
	if st, err := h.repo.GetNodeTunnelTLS(nodeID); err == nil && st != nil && st.Ready == 1 {
		if ok, caSHA, certSHA := h.checkNodeTunnelTLSStatus(nodeID); ok && caSHA == st.CASha256 && certSHA == st.CertSha256 {
			return true
		}
		log.Printf("tunnel-tls: 节点 %d 已标记就绪但 agent 回查不符（缺失/过期/指纹不匹配），重新下发", nodeID)
	}
	if err := h.distributeNodeTunnelTLS(nodeID); err != nil {
		log.Printf("tunnel-tls: 节点 %d 证书下发失败，保持旧渲染（链路不受影响）: %v", nodeID, err)
		return false
	}
	return true
}

// tunnelTLSReadyMap reads the ready set for a batch of nodes in one
// query. On a DB error it returns an empty map (fail-open to legacy
// rendering) and logs — a DB blip must never flip a segment to
// verified, and must never break a deploy.
func (h *Handler) tunnelTLSReadyMap(nodeIDs []int64) map[int64]bool {
	if h == nil || h.repo == nil || len(nodeIDs) == 0 {
		return map[int64]bool{}
	}
	ready, err := h.repo.ListNodeTunnelTLSReady(nodeIDs)
	if err != nil {
		log.Printf("tunnel-tls: 读取节点证书就绪态失败，保持旧渲染: %v", err)
		return map[int64]bool{}
	}
	return ready
}
