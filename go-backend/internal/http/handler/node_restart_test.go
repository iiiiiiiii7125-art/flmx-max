package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"go-backend/internal/store/repo"
)

func TestValidateRestartSchedule(t *testing.T) {
	valid := []struct {
		enabled  int
		mode     string
		time     string
		jitter   int
		interval int
	}{
		{1, "daily", "04:00", 30, 60},
		{0, "daily", "00:00", 0, 60},
		{1, "daily", "23:59", 720, 60},
		// 间隔模式：边界值与典型值；间隔在 daily 模式不判（此处
		// 给 0 亦合法，daily 不消费该字段）。
		{1, "interval", "04:00", 30, 15},
		{1, "interval", "04:00", 30, 120},
		{1, "interval", "04:00", 0, 10080},
		{1, "daily", "04:00", 30, 0},
	}
	for _, c := range valid {
		if err := validateRestartSchedule(c.enabled, c.mode, c.time, c.jitter, c.interval); err != nil {
			t.Fatalf("expected valid %+v, got %v", c, err)
		}
	}
	invalid := []struct {
		enabled  int
		mode     string
		time     string
		jitter   int
		interval int
	}{
		{2, "daily", "04:00", 30, 60},
		{-1, "daily", "04:00", 30, 60},
		{1, "daily", "24:00", 30, 60},
		{1, "daily", "4:00", 30, 60},
		{1, "daily", "04:60", 30, 60},
		{1, "daily", "", 30, 60},
		{1, "daily", "04:00", -1, 60},
		{1, "daily", "04:00", 721, 60},
		{1, "weekly", "04:00", 30, 60},   // 未知模式
		{1, "", "04:00", 30, 60},         // 空模式须先经 handler 归一，校验面不放行
		{1, "interval", "04:00", 30, 14}, // 间隔低于下限
		{1, "interval", "04:00", 30, 10081},
		{1, "interval", "04:00", 30, 0},
		{1, "interval", "04:00", 721, 120}, // 间隔模式下抖动边界照旧
	}
	for _, c := range invalid {
		if err := validateRestartSchedule(c.enabled, c.mode, c.time, c.jitter, c.interval); err == nil {
			t.Fatalf("expected invalid %+v to be rejected", c)
		}
	}
}

func TestRestartSchedulePayloadShape(t *testing.T) {
	// daily 模式载荷必须与引入模式前逐字一致：恰好三键、无
	// mode/intervalMinutes——旧 agent 读到的就是它认识的形状。
	p := restartSchedulePayload(1, "daily", "04:00", 30, 60)
	if p["enabled"] != true || p["time"] != "04:00" || p["jitterMinutes"] != 30 {
		t.Fatalf("payload shape drifted: %v", p)
	}
	if len(p) != 3 {
		t.Fatalf("daily payload must carry exactly the pre-mode keys: %v", p)
	}
	// 存量空 mode 按 daily 读，载荷同样无新键。
	p = restartSchedulePayload(1, "", "04:00", 30, 60)
	if len(p) != 3 {
		t.Fatalf("empty mode must render the daily payload: %v", p)
	}
	p = restartSchedulePayload(0, "daily", "04:00", 30, 60)
	if p["enabled"] != false {
		t.Fatalf("disabled must render enabled=false: %v", p)
	}
	// interval 模式在三键之外带 mode＋intervalMinutes。
	p = restartSchedulePayload(1, "interval", "04:00", 30, 120)
	if p["mode"] != "interval" || p["intervalMinutes"] != 120 {
		t.Fatalf("interval payload must carry mode+intervalMinutes: %v", p)
	}
	if p["enabled"] != true || p["time"] != "04:00" || p["jitterMinutes"] != 30 {
		t.Fatalf("interval payload must keep the base keys: %v", p)
	}
}

func seedRestartNode(t *testing.T, r *repo.Repository, id int64, forwardMode string) {
	t.Helper()
	err := r.DB().Exec(
		`INSERT INTO node(id, name, secret, server_ip, port, created_time, status, forward_mode)
		 VALUES(?, ?, 'secret', '192.0.2.1', '1000-65535', 1000, 0, ?)`,
		id, "restart-test-node", forwardMode,
	).Error
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}
}

func postRestartSchedule(t *testing.T, h *Handler, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/restart-schedule", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	h.nodeRestartScheduleSet(rec, req)
	var env map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, rec.Body.String())
	}
	return env
}

func TestNodeRestartScheduleSetOfflineNodePersists(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "restart.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()
	seedRestartNode(t, r, 41, "agent")
	h := New(r, "test-secret")

	// Offline node: the setting must persist and report pushed=false
	// (the reconnect hook re-pushes; see pushNodeRestartSchedule).
	env := postRestartSchedule(t, h, map[string]interface{}{
		"id": 41, "enabled": 1, "time": "03:30", "jitterMinutes": 45,
	})
	if env["code"].(float64) != 0 {
		t.Fatalf("expected success envelope, got %v", env)
	}
	data := env["data"].(map[string]interface{})
	if data["pushed"] != false {
		t.Fatalf("offline node must report pushed=false, got %v", data)
	}
	node, err := r.GetNodeByID(41)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.RestartEnabled != 1 || node.RestartTime != "03:30" || node.RestartJitterMinutes != 45 {
		t.Fatalf("setting not persisted: %+v", node)
	}

	// Turning it off persists too (default-off is a stored state,
	// not an absence the agent could misread).
	env = postRestartSchedule(t, h, map[string]interface{}{
		"id": 41, "enabled": 0, "time": "03:30", "jitterMinutes": 45,
	})
	if env["code"].(float64) != 0 {
		t.Fatalf("disable must succeed, got %v", env)
	}
	node, _ = r.GetNodeByID(41)
	if node.RestartEnabled != 0 {
		t.Fatalf("disable not persisted: %+v", node)
	}
}

func TestNodeRestartScheduleSetRejects(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "restart-reject.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()
	seedRestartNode(t, r, 42, "agent")
	seedRestartNode(t, r, 44, "nftables")
	h := New(r, "test-secret")

	cases := []map[string]interface{}{
		{"id": 42, "enabled": 1, "time": "24:00", "jitterMinutes": 30},                                               // bad time
		{"id": 42, "enabled": 1, "time": "04:00", "jitterMinutes": 721},                                              // jitter too large
		{"id": 42, "enabled": 2, "time": "04:00", "jitterMinutes": 30},                                               // bad switch
		{"id": 44, "enabled": 1, "time": "04:00", "jitterMinutes": 30},                                               // nftables node
		{"id": 999, "enabled": 1, "time": "04:00", "jitterMinutes": 30},                                              // missing node
		{"id": 42, "enabled": 1, "time": "04:00", "jitterMinutes": 30, "mode": "weekly"},                             // unknown mode
		{"id": 42, "enabled": 1, "time": "04:00", "jitterMinutes": 30, "mode": "interval", "intervalMinutes": 10},    // interval below floor
		{"id": 42, "enabled": 1, "time": "04:00", "jitterMinutes": 30, "mode": "interval", "intervalMinutes": 10081}, // interval above ceiling
	}
	for i, body := range cases {
		env := postRestartSchedule(t, h, body)
		if env["code"].(float64) == 0 {
			t.Fatalf("case %d must be rejected, got success: %v", i, env)
		}
	}
	// Nothing may have been written by the rejected calls.
	node, _ := r.GetNodeByID(42)
	if node.RestartEnabled != 0 {
		t.Fatalf("rejected writes must not persist: %+v", node)
	}
}

// fix/restart-interval：间隔模式设置在离线节点上同样只落库待
// 补发，模式与间隔分钟与开关一同持久化、回执回显。
func TestNodeRestartScheduleSetIntervalPersists(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "restart-interval.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()
	seedRestartNode(t, r, 43, "agent")
	h := New(r, "test-secret")

	env := postRestartSchedule(t, h, map[string]interface{}{
		"id": 43, "enabled": 1, "time": "04:00", "jitterMinutes": 10,
		"mode": "interval", "intervalMinutes": 120,
	})
	if env["code"].(float64) != 0 {
		t.Fatalf("expected success envelope, got %v", env)
	}
	data := env["data"].(map[string]interface{})
	if data["mode"] != "interval" || data["intervalMinutes"] != float64(120) {
		t.Fatalf("receipt must echo mode+intervalMinutes, got %v", data)
	}
	node, err := r.GetNodeByID(43)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.RestartMode != "interval" || node.RestartIntervalMinutes != 120 {
		t.Fatalf("interval setting not persisted: mode=%q interval=%d", node.RestartMode, node.RestartIntervalMinutes)
	}
	// 列表行透出新两字段（前端弹窗的回填消费面）。
	items, err := r.ListNodes()
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	for _, it := range items {
		if id, _ := it["id"].(int64); id == 43 {
			if it["restartMode"] != "interval" || it["restartIntervalMinutes"] != 120 {
				t.Fatalf("list row must carry mode+interval: %v", it)
			}
		}
	}
	// 切回 daily 亦落库（模式是存量状态的一部分，不是缺省）。
	env = postRestartSchedule(t, h, map[string]interface{}{
		"id": 43, "enabled": 1, "time": "05:00", "jitterMinutes": 10, "mode": "daily",
	})
	if env["code"].(float64) != 0 {
		t.Fatalf("switch back to daily must succeed, got %v", env)
	}
	node, _ = r.GetNodeByID(43)
	if node.RestartMode != "daily" {
		t.Fatalf("mode switch not persisted: %q", node.RestartMode)
	}
}

// W8-R-C601：下发结果持久化——离线保存记 pending、在线确认记
// applied，刷新列表行可见；失败只在当次回执带 pushError 的旧
// 形态不再成立。
func TestNodeRestartScheduleSetOfflineRecordsPending(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "restart-pending.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()
	seedRestartNode(t, r, 41, "agent")
	h := New(r, "test-secret")

	env := postRestartSchedule(t, h, map[string]interface{}{
		"id": 41, "enabled": 1, "time": "03:30", "jitterMinutes": 45,
	})
	if env["code"].(float64) != 0 {
		t.Fatalf("expected success envelope, got %v", env)
	}
	data := env["data"].(map[string]interface{})
	if data["pushStatus"] != restartPushPending {
		t.Fatalf("offline save must report pushStatus=pending, got %v", data)
	}
	node, err := r.GetNodeByID(41)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.RestartPushStatus != restartPushPending {
		t.Fatalf("push status not persisted: %q", node.RestartPushStatus)
	}
	if node.RestartPushedAt <= 0 {
		t.Fatalf("push attempt time not persisted: %d", node.RestartPushedAt)
	}
	// 列表行透出三字段（前端路 5 呈现半的消费面）。
	items, err := r.ListNodes()
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	var found map[string]interface{}
	for _, it := range items {
		if id, _ := it["id"].(int64); id == 41 {
			found = it
		}
	}
	if found == nil {
		t.Fatalf("node 41 missing from list")
	}
	if found["restartPushStatus"] != restartPushPending {
		t.Fatalf("list row push status = %v, want pending", found["restartPushStatus"])
	}
	if found["restartPushedAt"] == int64(0) || found["restartPushedAt"] == nil {
		t.Fatalf("list row push time missing: %v", found["restartPushedAt"])
	}
}

// W8-R-C601：在线节点保存后 agent 确认，下发结果记 applied 且
// SetRestartSchedule 命令确已发出。
func TestNodeRestartScheduleSetOnlineRecordsApplied(t *testing.T) {
	a := newCleanupAgent(t)
	env := postRestartSchedule(t, a.h, map[string]interface{}{
		"id": 1, "enabled": 1, "time": "03:30", "jitterMinutes": 45,
	})
	if env["code"].(float64) != 0 {
		t.Fatalf("expected success envelope, got %v", env)
	}
	data := env["data"].(map[string]interface{})
	if data["pushed"] != true || data["pushStatus"] != restartPushApplied {
		t.Fatalf("online save must report pushed=true pushStatus=applied, got %v", data)
	}
	if cmds := a.commandsOfType("SetRestartSchedule"); len(cmds) == 0 {
		t.Fatalf("SetRestartSchedule command was never sent")
	}
	node, err := a.h.repo.GetNodeByID(1)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.RestartPushStatus != restartPushApplied || node.RestartPushError != "" || node.RestartPushedAt <= 0 {
		t.Fatalf("applied result not persisted: status=%q err=%q at=%d", node.RestartPushStatus, node.RestartPushError, node.RestartPushedAt)
	}
}

// W8-R-C601：确定性失败判据——版本过旧／无 systemd 时重连亦然，
// 不得承诺自动补发；瞬时失败（超时／离线）不在此列。
func TestRestartPushDeterministicFailure(t *testing.T) {
	if !restartPushDeterministicFailure("未知命令类型: SetRestartSchedule") {
		t.Fatalf("unknown command must be deterministic")
	}
	if !restartPushDeterministicFailure("节点无 systemd，定时重启无法持久化（容器形态不支持）") {
		t.Fatalf("no-systemd must be deterministic")
	}
	for _, transient := range []string{"", "节点离线", "命令超时", "connection reset"} {
		if restartPushDeterministicFailure(transient) {
			t.Fatalf("transient failure %q must not be deterministic", transient)
		}
	}
}
