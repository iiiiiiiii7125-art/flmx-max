package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// TestNodeBatchUpgradeDedupesIDs 钉 R4B03-004：同批重复 id 只下发
// 一条 UpgradeAgent、回执每节点恰一条。
func TestNodeBatchUpgradeDedupesIDs(t *testing.T) {
	stubGHMirror404(t) // 下发预热本地化＋排空（GATE F3），不研究预热本身
	r, err := repo.Open(t.TempDir() + "/r4-batch-dedupe.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	nodeID := kr1SeedNode(t, r, "r4-batch-node", "198.51.100.41", 0, "agent")
	rec := &kr1CommandRecorder{}
	stop := startKR1MockNode(t, srv.URL, "r4-batch-node-secret", rec)
	defer stop()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(nodeID) }, "batch mock node online")

	req := newAuthenticatedJSONRequest(t, map[string]interface{}{
		"ids":     []int64{nodeID, nodeID, nodeID},
		"version": "9.9.9",
	})
	httpRec := httptest.NewRecorder()
	h.nodeBatchUpgrade(httpRec, req)

	var payload batchResultsPayload
	if err := json.Unmarshal(httpRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, httpRec.Body.String())
	}
	if len(payload.Data.Results) != 1 {
		t.Fatalf("回执条数 = %d, want 1（重复 ids 去重保序）", len(payload.Data.Results))
	}
	if !payload.Data.Results[0].Success {
		t.Fatalf("唯一回执应为成功: %+v", payload.Data.Results[0])
	}
	if got := rec.count("UpgradeAgent"); got != 1 {
		t.Fatalf("UpgradeAgent 下发次数 = %d, want 1", got)
	}
}
