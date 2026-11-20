# System / Notify API

## POST /api/v1/system/version

Current panel version.

## POST /api/v1/system/storage

(Accepts GET as well as POST.)

Panel storage usage breakdown.

## POST /api/v1/system/check-updates

Check GitHub for a newer panel release.

## POST /api/v1/system/upgrade

Start an in-panel self-upgrade (admin only). Progress and outcome are polled via
the status endpoint.

## GET /api/v1/system/upgrade/status

(Accepts POST as well as GET.)

Self-upgrade status.

## POST /api/v1/system/gh-cache/purge

Purge one version's GitHub-mirror tag asset cache (admin only). Use
it after a tag is moved or re-cut: the mirror caches tag assets for
24 hours, and a poisoned entry would otherwise keep serving the old
hash until expiry (R18-B8-01). The version must pass the redirect-tag
check — an invalid version is rejected and nothing is deleted
(fail-closed). A version with no cached files answers `removed: 0`
without error.

**Request:** `{"version": "2.1.18"}`

**Response:** `data` carries the version and how many cached files
were actually removed:

```json
{"version": "2.1.18", "removed": 6}
```

Clearing failures (invalid version, disk error) answer envelope
`code` -2 instead of silently reporting success. Non-POST methods
and non-admin callers are rejected.

## POST /api/v1/system-info/ticket

Issue the short-lived ticket the browser realtime WebSocket handshake
now requires (login required; the JWT travels in the `Authorization`
header as usual). No request body.

**Response:** `data` carries the ticket and its lifetime in seconds:

```json
{"ticket": "<base64url token>", "expiresIn": 30}
```

The ticket is valid for 30 seconds and single-use: the first
handshake attempt consumes it, whether it succeeds or not. It is bound
to the issuing user and to that user's current token revocation
epoch, so logging out or changing the password voids unconsumed
tickets. Usage: fetch a ticket, then open the socket as
`/system-info?type=0&ticket=<ticket>`; fetch a fresh ticket for every
(re)connect. A bare JWT in the WebSocket query string is no longer
accepted — the `secret` query parameter remains only for the type=1
agent/node channel. A disabled account or a stale/revoked token is
answered with envelope `code` 401; non-POST methods with `code` 405.

## POST /api/v1/notify/test

Send a test notification through the configured channel (admin only).

## POST /api/v1/notify/health

Report whether saved notification settings can actually deliver
anything (admin only, no request body). A configuration where the
master switch is on but no channel is usable (Telegram token/chat ID
or webhook missing, or events set to `none`) silently drops every
event — this endpoint surfaces it (C-06). No secrets are returned,
only presence booleans.

**Response:** `data` is:

```json
{
  "enabled": true,
  "telegramTokenSet": true,
  "telegramChatIdSet": false,
  "webhookSet": false,
  "eventsSelected": true,
  "usable": false,
  "missing": ["telegramChatId", "webhook"]
}
```

`usable` is false when the master switch is off, no channel is
usable, or no events are selected. `missing` names the gaps only
when the master switch is on (off is a deliberate state, not a gap).

## POST /api/v1/public/config/get

Read a whitelisted public config value by name (no login; the only public config
endpoint).
