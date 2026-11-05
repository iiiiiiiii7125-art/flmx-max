# System / Announcement / Notify API

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

## GET /api/v1/announcement/get

Current announcement (login required).

## POST /api/v1/announcement/update

Create / update the announcement (admin only). Content is Markdown.

## POST /api/v1/notify/test

Send a test notification through the configured channel (admin only).

## POST /api/v1/public/config/get

Read a whitelisted public config value by name (no login; the only public config
endpoint).
