package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// R6X-017：限速规则实体编辑（改值/禁用）/删除后，必须按引用集（转发
// speed_id / ip_speed_id 直引 + user_tunnel.speed_id 间接引）走既有
// sync 通道（syncForwardServices）重下发受影响转发；失败只记日志、
// 不回滚 DB。三变体以入口节点 mock agent 收到的命令为断言面：
// AddLimiters 载荷（限速值）与 UpdateService 服务 limiter 字段（引用
// 归属）必须随 DB 新口径更新/回退（修复前操作后节点零新命令）。

type r6x017Scenario struct {
	h          *Handler
	repo       *repo.Repository
	server     *ws.Server
	nodeID     int64
	tunnelID   int64
	forwardID  int64
	ruleID     int64 // 被编辑/删除的规则 R（10 Mbps）
	fallbackID int64 // UT 级回退规则 F（5 Mbps）
	rc         *r1b3CommandRecorder
}

// refSel picks which rule a reference points at: 0=无引用, 1=R, 2=F。
func r6x017Setup(t *testing.T, forwardRef, utRef int) *r6x017Scenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r6x017.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	server := ws.NewServer(r, "test-secret")
	server.SetNodeOnlineHook(h.onNodeOnline)
	server.SetNodeOfflineHook(h.notifyNodeOffline)
	h.wsServer = server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)

	s := &r6x017Scenario{h: h, repo: r, server: server, rc: &r1b3CommandRecorder{}}
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	s.nodeID = seedRelayTargetNode(t, r, "speed-a", "198.51.100.21")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node SET port = '1000-65535'`).Error; err != nil {
		t.Fatal(err)
	}
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx.Error)
	}
	tunnelID, err := r.CreateTunnelTx(tx, "r6x017-tunnel", 1, 1, 1, now, 1, nil, 1, "", "", 0)
	if err != nil {
		t.Fatalf("create tunnel: %v", err)
	}
	if err := r.CreateChainTunnelTx(tx, tunnelID, "1", s.nodeID, sql.NullInt64{}, "", 1, "tls", ""); err != nil {
		t.Fatalf("create chain tunnel: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit tunnel: %v", err)
	}
	s.tunnelID = tunnelID

	mkLimit := func(name string, speed int) int64 {
		t.Helper()
		sl := &model.SpeedLimit{Name: name, Speed: speed, CreatedTime: now, Status: 1}
		if err := r.DB().Create(sl).Error; err != nil {
			t.Fatalf("seed speed_limit %s: %v", name, err)
		}
		return sl.ID
	}
	s.ruleID = mkLimit("r6x017-rule", 10)
	s.fallbackID = mkLimit("r6x017-fallback", 5)
	pick := func(sel int) int64 {
		switch sel {
		case 1:
			return s.ruleID
		case 2:
			return s.fallbackID
		}
		return 0
	}

	var forwardSpeed interface{}
	if id := pick(forwardRef); id > 0 {
		forwardSpeed = id
	}
	fwdID, err := r.CreateForwardTx(1, "admin", "r6x017-fwd", tunnelID, "203.0.113.20:443", "fifo", now, 1,
		[]int64{s.nodeID}, 20001, "", forwardSpeed, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	s.forwardID = fwdID

	if utRef > 0 {
		utSpeed := pick(utRef)
		if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(1, ?, ?, 10, 100, 0, 0, 0, 0, 0, 1)`,
			tunnelID, utSpeed).Error; err != nil {
			t.Fatalf("seed user_tunnel: %v", err)
		}
	}

	connectR1B3Agent(t, ts, "speed-a-secret", s.rc)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(s.nodeID)
	}, "mock agent online")
	waitForCondition(t, 5*time.Second, func() bool {
		n := len(s.rc.snapshot())
		time.Sleep(400 * time.Millisecond)
		return len(s.rc.snapshot()) == n
	}, "node command stream quiescent")
	return s
}

func (s *r6x017Scenario) baseline() int { return len(s.rc.snapshot()) }

func (s *r6x017Scenario) cmdsAfter(baseline int) []cleanupAgentCommand {
	all := s.rc.snapshot()
	if baseline >= len(all) {
		return nil
	}
	return all[baseline:]
}

// lastLimiterLimits 返回 cmds 中最后一条 name 限速器的 limits 载荷
// （AddLimiters 直载 / UpdateLimiters 的 data 载荷同口径）。
func r6x017LastLimiterLimits(cmds []cleanupAgentCommand, name string) ([]string, bool) {
	var last []string
	found := false
	for _, c := range cmds {
		var payload map[string]interface{}
		switch c.Type {
		case "AddLimiters":
			if err := json.Unmarshal(c.Data, &payload); err != nil {
				continue
			}
		case "UpdateLimiters":
			var wrap map[string]interface{}
			if err := json.Unmarshal(c.Data, &wrap); err != nil {
				continue
			}
			data, _ := wrap["data"].(map[string]interface{})
			payload = data
		default:
			continue
		}
		if payload == nil || fmt.Sprint(payload["name"]) != name {
			continue
		}
		rawLimits, _ := payload["limits"].([]interface{})
		limits := make([]string, 0, len(rawLimits))
		for _, l := range rawLimits {
			limits = append(limits, fmt.Sprint(l))
		}
		last = limits
		found = true
	}
	return last, found
}

// lastServiceLimiter 返回 cmds 中 forward 的 tcp 服务最后一次下发的
// limiter 字段（无 limiter 时为 "" 且 found=true 表示服务确被下发）。
func (s *r6x017Scenario) lastServiceLimiter(cmds []cleanupAgentCommand) (string, bool) {
	last := ""
	found := false
	prefix := fmt.Sprintf("%d_1_", s.forwardID)
	for _, c := range cmds {
		if c.Type != "UpdateService" && c.Type != "AddService" {
			continue
		}
		var services []struct {
			Name    string  `json:"name"`
			Limiter *string `json:"limiter"`
		}
		if err := json.Unmarshal(c.Data, &services); err != nil {
			continue
		}
		for _, svc := range services {
			if strings.HasPrefix(svc.Name, prefix) && strings.HasSuffix(svc.Name, "_tcp") {
				if svc.Limiter != nil {
					last = *svc.Limiter
				} else {
					last = ""
				}
				found = true
			}
		}
	}
	return last, found
}

// 引用集全集断言（仓库正本 SpeedLimitAffectedForwardIDs）：直引两列
// （speed_id / ip_speed_id）+ UT 间接引三路全覆盖、双路命中去重、
// 无关引用不入集。
func TestR6X017SpeedLimitAffectedForwardIDsCoversAllReferenceKinds(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r6x017-refs.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	mkForward := func(id int64, userID int64, tunnelID int64, speedID, ipSpeedID any) {
		t.Helper()
		if err := r.DB().Exec(`
			INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx, speed_id, ip_speed_id)
			VALUES(?, ?, 'user', 'fwd', ?, '127.0.0.1:80', 'fifo', 0, 0, 1, 1, 1, 0, ?, ?)
		`, id, userID, tunnelID, speedID, ipSpeedID).Error; err != nil {
			t.Fatalf("insert forward %d: %v", id, err)
		}
	}
	// R=42，旁规则=43。
	mkForward(101, 1, 9, 42, nil)  // speed_id 直引
	mkForward(102, 1, 9, nil, 42)  // ip_speed_id 直引
	mkForward(103, 7, 9, nil, nil) // UT 间接引（user 7 / tunnel 9）
	mkForward(104, 1, 9, 43, nil)  // 无关规则，不入集
	mkForward(105, 7, 9, 42, nil)  // 直引 + UT 间接引双路命中，只计一次
	if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(7, 9, 42, 10, 100, 0, 0, 0, 0, 0, 1)`).Error; err != nil {
		t.Fatalf("insert user_tunnel: %v", err)
	}

	ids, err := r.SpeedLimitAffectedForwardIDs(42)
	if err != nil {
		t.Fatalf("SpeedLimitAffectedForwardIDs: %v", err)
	}
	want := []int64{101, 102, 103, 105}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("R6X-017: affected forward set = %v, want %v (all reference kinds, deduped, sorted)", ids, want)
	}
}

// 变体一（改值，UT 间接引用）：转发无直引、UT 引 R=10 → 改 R 为 1 后
// 节点须收到 R 的新限速载荷（1 Mbps 线）且服务重下发（limiter 仍 R）。
func TestR6X017SpeedLimitUpdateValueResyncsReferencingForwards(t *testing.T) {
	s := r6x017Setup(t, 0, 1)
	if got, found := s.lastServiceLimiter(s.rc.snapshot()); !found || got != fmt.Sprint(s.ruleID) {
		t.Fatalf("setup: initial service limiter must be UT rule %d, got %q found=%v", s.ruleID, got, found)
	}
	baseline := s.baseline()

	res := httptest.NewRecorder()
	s.h.speedLimitUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": s.ruleID, "speed": 1}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("speedLimitUpdate failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}

	fresh := s.cmdsAfter(baseline)
	limits, found := r6x017LastLimiterLimits(fresh, fmt.Sprint(s.ruleID))
	if !found {
		t.Fatalf("R6X-017: no limiter re-dispatch for rule %d after speed update (node config stays at old speed)", s.ruleID)
	}
	want := []string{speedToLimitLine("$", 1)}
	if fmt.Sprint(limits) != fmt.Sprint(want) {
		t.Fatalf("R6X-017: re-dispatched limiter limits = %v, want %v (new speed 1 Mbps)", limits, want)
	}
	if got, found := s.lastServiceLimiter(fresh); !found || got != fmt.Sprint(s.ruleID) {
		t.Fatalf("R6X-017: forward service must be re-dispatched with limiter %d after rule update, got %q found=%v", s.ruleID, got, found)
	}
}

// 变体二（禁用，直引 + UT 回退）：转发直引 R=10、UT 引 F=5 → 禁用 R
// （status=0）后服务 limiter 须回退为 F，且 F 的限速载荷被下发。
func TestR6X017SpeedLimitDisableFallsBackToUserTunnelRule(t *testing.T) {
	s := r6x017Setup(t, 1, 2)
	if got, found := s.lastServiceLimiter(s.rc.snapshot()); !found || got != fmt.Sprint(s.ruleID) {
		t.Fatalf("setup: initial service limiter must be forward rule %d, got %q found=%v", s.ruleID, got, found)
	}
	baseline := s.baseline()

	res := httptest.NewRecorder()
	s.h.speedLimitUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": s.ruleID, "status": 0}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("speedLimitUpdate(disable) failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	if row, err := s.repo.GetSpeedLimit(s.ruleID); err != nil || row == nil || row.Status != 0 {
		t.Fatalf("disable must persist in DB (no rollback), got %+v err=%v", row, err)
	}

	fresh := s.cmdsAfter(baseline)
	if got, found := s.lastServiceLimiter(fresh); !found || got != fmt.Sprint(s.fallbackID) {
		t.Fatalf("R6X-017: after disabling rule %d the service limiter must fall back to UT rule %d, got %q found=%v (no re-dispatch = stale enforcement)", s.ruleID, s.fallbackID, got, found)
	}
	limits, found := r6x017LastLimiterLimits(fresh, fmt.Sprint(s.fallbackID))
	want := []string{speedToLimitLine("$", 5)}
	if !found || fmt.Sprint(limits) != fmt.Sprint(want) {
		t.Fatalf("R6X-017: fallback limiter %d must be dispatched with limits %v, got %v found=%v", s.fallbackID, want, limits, found)
	}
}

// 变体三（删除，直引 + UT 回退）：删除 R 后果与禁用同口径——服务
// limiter 回退 F；DB 中 R 已删（不回滚）。
func TestR6X017SpeedLimitDeleteFallsBackToUserTunnelRule(t *testing.T) {
	s := r6x017Setup(t, 1, 2)
	if got, found := s.lastServiceLimiter(s.rc.snapshot()); !found || got != fmt.Sprint(s.ruleID) {
		t.Fatalf("setup: initial service limiter must be forward rule %d, got %q found=%v", s.ruleID, got, found)
	}
	baseline := s.baseline()

	res := httptest.NewRecorder()
	s.h.speedLimitDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": s.ruleID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("speedLimitDelete failed: code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	if row, err := s.repo.GetSpeedLimit(s.ruleID); err != nil || row != nil {
		t.Fatalf("delete must persist in DB (no rollback), got %+v err=%v", row, err)
	}

	fresh := s.cmdsAfter(baseline)
	if got, found := s.lastServiceLimiter(fresh); !found || got != fmt.Sprint(s.fallbackID) {
		t.Fatalf("R6X-017: after deleting rule %d the service limiter must fall back to UT rule %d, got %q found=%v (no re-dispatch = stale enforcement)", s.ruleID, s.fallbackID, got, found)
	}
	limits, found := r6x017LastLimiterLimits(fresh, fmt.Sprint(s.fallbackID))
	want := []string{speedToLimitLine("$", 5)}
	if !found || fmt.Sprint(limits) != fmt.Sprint(want) {
		t.Fatalf("R6X-017: fallback limiter %d must be dispatched with limits %v, got %v found=%v", s.fallbackID, want, limits, found)
	}
}
