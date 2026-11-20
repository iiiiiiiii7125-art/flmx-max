package socket

// R18-F1-01 (D-3): agent side of the panel-issued tunnel TLS scheme.
//
// The panel is the trust root: it owns one per-install CA and issues one
// certificate + private key per node (SAN: node-<id>.flmx.internal).
// Distribution rides the existing AES-encrypted panel→agent control
// channel as two new commands:
//
//   - TunnelTLSIssue: panel sends {caCertPem, nodeCertPem,
//     nodePrivateKeyPem, nodeId, serverName}; the agent verifies the
//     material (CA parses, key matches the certificate, certificate
//     chains to the CA for the expected server name, both still valid)
//     and only then persists it, atomically, at 0600 under <agent
//     workdir>/tls/. A verification failure stores nothing and the
//     command fails — the panel never marks the node ready, so the
//     tunnel render keeps the legacy (unverified) form and a running
//     chain is untouched.
//   - TunnelTLSStatus: read-only; reports whether the on-disk material
//     is present and still verifies, with SHA-256 fingerprints, so the
//     panel can re-confirm readiness before it renders verified TLS
//     and can re-issue when the files are gone or stale.
//
// The rendered gost config references these files by relative path
// (tls/ca.pem, tls/node-cert.pem, tls/node-key.pem): the agent runs
// with WorkingDirectory=<install dir> and gost loads the files
// relative to the same process CWD, so the paths hold across install
// layouts (systemd /etc/flmx_agent, legacy dirs, containers).

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tunnelTLSRelDir is the agent-workdir-relative directory holding the
// panel-issued tunnel TLS material.
const tunnelTLSRelDir = "tls"

const (
	tunnelTLSCACertFileName   = "ca.pem"
	tunnelTLSNodeCertFileName = "node-cert.pem"
	tunnelTLSNodeKeyFileName  = "node-key.pem"
)

// tunnelTLSBaseDir returns the agent working directory that tunnel TLS
// material is stored under. It is a variable (same seam as
// uninstallWorkingDir) so tests can redirect storage to a temp dir.
var tunnelTLSBaseDir = os.Getwd

// tunnelTLSIssueRequest is the TunnelTLSIssue command payload. The
// private key field is named nodePrivateKeyPem on purpose: the command
// log redactor masks values under keys matching private_?key, so the
// key never lands in agent logs (see redactCommandForLog).
type tunnelTLSIssueRequest struct {
	NodeID            int64  `json:"nodeId"`
	ServerName        string `json:"serverName"`
	CACertPEM         string `json:"caCertPem"`
	NodeCertPEM       string `json:"nodeCertPem"`
	NodePrivateKeyPEM string `json:"nodePrivateKeyPem"`
}

// tunnelTLSIssueResponse is the TunnelTLSIssue ack: fingerprints of the
// material the agent actually stored and verified.
type tunnelTLSIssueResponse struct {
	Ready      bool   `json:"ready"`
	ServerName string `json:"serverName"`
	CASha256   string `json:"caSha256"`
	CertSha256 string `json:"certSha256"`
}

// tunnelTLSStatusResponse is the TunnelTLSStatus reply.
type tunnelTLSStatusResponse struct {
	Ready      bool   `json:"ready"`
	ServerName string `json:"serverName,omitempty"`
	CASha256   string `json:"caSha256,omitempty"`
	CertSha256 string `json:"certSha256,omitempty"`
	Error      string `json:"error,omitempty"`
}

func tunnelTLSPaths(baseDir string) (ca, cert, key string) {
	dir := filepath.Join(baseDir, tunnelTLSRelDir)
	return filepath.Join(dir, tunnelTLSCACertFileName),
		filepath.Join(dir, tunnelTLSNodeCertFileName),
		filepath.Join(dir, tunnelTLSNodeKeyFileName)
}

// tunnelTLSPathsForRender returns the agent-relative paths the panel
// renders into gost tunnel configs (tls/ca.pem etc.).
func tunnelTLSPathsForRender() (ca, cert, key string) {
	return filepath.Join(tunnelTLSRelDir, tunnelTLSCACertFileName),
		filepath.Join(tunnelTLSRelDir, tunnelTLSNodeCertFileName),
		filepath.Join(tunnelTLSRelDir, tunnelTLSNodeKeyFileName)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// verifyTunnelTLSMaterial checks panel-issued material before it is
// trusted or stored: the CA must parse as a CA certificate, the node
// certificate must parse and match its private key, chain to the CA
// for the expected server name, and both must still be valid (with a
// 1h margin). It returns the SHA-256 fingerprints of the CA and node
// certificates. Nothing is written here.
func verifyTunnelTLSMaterial(caPEM, certPEM, keyPEM []byte, serverName string) (caSHA, certSHA string, err error) {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return "", "", errors.New("tunnel TLS: serverName 为空")
	}
	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil || caBlock.Type != "CERTIFICATE" {
		return "", "", errors.New("tunnel TLS: CA 证书 PEM 无法解析")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("tunnel TLS: CA 证书解析失败: %w", err)
	}
	if !caCert.IsCA {
		return "", "", errors.New("tunnel TLS: CA 证书不是 CA 证书")
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return "", "", errors.New("tunnel TLS: 节点证书 PEM 无法解析")
	}
	nodeCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("tunnel TLS: 节点证书解析失败: %w", err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return "", "", fmt.Errorf("tunnel TLS: 节点证书与私钥不匹配: %w", err)
	}
	now := time.Now()
	if now.Add(time.Hour).After(caCert.NotAfter) {
		return "", "", errors.New("tunnel TLS: CA 证书已过期或即将过期")
	}
	if now.Add(time.Hour).After(nodeCert.NotAfter) {
		return "", "", errors.New("tunnel TLS: 节点证书已过期或即将过期")
	}
	found := false
	for _, dns := range nodeCert.DNSNames {
		if strings.EqualFold(strings.TrimSpace(dns), serverName) {
			found = true
			break
		}
	}
	if !found {
		return "", "", fmt.Errorf("tunnel TLS: 节点证书 SAN 不含期望的节点身份 %q", serverName)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := nodeCert.Verify(x509.VerifyOptions{
		Roots:       pool,
		DNSName:     serverName,
		CurrentTime: now,
	}); err != nil {
		return "", "", fmt.Errorf("tunnel TLS: 节点证书无法由该 CA 验签: %w", err)
	}
	return sha256Hex(caBlock.Bytes), sha256Hex(certBlock.Bytes), nil
}

// storeTunnelTLSFile atomically publishes one PEM file at 0600: write
// to a temp file in the same directory, chmod, rename. A crash leaves
// either the old complete file or the new complete file, never a
// half-written one (same pattern as the panel's jwt_secret write).
func storeTunnelTLSFile(dir, name string, pemBytes []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("tunnel TLS: 创建目录 %s: %w", dir, err)
	}
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pemBytes, 0o600); err != nil {
		return fmt.Errorf("tunnel TLS: 写入 %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tunnel TLS: 设置 %s 权限: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tunnel TLS: 发布 %s: %w", path, err)
	}
	return nil
}

// issueTunnelTLSCerts verifies panel-issued material and persists it
// under baseDir. Verification runs first and storage is all-or-nothing
// in effect: a failure at any step returns an error and the panel must
// not mark the node ready (stale complete files from an earlier issue
// are left untouched — overwriting them with unverified material would
// be worse than keeping the last good set).
func issueTunnelTLSCerts(baseDir string, req tunnelTLSIssueRequest) (tunnelTLSIssueResponse, error) {
	if req.NodeID <= 0 {
		return tunnelTLSIssueResponse{}, errors.New("tunnel TLS: nodeId 非法")
	}
	caSHA, certSHA, err := verifyTunnelTLSMaterial(
		[]byte(req.CACertPEM), []byte(req.NodeCertPEM), []byte(req.NodePrivateKeyPEM), req.ServerName)
	if err != nil {
		return tunnelTLSIssueResponse{}, err
	}
	dir := filepath.Join(baseDir, tunnelTLSRelDir)
	if err := storeTunnelTLSFile(dir, tunnelTLSCACertFileName, []byte(req.CACertPEM)); err != nil {
		return tunnelTLSIssueResponse{}, err
	}
	if err := storeTunnelTLSFile(dir, tunnelTLSNodeCertFileName, []byte(req.NodeCertPEM)); err != nil {
		return tunnelTLSIssueResponse{}, err
	}
	if err := storeTunnelTLSFile(dir, tunnelTLSNodeKeyFileName, []byte(req.NodePrivateKeyPEM)); err != nil {
		return tunnelTLSIssueResponse{}, err
	}
	// Reload-verify from disk: the files the agent will actually load
	// must parse and verify, not just the bytes the panel sent.
	if _, err := tunnelTLSStatusFromDir(baseDir); err != nil {
		return tunnelTLSIssueResponse{}, fmt.Errorf("tunnel TLS: 落盘后复验失败: %w", err)
	}
	return tunnelTLSIssueResponse{
		Ready:      true,
		ServerName: strings.TrimSpace(req.ServerName),
		CASha256:   caSHA,
		CertSha256: certSHA,
	}, nil
}

// tunnelTLSStatusFromDir loads the on-disk material and verifies it.
// It returns the status (ready=false with an error string when
// anything is missing or fails verification); the error return is only
// for unexpected I/O failures distinct from "not ready".
func tunnelTLSStatusFromDir(baseDir string) (tunnelTLSStatusResponse, error) {
	caPath, certPath, keyPath := tunnelTLSPaths(baseDir)
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return tunnelTLSStatusResponse{Ready: false, Error: "CA 证书缺失"}, nil
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return tunnelTLSStatusResponse{Ready: false, Error: "节点证书缺失"}, nil
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return tunnelTLSStatusResponse{Ready: false, Error: "节点私钥缺失"}, nil
	}
	// The stored server name is recovered from the certificate SAN:
	// the panel renders serverName node-<id>.flmx.internal and the
	// certificate carries exactly that DNS SAN.
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return tunnelTLSStatusResponse{Ready: false, Error: "节点证书 PEM 无法解析"}, nil
	}
	nodeCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return tunnelTLSStatusResponse{Ready: false, Error: "节点证书解析失败"}, nil
	}
	serverName := ""
	for _, dns := range nodeCert.DNSNames {
		if strings.HasPrefix(dns, "node-") && strings.HasSuffix(dns, ".flmx.internal") {
			serverName = dns
			break
		}
	}
	if serverName == "" {
		return tunnelTLSStatusResponse{Ready: false, Error: "节点证书 SAN 不含节点身份"}, nil
	}
	caSHA, certSHA, err := verifyTunnelTLSMaterial(caPEM, certPEM, keyPEM, serverName)
	if err != nil {
		return tunnelTLSStatusResponse{Ready: false, Error: err.Error()}, nil
	}
	return tunnelTLSStatusResponse{
		Ready:      true,
		ServerName: serverName,
		CASha256:   caSHA,
		CertSha256: certSHA,
	}, nil
}

// handleTunnelTLSIssue handles the TunnelTLSIssue panel command.
func handleTunnelTLSIssue(data interface{}) (interface{}, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("序列化数据失败: %v", err)
	}
	var req tunnelTLSIssueRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("解析隧道 TLS 下发请求失败: %v", err)
	}
	baseDir, err := tunnelTLSBaseDir()
	if err != nil {
		return nil, fmt.Errorf("取 agent 工作目录失败: %v", err)
	}
	return issueTunnelTLSCerts(baseDir, req)
}

// handleTunnelTLSStatus handles the TunnelTLSStatus panel command
// (read-only: never writes, never mutates readiness by itself).
func handleTunnelTLSStatus(data interface{}) (interface{}, error) {
	baseDir, err := tunnelTLSBaseDir()
	if err != nil {
		return nil, fmt.Errorf("取 agent 工作目录失败: %v", err)
	}
	return tunnelTLSStatusFromDir(baseDir)
}
