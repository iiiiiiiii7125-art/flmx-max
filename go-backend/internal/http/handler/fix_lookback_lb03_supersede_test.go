package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// LB-03 回归（AUDIT-211-LOOKBACK 新立）：回退无 toVersion（老 agent
// 无代际配对）时 nodeRollback 此前完全不触碰核验行——窗内未结的升
// 级行在回退落地后照样过截止翻 failed、误发「升级未落地」告警，
// 红色状态行长挂。本文件钉三条（回包经真 WS mock 控制）：
//   1. 无目标回退成功 → 未结行结案 superseded（理由在册）、状态
//      行消失、后续扫描不发 node_upgrade_failed、且终态不被重连
//      核销翻成 landed（不伪造落地）；
//   2. 带 toVersion 的回退路径不变：落 rollback 核验行、命中回连
//      照常核销 landed；
//   3. 已 failed 的行被无目标回退结案后不再重复告警、状态行消失。

// startLB03MockNode 起一个 mock agent：普通命令立即回成功；
// RollbackAgent 的回包 data 由 rollbackData 决定（nil = 老 agent
// 形态，回包不带任何 data，即无目标回退）。
func startLB03MockNode(t *testing.T, baseURL, secret string, rollbackData map[string]interface{}) func() {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	u.Scheme = "ws"
	u.Path = "/system-info"
	q := u.Query()
	q.Set("type", "1")
	q.Set("secret", secret)
	q.Set("version", "v1")
	q.Set("http", "1")
	q.Set("tls", "1")
	q.Set("socks", "1")
	u.RawQuery = q.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial mock node: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, raw, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}
			plain := raw
			var wrap struct {
				Encrypted bool   `json:"encrypted"`
				Data      string `json:"data"`
			}
			if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Encrypted && strings.TrimSpace(wrap.Data) != "" {
				if crypto, cryptoErr := security.NewAESCrypto(secret); cryptoErr == nil {
					if dec, decErr := crypto.Decrypt(wrap.Data); decErr == nil {
						plain = []byte(dec)
					}
				}
			}
			var cmd struct {
				Type      string `json:"type"`
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(plain, &cmd); err != nil || strings.TrimSpace(cmd.RequestID) == "" {
				continue
			}
			resp := map[string]interface{}{
				"type":      fmt.Sprintf("%sResponse", cmd.Type),
				"success":   true,
				"message":   "OK",
				"requestId": cmd.RequestID,
			}
			if cmd.Type == "RollbackAgent" && rollbackData != nil {
				resp["data"] = rollbackData
			}
			respBytes, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, respBytes)
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = conn.Close()
			wg.Wait()
		})
	}
}

func lb03SetupNode(t *testing.T, h *Handler, r *repo.Repository, name, ip string, rollbackData map[string]interface{}) int64 {
	t.Helper()
	srv := httptest.NewServer(h.WebSocketHandler())
	t.Cleanup(srv.Close)
	id := seedWaveNode(t, r, name, ip)
	stop := startLB03MockNode(t, srv.URL, name+"-secret", rollbackData)
	t.Cleanup(stop)
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(id) }, name+" online")
	return id
}

func lb03SeedPendingUpgrade(t *testing.T, r *repo.Repository, nodeID int64, dispatchedAt, deadlineAt int64) {
	t.Helper()
	if err := r.UpsertNodeUpgradeVerification(&model.NodeUpgradeVerification{
		NodeID: nodeID, Kind: upgradeVerifyKindUpgrade, TargetVersion: "2.1.0",
		FromVersion: "2.0.0", DispatchedAt: dispatchedAt, DeadlineAt: deadlineAt,
		CreatedTime: dispatchedAt, UpdatedTime: dispatchedAt,
	}); err != nil {
		t.Fatalf("seed pending verification: %v", err)
	}
}

func lb03AssertNotVisible(t *testing.T, r *repo.Repository, nodeID int64) {
	t.Helper()
	visible, err := r.ListVisibleNodeUpgradeVerifications([]int64{nodeID})
	if err != nil {
		t.Fatalf("list visible: %v", err)
	}
	if _, ok := visible[nodeID]; ok {
		t.Fatalf("node %d must not carry a visible verification state", nodeID)
	}
}

func TestFixLookbackLB03TargetlessRollbackSupersedesPending(t *testing.T) {
	h, r, sent := setupNotifyHandler(t)
	id := lb03SetupNode(t, h, r, "lb03-a", "198.51.100.31", nil)
	now := time.Now().UnixMilli()
	lb03SeedPendingUpgrade(t, r, id, now, now+5*60*1000)

	if env := callNodeOp(t, h.nodeRollback, map[string]interface{}{"id": id}); env.Code != 0 {
		t.Fatalf("targetless rollback must succeed, got %+v", env)
	}
	rec := getVerifyRecord(t, r, id)
	if rec == nil || rec.Status != repo.NodeUpgradeVerifySuperseded {
		t.Fatalf("open record must be superseded by targetless rollback: %+v", rec)
	}
	if !strings.Contains(rec.CloseReason, "被无目标回退接管") || rec.ResolvedAt == 0 {
		t.Fatalf("supersede must record the takeover reason and resolve time: %+v", rec)
	}
	lb03AssertNotVisible(t, r, id)

	// 结案后扫描（含越过原截止）不得发「升级未落地」告警。
	h.sweepNodeUpgradeVerifications(time.Now().Add(10 * time.Minute))
	if msgs := sent(); len(msgs) != 0 {
		t.Fatalf("superseded record must never alert, got %v", msgs)
	}
	// 终态：即便节点随后以原升级目标版本回连，也不得翻 landed
	// （回退落地未经核验，不伪造）。
	setNodeReportedVersion(t, r, id, "2.1.0")
	h.verifyNodeUpgradeLanding(id)
	if rec := getVerifyRecord(t, r, id); rec.Status != repo.NodeUpgradeVerifySuperseded {
		t.Fatalf("superseded must be terminal: %+v", rec)
	}
}

func TestFixLookbackLB03TargetedRollbackPathUnchanged(t *testing.T) {
	h, r, _ := setupNotifyHandler(t)
	data := map[string]interface{}{
		"toVersion": "2.0.0", "fromVersion": "2.1.0", "backupGeneration": "previous",
	}
	id := lb03SetupNode(t, h, r, "lb03-b", "198.51.100.32", data)
	now := time.Now().UnixMilli()
	lb03SeedPendingUpgrade(t, r, id, now, now+5*60*1000)

	if env := callNodeOp(t, h.nodeRollback, map[string]interface{}{"id": id}); env.Code != 0 {
		t.Fatalf("targeted rollback must succeed, got %+v", env)
	}
	rec := getVerifyRecord(t, r, id)
	if rec == nil || rec.Kind != upgradeVerifyKindRollback || rec.TargetVersion != "2.0.0" ||
		rec.Status != repo.NodeUpgradeVerifyPending || rec.CloseReason != "" {
		t.Fatalf("targeted rollback must record its own verification row: %+v", rec)
	}
	setNodeReportedVersion(t, r, id, "2.0.0")
	h.verifyNodeUpgradeLanding(id)
	if rec := getVerifyRecord(t, r, id); rec.Status != repo.NodeUpgradeVerifyLanded {
		t.Fatalf("targeted rollback must land on matching reconnect: %+v", rec)
	}
}

func TestFixLookbackLB03TargetlessRollbackSupersedesFailed(t *testing.T) {
	h, r, sent := setupNotifyHandler(t)
	id := lb03SetupNode(t, h, r, "lb03-c", "198.51.100.33", nil)
	past := time.Now().Add(-10 * time.Minute).UnixMilli()
	lb03SeedPendingUpgrade(t, r, id, past, past+60*1000)

	// 先让它按原口径翻 failed 并告警一次（告警本身为真，不撤回）。
	h.sweepNodeUpgradeVerifications(time.Now())
	if rec := getVerifyRecord(t, r, id); rec.Status != repo.NodeUpgradeVerifyFailed {
		t.Fatalf("precondition failed state: %+v", rec)
	}
	if msgs := sent(); len(msgs) != 1 || msgs[0].Event != notifyEventNodeUpgradeFailed {
		t.Fatalf("precondition exactly one failure alert: %v", msgs)
	}

	if env := callNodeOp(t, h.nodeRollback, map[string]interface{}{"id": id}); env.Code != 0 {
		t.Fatalf("targetless rollback must succeed, got %+v", env)
	}
	if rec := getVerifyRecord(t, r, id); rec.Status != repo.NodeUpgradeVerifySuperseded {
		t.Fatalf("failed record must be superseded by targetless rollback: %+v", rec)
	}
	lb03AssertNotVisible(t, r, id)
	h.sweepNodeUpgradeVerifications(time.Now())
	if msgs := sent(); len(msgs) != 1 {
		t.Fatalf("no further alert after supersede, got %d messages", len(msgs))
	}
}
