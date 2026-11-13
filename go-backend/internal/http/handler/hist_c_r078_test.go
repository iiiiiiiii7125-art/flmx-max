package handler

import (
	"encoding/json"
	"sync"
	"testing"

	"go-backend/internal/http/response"
)

// R-078 (AUDIT-HIST L5): concurrent nodeCreate with the same name and
// IP both returned code 0 and persisted two indistinguishable rows —
// nodeCreate had no same-name-plus-same-IP check at all (only the
// Idempotency-Key replay dedup, which needs the client to send the
// same key). The fix serializes the check-then-insert on
// Handler.nodeCreateMu and rejects duplicates by name; nodeUpdate
// gets the same-family check when the pair changes.

func histCCreateNodePayload(name, serverIP string) string {
	payload, _ := json.Marshal(map[string]interface{}{
		"name": name, "serverIp": serverIP, "forwardMode": "agent",
	})
	return string(payload)
}

func TestHistCR078ConcurrentSameNameSameIPSecondFails(t *testing.T) {
	r, h := r2b2Open(t)
	payload := histCCreateNodePayload("hist-c-r078", "203.0.113.77")

	results := make([]response.R, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = pcFixCreateNode(t, h, payload)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, out := range results {
		if out.Code == 0 {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("exactly one concurrent create must succeed, got %d successes: %+v", succeeded, results)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name = 'hist-c-r078' AND server_ip = '203.0.113.77'`); n != 1 {
		t.Fatalf("concurrent creates persisted %d rows, want 1", n)
	}

	// Sequential repeat of the same pair must also fail.
	if out := pcFixCreateNode(t, h, payload); out.Code == 0 {
		t.Fatalf("sequential duplicate nodeCreate must fail, got %+v", out)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name = 'hist-c-r078' AND server_ip = '203.0.113.77'`); n != 1 {
		t.Fatalf("duplicate create persisted %d rows, want 1", n)
	}

	// Same name with a different IP, and same IP with a different name,
	// are still allowed (H79 note: only the pair is unique).
	if out := pcFixCreateNode(t, h, histCCreateNodePayload("hist-c-r078", "203.0.113.78")); out.Code != 0 {
		t.Fatalf("same name with different IP must succeed: %+v", out)
	}
	if out := pcFixCreateNode(t, h, histCCreateNodePayload("hist-c-r078-other", "203.0.113.77")); out.Code != 0 {
		t.Fatalf("same IP with different name must succeed: %+v", out)
	}
}

func TestHistCR078NodeUpdateToExistingPairFails(t *testing.T) {
	r, h := r2b2Open(t)
	if out := pcFixCreateNode(t, h, histCCreateNodePayload("hist-c-r078-a", "203.0.113.81")); out.Code != 0 {
		t.Fatalf("seed A must succeed: %+v", out)
	}
	if out := pcFixCreateNode(t, h, histCCreateNodePayload("hist-c-r078-b", "203.0.113.82")); out.Code != 0 {
		t.Fatalf("seed B must succeed: %+v", out)
	}
	bID := pcFixNodeID(t, r, "hist-c-r078-b")

	// Renaming B onto A's (name, IP) pair must fail and leave B intact.
	if env := kr1CallNodeUpdate(t, h, map[string]interface{}{"id": bID, "name": "hist-c-r078-a", "serverIp": "203.0.113.81"}); env.Code == 0 {
		t.Fatalf("nodeUpdate onto an existing (name, IP) pair must fail, got %+v", env)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name = 'hist-c-r078-a' AND server_ip = '203.0.113.81'`); n != 1 {
		t.Fatalf("rejected update changed the pair count to %d, want 1", n)
	}

	// Unrelated update of B (remark only) still succeeds.
	if env := kr1CallNodeUpdate(t, h, map[string]interface{}{"id": bID, "remark": "hist-c-ok"}); env.Code != 0 {
		t.Fatalf("unrelated nodeUpdate must succeed: %+v", env)
	}
}
