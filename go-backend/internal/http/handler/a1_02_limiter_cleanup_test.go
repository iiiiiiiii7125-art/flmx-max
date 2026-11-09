package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// A1-02 回归：限速规则在节点上落的流量 limiter 条目（LimiterConfig）
// 此前在任何删除路径都不下发 DeleteLimiters——删转发只发
// DeleteService＋DeleteCLimiters、删规则只 resync 服务摘引用，条目在
// agent gost.json 里永久残留（真机收据总账 DeleteLimiters ×0）。
// 本文件钉三条路径：
//   1. 删转发（两漏斗）：转发专属 rule_traffic_limit_<id> 恒删；总速
//      规则条目仅当本转发是唯一存续引用时删；
//   2. 共享规则：删其一不动规则条目，删尽最后引用才删；
//   3. 删规则（handler 全程）：resync 后向受影响节点补发规则条目，
//      以本规则作 IP 限速的转发另删其专属条目。
// 三条路径后，同步侧曾落的全部 limiter 名均有对位 DeleteLimiters——
// 即「清零后 agent 配置无 limiters」的命令面等价断言。

func seedA102SpeedRule(t *testing.T, a *cleanupAgent, id int64, speed int) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := a.h.repo.DB().Create(&model.SpeedLimit{ID: id, Name: fmt.Sprintf("rule-%d", id), Speed: speed, CreatedTime: now, Status: 1}).Error; err != nil {
		t.Fatalf("create speed limit %d: %v", id, err)
	}
}

func seedA102Forward(t *testing.T, a *cleanupAgent, tunnelID int64, name string, port int, speedID, ipSpeedID interface{}) *forwardRecord {
	t.Helper()
	now := time.Now().UnixMilli()
	forwardID, err := a.h.repo.CreateForwardTx(
		1, "admin", name, tunnelID, "203.0.113.9:9001", "fifo", now, 1,
		[]int64{1}, port, "", speedID, 0, 0, ipSpeedID, 0, 0, 0,
	)
	if err != nil {
		t.Fatalf("create forward %s: %v", name, err)
	}
	forward, err := a.h.getForwardRecord(forwardID)
	if err != nil {
		t.Fatalf("get forward %s: %v", name, err)
	}
	return forward
}

func seedA102Base(t *testing.T, a *cleanupAgent) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := a.h.repo.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return seedTunnelForNftables(t, a.h, "a1-02-tunnel", 1)
}

func a102DeleteLimiterNames(t *testing.T, a *cleanupAgent) []string {
	t.Helper()
	var names []string
	for _, cmd := range a.commandsOfType("DeleteLimiters") {
		var payload map[string]interface{}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode DeleteLimiters payload %s: %v", string(cmd.Data), err)
		}
		name, _ := payload["limiter"].(string)
		names = append(names, name)
	}
	return names
}

func a102NameCount(names []string, want string) int {
	count := 0
	for _, name := range names {
		if name == want {
			count++
		}
	}
	return count
}

func TestA102ForwardDeleteRemovesTrafficLimiters(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 41, 10)
	seedA102SpeedRule(t, a, 42, 20)
	forward := seedA102Forward(t, a, tunnelID, "a1-02-fwd", 22100, int64(41), int64(42))

	if err := a.h.deleteForwardServicesOnNodeBatch(forward, 1); err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	a.probe(t)
	if got := len(a.commandsOfType("DeleteService")); got == 0 {
		t.Fatalf("expected DeleteService to be sent")
	}
	names := a102DeleteLimiterNames(t, a)
	// 转发专属条目（IP 限速规则 42 落的）与唯一引用的总速规则条目 41。
	if got := a102NameCount(names, fmt.Sprintf("rule_traffic_limit_%d", forward.ID)); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for rule_traffic_limit_%d, got %d (all: %v)", forward.ID, got, names)
	}
	if got := a102NameCount(names, "41"); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for sole-referenced rule 41, got %d (all: %v)", got, names)
	}
	// IP 规则本身不落「42」同名条目（其条目名是 rule_traffic_limit_<fid>）。
	if got := a102NameCount(names, "42"); got != 0 {
		t.Fatalf("rule 42 must not be deleted by name, got %d (all: %v)", got, names)
	}
	// 顺序：DeleteLimiters 必须在 DeleteService 之后（服务还引用时不得先删）。
	seenDeleteService := false
	for _, typ := range r19F03CommandTypes(t, a) {
		if typ == "DeleteService" {
			seenDeleteService = true
		}
		if typ == "DeleteLimiters" && !seenDeleteService {
			t.Fatalf("DeleteLimiters must be sent after DeleteService")
		}
	}
}

func TestA102ForwardDeleteKeepsSharedRuleLimiterUntilLastReferrer(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 41, 10)
	first := seedA102Forward(t, a, tunnelID, "a1-02-fwd-a", 22101, int64(41), nil)
	second := seedA102Forward(t, a, tunnelID, "a1-02-fwd-b", 22102, int64(41), nil)

	if err := a.h.controlForwardServices(first, "DeleteService", true); err != nil {
		t.Fatalf("control delete first: %v", err)
	}
	a.probe(t)
	names := a102DeleteLimiterNames(t, a)
	if got := a102NameCount(names, "41"); got != 0 {
		t.Fatalf("shared rule 41 must survive while second forward references it, got %d deletes (all: %v)", got, names)
	}
	if got := a102NameCount(names, fmt.Sprintf("rule_traffic_limit_%d", first.ID)); got != 1 {
		t.Fatalf("expected the deleted forward's own traffic limiter to be removed, got %d (all: %v)", got, names)
	}

	// 镜像生产顺序：先删库行，再删第二条转发——此时引用集只剩它自己。
	if err := a.h.deleteForwardByID(first.ID); err != nil {
		t.Fatalf("delete first forward row: %v", err)
	}
	if err := a.h.controlForwardServices(second, "DeleteService", true); err != nil {
		t.Fatalf("control delete second: %v", err)
	}
	a.probe(t)
	names = a102DeleteLimiterNames(t, a)
	if got := a102NameCount(names, "41"); got != 1 {
		t.Fatalf("expected rule 41 limiter deleted with last referrer, got %d (all: %v)", got, names)
	}
}

func TestA102SpeedLimitDeleteSweepsNodeLimiters(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID := seedA102Base(t, a)
	seedA102SpeedRule(t, a, 43, 30)
	// F1 以规则 43 作总速；F2 以规则 43 作 IP 限速（专属条目名按转发 ID）。
	totalForward := seedA102Forward(t, a, tunnelID, "a1-02-total", 22103, int64(43), nil)
	ipForward := seedA102Forward(t, a, tunnelID, "a1-02-ip", 22104, nil, int64(43))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/speed-limit/delete", strings.NewReader(`{"id":43}`))
	rec := httptest.NewRecorder()
	a.h.speedLimitDelete(rec, req)
	a.probe(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("speed limit delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Code != 0 {
		t.Fatalf("speed limit delete envelope = %s (err %v)", rec.Body.String(), err)
	}
	names := a102DeleteLimiterNames(t, a)
	// 两条受影响转发同在节点 1：规则条目按（节点，名）去重，只发一次。
	if got := a102NameCount(names, "43"); got != 1 {
		t.Fatalf("expected exactly 1 DeleteLimiters for rule 43, got %d (all: %v)", got, names)
	}
	if got := a102NameCount(names, fmt.Sprintf("rule_traffic_limit_%d", ipForward.ID)); got != 1 {
		t.Fatalf("expected 1 DeleteLimiters for the IP-referencing forward's traffic limiter, got %d (all: %v)", got, names)
	}
	if got := a102NameCount(names, fmt.Sprintf("rule_traffic_limit_%d", totalForward.ID)); got != 0 {
		t.Fatalf("total-only forward has no traffic limiter entry, got %d (all: %v)", got, names)
	}
	// 规则行已删（handler 语义本体未动）。
	if exists, err := a.h.repo.SpeedLimitExists(43); err != nil || exists {
		t.Fatalf("rule 43 must be deleted, exists=%v err=%v", exists, err)
	}
}
