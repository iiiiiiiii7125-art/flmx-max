package handler

import (
	"log"
	"strings"
	"time"
)

// nodeMetricStaleAfter 是节点指标陈旧阈值（A-低-6）：agent 每 30 秒
// 上报一次指标，5 分钟（10 倍间隔）无新行即视为累计数冻结——节
// 点离线时 node_metric 行停止推进，月流量等累计数静默停在旧值；
// 阈值外由 attachNodeMetricStaleness 在节点列表行上打 metricStale
// 标记，节点页据此显式标注「数据陈旧」。
const nodeMetricStaleAfter = 5 * time.Minute

// attachNodeMetricStaleness enriches node list rows with the metric
// freshness marker (A-低-6): lastMetricAt is the latest node_metric
// timestamp (UnixMilli, 0 = never reported), metricStale is true when
// that timestamp is older than nodeMetricStaleAfter (or absent). One
// batch lookup for all listed nodes (same shape as
// attachConnlogMarkStates). On lookup failure the rows carry no
// marker (pre-fix behaviour) and the failure is logged: a DB error
// must not fake freshness.
func (h *Handler) attachNodeMetricStaleness(items []map[string]interface{}) {
	if h == nil || len(items) == 0 {
		return
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		if id := asInt64(item["id"], 0); id != 0 {
			ids = append(ids, id)
		}
	}
	nowMs := time.Now().UnixMilli()
	latest := map[int64]int64{}
	if h.repo != nil {
		var err error
		latest, err = h.repo.LatestNodeMetricTimes(ids)
		if err != nil {
			log.Printf("attach node metric staleness: LatestNodeMetricTimes failed: %v (stale markers omitted)", err)
		}
	}
	for _, item := range items {
		id := asInt64(item["id"], 0)
		if id == 0 {
			continue
		}
		last := latest[id]
		item["lastMetricAt"] = last
		item["metricStale"] = last == 0 || nowMs-last > nodeMetricStaleAfter.Milliseconds()
	}
}

// attachNodeNicShared enriches node list rows with the NIC-sharing
// marker (A-低-5): nodes sharing one server address are grouped by
// normalized IP (serverIpV4 when set, else serverIp); a group with
// more than one member gets nicSharedWith = the other member IDs.
// The marker is a display hint for the ingestion-side dedupe
// (shouldCountNicTrafficLocked, keyed by BootID): same-IP nodes
// usually share one machine's NIC counters, so only the lowest node
// ID's monthly total counts. Same-IP is an approximation (NAT /
// multi-homed hosts can share an address without sharing a NIC), so
// the marker never gates billing — it only explains the dedupe.
func (h *Handler) attachNodeNicShared(items []map[string]interface{}) {
	if len(items) == 0 {
		return
	}
	byIP := make(map[string][]int64)
	ips := make(map[int64]string, len(items))
	for _, item := range items {
		id := asInt64(item["id"], 0)
		if id == 0 {
			continue
		}
		ip := nodeListServerIP(item)
		if ip == "" {
			continue
		}
		ips[id] = ip
		byIP[ip] = append(byIP[ip], id)
	}
	for _, item := range items {
		id := asInt64(item["id"], 0)
		if id == 0 {
			continue
		}
		group, ok := byIP[ips[id]]
		if !ok || len(group) < 2 {
			continue
		}
		others := make([]int64, 0, len(group)-1)
		for _, other := range group {
			if other != id {
				others = append(others, other)
			}
		}
		item["nicSharedWith"] = others
	}
}

// nodeListServerIP returns the normalized server address of a node
// list row: serverIpV4 when set, else serverIp; trimmed and
// lower-cased. A nullable (nil) value reads as "".
func nodeListServerIP(item map[string]interface{}) string {
	if v, ok := item["serverIpV4"]; ok {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.ToLower(strings.TrimSpace(s))
		}
	}
	if v, ok := item["serverIp"]; ok {
		if s, ok := v.(string); ok {
			return strings.ToLower(strings.TrimSpace(s))
		}
	}
	return ""
}
