# Monitoring API

Monitoring endpoints are NOT admin-only at the middleware layer: each handler
checks access itself — the caller must be an admin (role_id 0) or hold a monitor
permission grant (see the permission endpoints below, which are admin-only).

## POST /api/v1/monitor/access

Whether the current user may access monitoring data.

## POST /api/v1/monitor/nodes

Node metric summaries for all nodes (CPU / memory / disk / network / load /
connection counts).

## POST /api/v1/monitor/nodes/<id>

Metric history for one node.

## POST /api/v1/monitor/tunnels

Tunnel metric summaries.

## POST /api/v1/monitor/tunnels/quality

Tunnel quality samples (entry→exit and exit→target latency / loss).

## POST /api/v1/monitor/tunnels/<id>

Metric history for one tunnel.

## POST /api/v1/monitor/services

List service monitors (HTTP / TCP / ICMP probes run from nodes).

## POST /api/v1/monitor/services/create

Create a service monitor.

## POST /api/v1/monitor/services/update

Update a service monitor.

## POST /api/v1/monitor/services/delete

Delete a service monitor.

## POST /api/v1/monitor/services/run

Run a service monitor once, immediately.

## POST /api/v1/monitor/services/latest-results

Latest probe result per monitor.

## POST /api/v1/monitor/services/limits

Current monitor count vs. allowed limits.

## POST /api/v1/monitor/services/<id>

Probe result history for one monitor.

## POST /api/v1/monitor/permission/list

List users holding a monitor permission grant (admin only).

## POST /api/v1/monitor/permission/assign

Grant monitoring access to a user (admin only).

## POST /api/v1/monitor/permission/remove

Revoke a user's monitoring access (admin only).
