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

> Note: 更新入站须回填凭据子字段，留空保留旧值；有意轮换走显式新值或 regenerate
> (`POST /api/v1/inbound/regenerate`). This covers the credential subfields
> (encryption secret, Reality private key / short IDs, listen address, UUID and
> password): leaving any of them empty on update keeps the stored value instead
> of rotating it. Creation is unchanged (empty values are generated).

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

## POST /api/v1/inbound/relay/probe

Measure relay lines now: `{"ids": [...]}` with relay line IDs, or all lines
when `ids` is empty / omitted. Entry nodes are probed first so the totals are
current. Response `data` is a map keyed by relay line ID (as a string); each
value carries the estimated per-point latency (`points`, same shape as node
latency points), the legs it is made of (`hops: [{from, to, ok, latencyMs,
lossPct, error}]`), the path total (`pathMs`) and the entry (`entry`,
`entryNodeId`).

## Outbound IP strategy (`outboundIpStrategy`)

Create / update payloads and inbound responses carry `outboundIpStrategy`,
which scopes the inbound's outbound connections: `auto` (default, dual-stack),
`ipv4_only`, `ipv6_only`, `prefer_ipv4`, `prefer_ipv6` (prefer falls back to
the other family after ~250 ms). Only Xray / sing-box kernels accept a
non-`auto` value (unknown values are rejected, never silently ignored);
sing-box must be ≥ 1.12 for any non-`auto` value, and Xray `prefer_*` needs
≥ 25.6.8. On update the field is optional: omit it to leave the strategy
unchanged.
