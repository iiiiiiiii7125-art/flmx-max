# Subscription Management API

A subscription bundles selected inbounds into one client link. Rendering follows a
fixed pipeline: filter → rename → flag → dedupe → prefix → sort (dedupe key is
host|port|protocol, name excluded). Format is chosen by client User-Agent, or by an
explicit format link. Admin endpoints require role_id 0.

## POST /api/v1/subscription/list

List all managed subscriptions.

## POST /api/v1/subscription/save

Create or update a subscription. Options include: selected inbounds / sources,
`relayIds` line scope (empty = all lines, 0 = direct only, N = that relay only),
filter / rename / flag / dedupe / prefix / sort processing, rule groups, and a
custom JS rename script (goja sandbox, 1s timeout).

## POST /api/v1/subscription/delete

Delete a subscription. Its user assignments (user-subscription) are cleared
automatically.

## POST /api/v1/subscription/reset

Reset a subscription's token. Old links stop working immediately.

## POST /api/v1/subscription/reset-traffic

Reset a share subscription's traffic counters.

## POST /api/v1/subscription/traffic

Traffic statistics for one subscription (hourly for the last 24 hours, daily
for the last 30 days, monthly for the last 12 months; totals for the whole
subscription, shared by all assignees). Hourly samples are retained 400 days.

## POST /api/v1/user/subscription/traffic

Same traffic statistics from the assignee's perspective, for a subscription
assigned to the current user (see "my subscriptions").

## POST /api/v1/subscription/preview

Preview the rendered node list for a subscription without saving.

## POST /api/v1/subscription/source/list

List remote subscription sources.

## POST /api/v1/subscription/source/save

Create or update a remote subscription source (URL + sync interval). Synced nodes
are matched by name first, then by address.

## POST /api/v1/subscription/source/refresh

Trigger an immediate sync of a source.

## POST /api/v1/subscription/source/delete

Delete a source.

## POST /api/v1/user-subscription/list

List per-subscription user assignments (admin). This is the current (1.0.12+)
assignment model: users are granted whole subscriptions.

## POST /api/v1/user-subscription/set

Set which subscriptions a user is assigned.

## POST /api/v1/user-subscription/mine

Current user's own assigned subscriptions, with their independent links, traffic
and expiry. Users without any assignment fall back to their legacy personal link.

## POST /api/v1/user-inbound/list

List legacy per-inbound user assignments (pre-1.0.12 model, kept for existing data).

## POST /api/v1/user-inbound/set

Set legacy per-inbound assignments for a user (direct inbound or a specific relay).

## POST /api/v1/user-inbound/mine

Current user's legacy personal subscription data: builds the signed `?user=` link
(signatures accept flmx-subsig-v3 → v2 → v1) and per-inbound / per-relay links
(`?inbound=`, `?relay=`).

## GET /api/v1/open_api/sub/<token>

Public subscription fetch (no login; the token is the credential — do not share it
publicly). Returns the format matching the client UA. With `?user=<id>&sig=<sig>`
it renders that user's assigned view; without it, the subscription's full content.

## POST /api/v1/open_api/sub_store

Compatibility endpoint for external subscription tools: exchanges panel account
credentials (`user` / `password` in the POST body, never in the URL) plus a tunnel
name for that user's subscription content.

## GET /api/v1/open_api/gh/<url>

Built-in GitHub mirror proxy used to accelerate kernel / rule downloads. Only
whitelisted GitHub repos are proxied; on failure the panel falls back to cached
copies.
