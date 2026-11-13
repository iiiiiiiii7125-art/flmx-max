package handler

import "log"

// Agent-reported connlog-mark state (lane C): the agent counts, on
// every metric heartbeat (5s), how many of its currently loaded
// forward services carry the panel's connlog entry mark — read off
// the live service registry, never off a config file — and reports
// the count in its SystemInfo. The panel keeps the last reported
// value per node in memory (like nodeLatency) and compares it with
// the node's desired switch (node.conn_log_enabled) to detect a
// config push the agent never applied.
//
// Deliberately in-memory, not a DB column: the value is live agent
// state, stale the moment the agent disconnects (the offline hook
// drops it), and a panel restart repopulates it within one metric
// tick. Persisting it would let a dead agent's last count pose as
// current truth. It never enters backups (runtime state, like
// nftablesStatus's live half, without the column).

// connlogMarkNotAppliedWarning is the mismatch string surfaced to
// the UI/operator when the node switch is on but the agent reports
// zero marked forwards: the desired config never reached the
// agent's loaded services. Trigger a re-push (lane A) to repair.
const connlogMarkNotAppliedWarning = "配置未下发到 agent"

// connlogMarkUnknownWarning is the honest-unknown string when the
// node switch is on but the agent has never reported a mark count
// (old agent, or not connected since the panel started): no verdict
// either way, never a silent "ok".
const connlogMarkUnknownWarning = "agent 未上报打标状态"

// setConnlogMark records one node's latest agent-reported marked
// forward count (metric heartbeat). Only called with a non-nil
// report: an old agent that omits the field leaves the previous
// state untouched (and a fresh node stays unreported).
func (h *Handler) setConnlogMark(nodeID int64, marked int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.connlogMarkMu.Lock()
	defer h.connlogMarkMu.Unlock()
	if h.connlogMarkByNode == nil {
		h.connlogMarkByNode = make(map[int64]int64)
	}
	h.connlogMarkByNode[nodeID] = marked
}

// dropConnlogMark forgets a node's reported mark state (offline):
// a disconnected agent's last count is not evidence about its
// current loaded services.
func (h *Handler) dropConnlogMark(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.connlogMarkMu.Lock()
	defer h.connlogMarkMu.Unlock()
	delete(h.connlogMarkByNode, nodeID)
}

// connlogMark returns the last reported marked-forward count for a
// node and whether any report arrived since (re)connect.
func (h *Handler) connlogMark(nodeID int64) (marked int64, reported bool) {
	if h == nil || nodeID <= 0 {
		return 0, false
	}
	h.connlogMarkMu.Lock()
	defer h.connlogMarkMu.Unlock()
	marked, reported = h.connlogMarkByNode[nodeID]
	return marked, reported
}

// connlogMarkMismatch is the desired-vs-actual verdict: the node
// switch wants collection, the node has forwards that should carry
// the mark, but the agent reports no marked forward at all.
// Unreported (old agent / never connected) is unknown, never a
// mismatch — only a positive "0" from a reporting agent proves the
// push missed. hasForwards is load-bearing: a node with zero
// forwards reports 0 because there is nothing to mark, and no
// re-push can ever change that — warning there would be a false
// positive (verified live on a forward-less node before this gate).
func connlogMarkMismatch(enabled bool, marked int64, reported bool, hasForwards bool) bool {
	return enabled && reported && marked == 0 && hasForwards
}

// connlogMarkWarning renders the verdict for the connlog view: the
// mismatch string when the push missed, the unknown string when
// the switch is on but nothing was ever reported, "" otherwise. A
// reported 0 on a forward-less node is "" (nothing to mark), not
// unknown — the agent did report, and its answer is vacuously true.
func connlogMarkWarning(enabled bool, marked int64, reported bool, hasForwards bool) string {
	switch {
	case connlogMarkMismatch(enabled, marked, reported, hasForwards):
		return connlogMarkNotAppliedWarning
	case enabled && !reported:
		return connlogMarkUnknownWarning
	default:
		return ""
	}
}

// attachConnlogMarkStates enriches node list rows with the
// agent-reported mark state (mirrors attachNodeLatency):
// connlogMarkedForwards is the last reported count, or null when
// the agent never reported (old agent / offline); connlogMarkMismatch
// is true only on a proven mismatch (switch on, node has forwards,
// agent reports 0); connlogMarkWarning carries "配置未下发到 agent"
// on mismatch and is absent otherwise.
//
// The forward gate is one batch lookup for all listed nodes (same
// shape as attachNodeUninstallStates). On lookup failure the gate
// stays open (pre-gate behaviour) and the failure is logged: a DB
// error must not hide a real mismatch. A nil repo (unit rigs that
// build a bare Handler) likewise leaves the gate open.
func (h *Handler) attachConnlogMarkStates(items []map[string]interface{}) {
	if h == nil || len(items) == 0 {
		return
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		if id := asInt64(item["id"], 0); id != 0 {
			ids = append(ids, id)
		}
	}
	hasForwards := func(int64) bool { return true }
	if h.repo != nil {
		if ports, err := h.repo.ListForwardPortsByNodes(ids); err != nil {
			log.Printf("attach connlog mark states: ListForwardPortsByNodes failed: %v (mismatch verdict falls back to ungated)", err)
		} else {
			byNode := ports
			hasForwards = func(id int64) bool { return len(byNode[id]) > 0 }
		}
	}
	for _, item := range items {
		id := asInt64(item["id"], 0)
		if id == 0 {
			continue
		}
		marked, reported := h.connlogMark(id)
		if reported {
			item["connlogMarkedForwards"] = marked
		} else {
			item["connlogMarkedForwards"] = nil
		}
		enabled := asInt64(item["connLogEnabled"], 0) == 1
		if mismatch := connlogMarkMismatch(enabled, marked, reported, hasForwards(id)); mismatch {
			item["connlogMarkMismatch"] = true
			item["connlogMarkWarning"] = connlogMarkNotAppliedWarning
		} else {
			item["connlogMarkMismatch"] = false
		}
	}
}
