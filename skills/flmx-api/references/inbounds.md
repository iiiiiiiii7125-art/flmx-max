# Inbound Management API

Inbounds are the proxy endpoints rendered onto node kernels (Xray / sing-box / Snell).
All inbound endpoints require admin privileges unless noted. Inbound CRUD lives on
nodes: install a kernel first (see [kernels](kernels.md)), then create inbounds on it.

## POST /api/v1/inbound/list

List inbounds, optionally filtered by node / kernel.

## POST /api/v1/inbound/create

Create an inbound on a kernel. Protocol × kernel × transport combinations that the
kernel cannot render are rejected at creation time with a Chinese reason message
(see `/api/v1/kernel/capabilities` for the support matrix).

## POST /api/v1/inbound/update

Update an inbound (name, port, transport, security, settings, extra config).

## POST /api/v1/inbound/delete

Delete an inbound. An inbound selected as a default exit or split target must be
detached first.

## POST /api/v1/inbound/batch-delete

Delete multiple inbounds in one call; each affected kernel receives one config push.

## POST /api/v1/inbound/batch-toggle

Enable / disable multiple inbounds in one call.

## POST /api/v1/inbound/toggle

Enable / disable a single inbound.

## POST /api/v1/inbound/probe

Probe inbound reachability from the panel.

## POST /api/v1/inbound/split

Read / save split-routing rules for an inbound (which traffic exits via which
outbound inbound). Rules pointing at deleted outbounds drop traffic instead of
falling back to direct.

## POST /api/v1/inbound/reset-traffic

Reset an inbound's traffic counters.

## POST /api/v1/inbound/regenerate

Regenerate an inbound's credentials (UUID / password / keys, including VLESS ENC
material). Existing share links for the inbound change accordingly.

## POST /api/v1/inbound/share

Get an inbound's share link / QR payload. Snell has no generic share link (a Surge
config line is returned instead).

## POST /api/v1/inbound/import

Import external nodes: share links, Clash / sing-box / Surge configs, or a
subscription URL. Single import is capped at 500 nodes. Fetches go through the
panel's SSRF-protected fetcher (public https only, private-IP allowlist via
`FLMX_FETCH_PRIVATE_ALLOWLIST`).

## POST /api/v1/inbound/external/update

Update an imported (external) inbound, e.g. after re-importing its source.

## POST /api/v1/inbound/sub/info

Get subscription info for inbounds, including the default subscription token and
per-inbound subscription links. Login required.

## POST /api/v1/inbound/relay/list

List relay lines (tunnel forwards) attached to an inbound.

## POST /api/v1/inbound/relay/create

Attach a relay line to an inbound via a tunnel.

## POST /api/v1/inbound/relay/delete

Remove a relay line from an inbound.
