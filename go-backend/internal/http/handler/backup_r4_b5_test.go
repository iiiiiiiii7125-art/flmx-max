package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R4 批 5 · R4X-009（E4-02）：backupImport 尾钩子的内核 reconcile
// 触发条件旧口径只认 inbounds 段——未选 inbounds 的部分导入即使
// 改写了白名单/RemoteAddr 输入集（nodes 节点地址 / tunnels 的
// Type 与入口·出口集 / forwards 的隧道归属），在线节点上的物化
// allow 也不重推、与导入后 DB 脱节驻留。本批按输入集全集表放宽
// 触发条件至任一物化输入集段（nodes/tunnels/forwards/inbounds，
// 计数以实际落库行计），落点仍是 handler 尾钩子的单个 goroutine：
// 全量导入多段同时命中也只触发一轮 reconcile（计数断言钉死）。
//
// 断言口径与 R1-B3/R3-B4 一致：落地 mock agent 收到新的
// KernelApply 且 allow 集随输入集变化；修复前三段部分导入均无
// 任何新下发（先红）。

// settleR4B5Baseline waits until the landing agent's command
// counts are stable (reconnect reconcile drained) and returns the
// KernelApply count as the baseline for newAllowsSince.
func settleR4B5Baseline(t *testing.T, s *r1b3WLScenario) int {
	t.Helper()
	lastApply, lastStatus := -1, -1
	stable := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := len(s.landingRC.kernelApplyAllows())
		sc := r4b5KernelStatusCount(s)
		if n == lastApply && sc == lastStatus && n > 0 {
			stable++
			if stable >= 3 {
				return n
			}
		} else {
			stable = 0
			lastApply, lastStatus = n, sc
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("kernel command counts never settled, applies=%d statuses=%d", lastApply, lastStatus)
	return -1
}

// r4b5KernelStatusCount counts KernelStatus commands the landing
// mock agent received. reconcileNodeKernels ends each round with
// exactly one pollNodeKernels → one KernelStatus per kernel node,
// so this count measures reconcile *rounds* directly. (Raw
// KernelApply counts cannot: the poll's drift branch re-applies
// once more against this mock's empty status reply — an artifact
// of the fixture, present in the pre-existing reconnect round
// too, and identical before/after this fix.)
func r4b5KernelStatusCount(s *r1b3WLScenario) int {
	n := 0
	for _, c := range s.landingRC.snapshot() {
		if c.Type == "KernelStatus" {
			n++
		}
	}
	return n
}

func postR4B5Import(t *testing.T, s *r1b3WLScenario, types []string, data *model.BackupData) string {
	t.Helper()
	body := struct {
		Types []string `json:"types"`
		*model.BackupData
	}{Types: types, BackupData: data}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal import body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	s.h.backupImport(rec, req)
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("backupImport types=%v: code=%d err=%v body=%s", types, env.Code, err, rec.Body.String())
	}
	return rec.Body.String()
}

// waitR4B5Allow polls until the last KernelApply allow pushed after
// baseline satisfies want (and not unwanted), or fails the test.
func waitR4B5Allow(t *testing.T, s *r1b3WLScenario, baseline int, want, unwanted, what string) {
	t.Helper()
	waitForCondition(t, 8*time.Second, func() bool {
		fresh := s.newAllowsSince(baseline)
		if len(fresh) == 0 {
			return false
		}
		last := fresh[len(fresh)-1]
		if !strings.Contains(last, want) {
			return false
		}
		return unwanted == "" || !strings.Contains(last, unwanted)
	}, what)
}

// ① nodes-only 部分导入：出口节点地址变更 → 落地 allow 换新地址。
func TestR4B5BackupImportNodesOnlyTriggersReconcile(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	baseline := settleR4B5Baseline(t, s)
	initial := s.landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.2/32") {
		t.Fatalf("setup: initial allow must contain exit A, got %v", initial)
	}

	data, err := s.repo.ExportPartial([]string{"nodes"})
	if err != nil {
		t.Fatalf("export nodes: %v", err)
	}
	mutated := false
	for i := range data.Nodes {
		if data.Nodes[i].ID == s.exitAID {
			data.Nodes[i].ServerIP = "198.51.100.99"
			data.Nodes[i].ServerIPv4 = "198.51.100.99"
			mutated = true
		}
	}
	if !mutated {
		t.Fatal("setup: exit node missing from nodes export")
	}
	body := postR4B5Import(t, s, []string{"nodes"}, data)
	if !strings.Contains(body, `"nodesImported":4`) {
		t.Fatalf("nodes segment must report 4 imported rows, body=%s", body)
	}

	waitR4B5Allow(t, s, baseline, "198.51.100.99/32", "198.51.100.2/32",
		"R4X-009: nodes-only partial import must trigger kernel reconcile with the new node address")
}

// ② tunnels-only 部分导入：隧道出口集 A→B → 落地 allow 换出口地址。
func TestR4B5BackupImportTunnelsOnlyTriggersReconcile(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	baseline := settleR4B5Baseline(t, s)
	initial := s.landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.2/32") {
		t.Fatalf("setup: initial allow must contain exit A, got %v", initial)
	}

	data, err := s.repo.ExportPartial([]string{"tunnels"})
	if err != nil {
		t.Fatalf("export tunnels: %v", err)
	}
	mutated := false
	for i := range data.Tunnels {
		if data.Tunnels[i].ID != s.tunnelID {
			continue
		}
		for j := range data.Tunnels[i].ChainTunnels {
			if data.Tunnels[i].ChainTunnels[j].NodeID == s.exitAID {
				data.Tunnels[i].ChainTunnels[j].NodeID = s.exitBID
				mutated = true
			}
		}
	}
	if !mutated {
		t.Fatal("setup: exit chain row missing from tunnels export")
	}
	body := postR4B5Import(t, s, []string{"tunnels"}, data)
	if !strings.Contains(body, `"tunnelsImported":1`) {
		t.Fatalf("tunnels segment must report 1 imported row, body=%s", body)
	}

	waitR4B5Allow(t, s, baseline, "198.51.100.3/32", "198.51.100.2/32",
		"R4X-009: tunnels-only partial import must trigger kernel reconcile with the new exit set")
}

// ③ forwards-only 部分导入：转发改归属到以落地为出口的隧道 →
// 落地 allow 加入落地自身地址、收回旧出口地址。
func TestR4B5BackupImportForwardsOnlyTriggersReconcile(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	dstTunnelID := seedRelayTargetTunnel(t, s.repo, "wl-tunnel-landing", s.frontID, 30002, s.landingID)
	baseline := settleR4B5Baseline(t, s)
	initial := s.landingRC.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.2/32") {
		t.Fatalf("setup: initial allow must contain exit A, got %v", initial)
	}

	data, err := s.repo.ExportPartial([]string{"forwards"})
	if err != nil {
		t.Fatalf("export forwards: %v", err)
	}
	mutated := false
	for i := range data.Forwards {
		if data.Forwards[i].ID == s.forwardID {
			data.Forwards[i].TunnelID = dstTunnelID
			mutated = true
		}
	}
	if !mutated {
		t.Fatal("setup: forward missing from forwards export")
	}
	body := postR4B5Import(t, s, []string{"forwards"}, data)
	if !strings.Contains(body, `"forwardsImported":1`) {
		t.Fatalf("forwards segment must report 1 imported row, body=%s", body)
	}
	if fwd, err := s.h.getForwardRecord(s.forwardID); err != nil || fwd == nil || fwd.TunnelID != dstTunnelID {
		t.Fatalf("setup: forward must belong to the landing-exit tunnel after import, got %+v err=%v", fwd, err)
	}

	waitR4B5Allow(t, s, baseline, "203.0.113.10/32", "198.51.100.2/32",
		"R4X-009: forwards-only partial import must trigger kernel reconcile with the new tunnel ownership")
}

// ④ 全量导入（多段同时命中 + 节点地址变更）：只触发一轮
// reconcile——计数断言以每轮 reconcile 恰发一次的 KernelStatus
// 轮询计轮次（防按段各起 goroutine 的重复触发实现；原始
// KernelApply 计数受夹具 drift 补推干扰，见 r4b5KernelStatusCount）。
func TestR4B5BackupImportFullImportTriggersReconcileOnce(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	baseline := settleR4B5Baseline(t, s)
	statusBaseline := r4b5KernelStatusCount(s)

	data, err := s.repo.ExportAll()
	if err != nil {
		t.Fatalf("export all: %v", err)
	}
	for i := range data.Nodes {
		if data.Nodes[i].ID == s.exitAID {
			data.Nodes[i].ServerIP = "198.51.100.99"
			data.Nodes[i].ServerIPv4 = "198.51.100.99"
		}
	}
	allTypes := []string{"users", "nodes", "tunnels", "forwards", "userTunnels", "speedLimits",
		"tunnelGroups", "userGroups", "permissions", "configs", "inbounds", "userInbounds",
		"announcements", "monitorPermissions", "userSubscriptions", "serviceMonitors"}
	postR4B5Import(t, s, allTypes, data)

	waitR4B5Allow(t, s, baseline, "198.51.100.99/32", "198.51.100.2/32",
		"full import must trigger kernel reconcile with the new node address")
	// 计数断言：等待窗口内不得出现第二轮 reconcile。
	time.Sleep(1500 * time.Millisecond)
	if rounds := r4b5KernelStatusCount(s) - statusBaseline; rounds != 1 {
		t.Fatalf("R4X-009: full import must trigger reconcile exactly once, got %d rounds (KernelStatus polls)", rounds)
	}
}

// ⑤ 防过修：非输入集段（users-only）导入不触发 reconcile——
// 触发面按输入集全集表逐段定，不得退化为无条件触发。
func TestR4B5BackupImportUsersOnlyDoesNotTriggerReconcile(t *testing.T) {
	s := setupR1B3Whitelist(t, 2)
	baseline := settleR4B5Baseline(t, s)

	data, err := s.repo.ExportPartial([]string{"users"})
	if err != nil {
		t.Fatalf("export users: %v", err)
	}
	if len(data.Users) == 0 {
		t.Fatal("setup: users export must not be empty")
	}
	body := postR4B5Import(t, s, []string{"users"}, data)
	if !strings.Contains(body, `"usersImported":`) {
		t.Fatalf("users segment must report imported rows, body=%s", body)
	}

	time.Sleep(1200 * time.Millisecond)
	if fresh := s.newAllowsSince(baseline); len(fresh) != 0 {
		t.Fatalf("anti-overfix: users-only import must not trigger kernel reconcile, got %v", fresh)
	}
}
