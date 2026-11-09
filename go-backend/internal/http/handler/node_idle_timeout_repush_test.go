package handler

import (
	"encoding/json"
	"testing"
	"time"
)

// F-212-A (panel layer): changing idleTimeoutSeconds through
// node/update persisted the value but never re-pushed the node's
// generated services, so a running agent's gost.json kept the old
// shape (LIVE-3 PF-3a attempt 1: DB showed 60 while the agent's
// config had no idleTimeout at all). The knob rides inside the
// generated service metadata, so a value change must trigger the
// same whole-node runtime redeploy the reconnect path uses
// (redeployLocalNodeRuntime) — the generation side already injects
// the current DB value into every service it builds.
//
// The value landing in the DB alone is not the behaviour under
// test; the UpdateService command reaching the (mock) agent with
// "idleTimeout": 60 in the service metadata is.
func TestNodeUpdateIdleTimeoutChangeRepushesServices(t *testing.T) {
	a := newCleanupAgent(t)
	seedR19F03Forward(t, a, 0, 0)
	a.probe(t)
	baseCount := len(a.commandsOfType("UpdateService"))

	res := postJSONToHandler(t, a.h.nodeUpdate, map[string]interface{}{
		"id": 1, "idleTimeoutSeconds": 60,
	})
	var env map[string]interface{}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, res.Body.String())
	}
	if env["code"].(float64) != 0 {
		t.Fatalf("nodeUpdate must succeed, got %v", env)
	}
	node, err := a.h.repo.GetNodeByID(1)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.IdleTimeoutSeconds != 60 {
		t.Fatalf("value not persisted: %+v", node)
	}

	deadline := time.Now().Add(8 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		for _, cmd := range a.commandsOfType("UpdateService")[baseCount:] {
			if payloadHasIdleTimeout(cmd.Data, 60) {
				found = true
				break
			}
		}
		if !found {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !found {
		t.Fatalf("no UpdateService with idleTimeout=60 reached the agent after node/update (F-212-A: value persisted, config never re-pushed); commands=%v",
			r19F03CommandTypes(t, a))
	}

	// Re-submitting the SAME value is not a change: it must not
	// trigger another whole-node redeploy.
	midCount := len(a.commandsOfType("UpdateService"))
	res = postJSONToHandler(t, a.h.nodeUpdate, map[string]interface{}{
		"id": 1, "idleTimeoutSeconds": 60,
	})
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode second nodeUpdate response: %v", err)
	}
	if env["code"].(float64) != 0 {
		t.Fatalf("second nodeUpdate must succeed, got %v", env)
	}
	time.Sleep(1500 * time.Millisecond)
	a.probe(t)
	if got := len(a.commandsOfType("UpdateService")); got != midCount {
		t.Fatalf("unchanged idleTimeoutSeconds triggered %d extra UpdateService commands", got-midCount)
	}
}

// payloadHasIdleTimeout reports whether a command payload carries
// the given idleTimeout value anywhere in its JSON tree (the
// service metadata sits nested inside the UpdateService payload).
func payloadHasIdleTimeout(data json.RawMessage, want float64) bool {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return false
	}
	return jsonTreeHasKeyValue(v, "idleTimeout", want)
}

func jsonTreeHasKeyValue(v interface{}, key string, want float64) bool {
	switch vv := v.(type) {
	case map[string]interface{}:
		for k, val := range vv {
			if k == key {
				if f, ok := val.(float64); ok && f == want {
					return true
				}
			}
			if jsonTreeHasKeyValue(val, key, want) {
				return true
			}
		}
	case []interface{}:
		for _, item := range vv {
			if jsonTreeHasKeyValue(item, key, want) {
				return true
			}
		}
	}
	return false
}
