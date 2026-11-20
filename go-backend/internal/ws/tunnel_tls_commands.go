package ws

// R18-F1-01 (D-3): panel↔agent tunnel-TLS wire contract.
//
// The panel owns one per-install CA and issues one certificate +
// private key per node (SAN: node-<id>.flmx.internal); distribution
// rides the existing AES-encrypted control channel as two commands:
//
//   - TunnelTLSIssue: panel sends the CA certificate, the node's
//     certificate and private key; the agent verifies the material and
//     persists it at 0600 under <agent workdir>/tls/. Only a successful
//     ack marks the node TLS-ready on the panel.
//   - TunnelTLSStatus: read-only; the agent reports whether the
//     on-disk material is present and still verifies, with
//     fingerprints, so the panel can re-confirm readiness before it
//     renders verified TLS.
//
// The sender (distribute/ensure) lands with the panel CA + issuance
// work; these constants exist first so the agent↔panel command census
// (go-gost/x/socket/command_contract_test.go) sees both ends of the
// contract in the same change that adds the agent route cases.
const (
	TunnelTLSIssueCommand  = "TunnelTLSIssue"
	TunnelTLSStatusCommand = "TunnelTLSStatus"
)
