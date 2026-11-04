# Monitoring API

Access differs per face (each handler checks access itself):

- **Node and service monitoring are admin-only.** A monitor permission
  grant does NOT open them.
- **Tunnel monitoring** is open to admins and to users holding a monitor
  permission grant; non-admin callers only ever see tunnels they can
  access.
- `GET /api/v1/monitor/access` reports the current caller's access.

## GET /api/v1/monitor/access

Whether the current user may access monitoring data.

## GET /api/v1/monitor/nodes

Node metric summaries for all nodes (CPU / memory / disk / network / load /
connection counts). Admin only.

## GET /api/v1/monitor/nodes/<id>

Metric history for one node.

## GET /api/v1/monitor/tunnels

Tunnel metric summaries.

## GET /api/v1/monitor/tunnels/quality

Tunnel quality samples (entry→exit and exit→target latency / loss).

## GET /api/v1/monitor/tunnels/<id>

Metric history for one tunnel.

## GET /api/v1/monitor/services

List service monitors (HTTP / TCP / ICMP probes run from nodes). All
service-monitor endpoints below are admin only.

## POST /api/v1/monitor/services/create

Create a service monitor.

## POST /api/v1/monitor/services/update

Update a service monitor.

## POST /api/v1/monitor/services/delete

Delete a service monitor.

## POST /api/v1/monitor/services/run

Run a service monitor once, immediately.

## GET /api/v1/monitor/services/latest-results

Latest probe result per monitor.

## GET /api/v1/monitor/services/limits

Current monitor count vs. allowed limits.

## GET /api/v1/monitor/services/<id>

Probe result history for one monitor.

## GET /api/v1/monitor/permission/list

List users holding a monitor permission grant (admin only).

## POST /api/v1/monitor/permission/assign

Grant monitoring access to a user (admin only).

## POST /api/v1/monitor/permission/remove

Revoke a user's monitoring access (admin only).
