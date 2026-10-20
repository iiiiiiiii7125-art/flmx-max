# System / Announcement / Notify API

## POST /api/v1/system/version

Current panel version.

## POST /api/v1/system/storage

Panel storage usage breakdown.

## POST /api/v1/system/check-updates

Check GitHub for a newer panel release.

## POST /api/v1/system/upgrade

Start an in-panel self-upgrade (admin only). Progress and outcome are polled via
the status endpoint.

## POST /api/v1/system/upgrade/status

Self-upgrade status.

## POST /api/v1/announcement/get

Current announcement (login required).

## POST /api/v1/announcement/update

Create / update the announcement (admin only). Content is Markdown.

## POST /api/v1/notify/test

Send a test notification through the configured channel (admin only).

## POST /api/v1/public/config/get

Read a whitelisted public config value by name (no login; the only public config
endpoint).
