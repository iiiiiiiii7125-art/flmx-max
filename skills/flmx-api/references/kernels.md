# Kernel Management API

Kernels are the proxy cores (Xray / sing-box / Snell) installed per node. Inbounds
are rendered onto kernels; see [inbounds](inbounds.md). All endpoints require
admin privileges.

## POST /api/v1/kernel/capabilities

The capability matrix: which protocol × kernel × transport combinations can be
rendered, plus version gates (e.g. Xray ≥ 25.9.5 for VLESS ENC, sing-box ≥ 1.12
for AnyTLS, ≥ 1.14 for ACME, Snell major versions). Unsupported combinations are
rejected when creating an inbound.

## POST /api/v1/kernel/list

List kernels installed on nodes, with type, version and enabled state.

## POST /api/v1/kernel/versions

List downloadable versions for a kernel type.

## POST /api/v1/kernel/install

Install (or upgrade) a kernel on a node, optionally at a specific version.
Downloads are checksum-verified; on failure the previous working configuration
is restored.

## POST /api/v1/kernel/control

Start / stop / restart a kernel on a node.

## POST /api/v1/kernel/logs

Fetch recent kernel logs from a node (ring buffer).
