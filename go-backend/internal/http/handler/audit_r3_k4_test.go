package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Round 3 K4 回归门：写面校验补全（B03-001/B03-002/B12-001/B12-002/
// B04-002/B04-003 + 方法守卫普查新增同族面）。

func k4Handler(t *testing.T) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "k4.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "test-secret"), r
}

func k4Call(t *testing.T, fn func(http.ResponseWriter, *http.Request), method string, body interface{}, admin bool) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(method, "/api/v1/test", bytes.NewReader(raw))
	if admin {
		claims := auth.Claims{Sub: "1", RoleID: 0}
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey, claims))
	}
	res := httptest.NewRecorder()
	fn(res, req)
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, res.Body.String())
	}
	return payload.Code, payload.Msg
}

// TestK4MethodGuardCensus 是方法守卫普查清单（lanes/K4/r3/method-census.md）
// 的永久对账门：机械枚举 Register 全部路由，逐 handler 断言存在 POST
// 方法守卫证据（直接校验 / requirePost / 经 body 解析助手或被调用函数
// 传递）。豁免集是封闭清单——读面与 agent 协议面逐个列名；新写面漏
// 守卫、或新路由的 handler 不在豁免集且无守卫证据，本测试即红。
func TestK4MethodGuardCensus(t *testing.T) {
	src, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	routeRe := regexp.MustCompile(`mux\.HandleFunc\("([^"]+)",\s*h\.(\w+)\)`)
	backupRe := regexp.MustCompile(`\{"(/api/v1/backup/[^"]+)",\s*h\.(\w+)\}`)
	type route struct{ path, fn string }
	var routes []route
	for _, m := range routeRe.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, route{m[1], m[2]})
	}
	for _, m := range backupRe.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, route{m[1], m[2]})
	}
	if len(routes) < 150 {
		t.Fatalf("route census implausibly small: %d", len(routes))
	}

	// 提取本包全部非测试源文件中的函数窗口（至下一个顶层 func 为止）。
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	funcBody := map[string]string{}
	funcRe := regexp.MustCompile(`(?m)^func (?:\(h \*Handler\) )?(\w+)\(`)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(data)
		locs := funcRe.FindAllStringSubmatchIndex(text, -1)
		for i, loc := range locs {
			fn := text[loc[2]:loc[3]]
			end := len(text)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			if _, dup := funcBody[fn]; !dup {
				funcBody[fn] = text[loc[0]:end]
			}
		}
	}

	postEvidence := []string{
		"r.Method != http.MethodPost", "requirePost(w, r)",
		"idFromBody(", "asInt64FromBodyKey(", "parseIDsFromBody(",
		"idsFromBody(", "idsFromBodyWithInvalid(", "switch r.Method",
	}
	var hasEvidence func(fn string, depth int) bool
	hasEvidence = func(fn string, depth int) bool {
		body, ok := funcBody[fn]
		if !ok {
			return false
		}
		for _, ev := range postEvidence {
			if strings.Contains(body, ev) {
				return true
			}
		}
		if depth == 0 {
			callRe := regexp.MustCompile(`(?:h\.)?(\w+)\(`)
			for _, m := range callRe.FindAllStringSubmatch(body, -1) {
				if m[1] != fn && hasEvidence(m[1], 1) {
					return true
				}
			}
		}
		return false
	}

	// 封闭豁免集：无 POST 守卫但合法的 handler（读面/静态面/agent
	// 上报协议面）。新增豁免必须逐个点名并说明类别——写面不得入集。
	exempt := map[string]string{
		"systemUpgradeStatus":                "读面（升级状态查询）",
		"openSubscription":                   "读面（公开订阅拉取）",
		"getAnnouncement":                    "读面（公告读取）",
		"errorPage":                          "静态错误页",
		"flowTest":                           "静态探活",
		"flowConfig":                         "agent 上报协议面（secret 鉴权，非管理 API 族）",
		"flowUpload":                         "agent 上报协议面（secret 鉴权，非管理 API 族）",
		"monitorNodeMetricsHandler":          "读面（节点指标）",
		"monitorNodeListHandler":             "读面（节点列表）",
		"monitorTunnelListHandler":           "读面（隧道列表）",
		"monitorTunnelQualityHandler":        "读面（隧道质量）",
		"monitorTunnelMetrics":               "读面（隧道指标）",
		"monitorServiceListHandler":          "读面（监控列表）",
		"monitorServiceLatestResultsHandler": "读面（最新结果）",
		"monitorServiceLimitsHandler":        "读面（监控上限）",
		"monitorServiceResultsHandler":       "读面（结果列表）",
		"monitorPermissionList":              "读面（权限列表）",
		"monitorAccessHandler":               "读面（GET-only 方法校验）",
		"nodeConnlog":                        "读面（节点连接日志查询，GET-only 方法校验）",
		"nodeConnlogReadiness":               "读面（节点连接日志就绪指示，GET-only 方法校验）",
		"backupImportAutoBackup":             "读面（GET-only 方法校验，文件下载）",
		"backupImportAutoBackupStatus":       "读面（GET-only 方法校验）",
	}
	for _, rt := range routes {
		if _, ok := exempt[rt.fn]; ok {
			continue
		}
		if _, ok := funcBody[rt.fn]; !ok {
			t.Errorf("route %s: handler %s not found in package sources", rt.path, rt.fn)
			continue
		}
		if !hasEvidence(rt.fn, 0) {
			t.Errorf("route %s: handler %s has no POST method guard evidence and is not in the exempt census set", rt.path, rt.fn)
		}
	}
}

// TestK4WriteFacesRejectNonPost 逐面行为回归：普查清单内全部补守卫
// 写面在 GET/PUT 下必须回「请求失败」
// 且零落库。
func TestK4WriteFacesRejectNonPost(t *testing.T) {
	h, r := k4Handler(t)
	faces := map[string]func(http.ResponseWriter, *http.Request){
		"userTunnelAssign":         h.userTunnelAssign,
		"userTunnelBatchAssign":    h.userTunnelBatchAssign,
		"userTunnelUpdate":         h.userTunnelUpdate,
		"forwardCreate":            h.forwardCreate,
		"forwardUpdate":            h.forwardUpdate,
		"forwardBatchChangeTunnel": h.forwardBatchChangeTunnel,
		"speedLimitCreate":         h.speedLimitCreate,
		"speedLimitUpdate":         h.speedLimitUpdate,
	}
	for name, fn := range faces {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			code, msg := k4Call(t, fn, method, map[string]interface{}{"name": "GETMADE", "groupId": 1}, true)
			if code == 0 || !strings.Contains(msg, "请求失败") {
				t.Errorf("%s %s: want 请求失败 rejection, got code=%d msg=%q", name, method, code, msg)
			}
		}
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM speed_limit WHERE name = 'GETMADE'`); n != 0 {
		t.Fatalf("GET speed-limit create leaked a row")
	}
	// 对照：POST 正常方法不受守卫误伤（原对照面 groupUserCreate
	// 已随分组三件套移除，改以 speedLimitCreate 同口径顶替）。
	code, msg := k4Call(t, h.speedLimitCreate, http.MethodPost, map[string]interface{}{"name": "POSTOK", "status": 1}, true)
	if code != 0 {
		t.Fatalf("POST speed-limit create must succeed, got code=%d msg=%q", code, msg)
	}
}

// TestK4MonitorCreateUpdateValidation 覆盖 B12-001/B12-002：创建面
// enabled 域外点名拒绝；创建/更新两面 nodeId 负值同口径拒绝。
func TestK4MonitorCreateUpdateValidation(t *testing.T) {
	h, r := k4Handler(t)
	enabled := 5
	code, msg := k4Call(t, h.monitorServiceCreate, http.MethodPost, map[string]interface{}{
		"name": "t-pub", "type": "tcp", "target": "203.0.113.7:80", "enabled": enabled,
	}, true)
	if code == 0 || !strings.Contains(msg, "启用状态只能为 0 或 1") {
		t.Fatalf("create enabled=5: want domain rejection, got code=%d msg=%q", code, msg)
	}
	code, msg = k4Call(t, h.monitorServiceCreate, http.MethodPost, map[string]interface{}{
		"name": "t-neg", "type": "tcp", "target": "203.0.113.8:80", "nodeId": -5,
	}, true)
	if code == 0 || !strings.Contains(msg, "无效的节点ID") {
		t.Fatalf("create nodeId=-5: want rejection, got code=%d msg=%q", code, msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM service_monitor`); n != 0 {
		t.Fatalf("rejected creates leaked rows: %d", n)
	}
	code, msg = k4Call(t, h.monitorServiceCreate, http.MethodPost, map[string]interface{}{
		"name": "t-ok", "type": "tcp", "target": "203.0.113.9:80", "enabled": 1,
	}, true)
	if code != 0 {
		t.Fatalf("control create failed: code=%d msg=%q", code, msg)
	}
	id := mustQueryInt(t, r, `SELECT id FROM service_monitor WHERE name = 't-ok'`)
	code, msg = k4Call(t, h.monitorServiceUpdate, http.MethodPost, map[string]interface{}{
		"id": id, "nodeId": -9,
	}, true)
	if code == 0 || !strings.Contains(msg, "无效的节点ID") {
		t.Fatalf("update nodeId=-9: want rejection, got code=%d msg=%q", code, msg)
	}
	var m model.ServiceMonitor
	if err := r.DB().Where("id = ?", id).First(&m).Error; err != nil {
		t.Fatal(err)
	}
	if m.NodeID != 0 {
		t.Fatalf("rejected update changed node_id to %d", m.NodeID)
	}
}

// TestK4NodeCreateUpdateGates 覆盖 B04-002/B04-003：创建面数值字段
// fail-closed、更新面 name/serverIp 非空门（未提交保值语义不变）。
func TestK4NodeCreateUpdateGates(t *testing.T) {
	h, r := k4Handler(t)
	code, msg := k4Call(t, h.nodeCreate, http.MethodPost, map[string]interface{}{
		"name": "garbage", "serverIp": "g.example.com", "expiryTime": "abc",
	}, true)
	if code == 0 || !strings.Contains(msg, "expiryTime 参数无效") {
		t.Fatalf("create expiryTime=abc: want fail-closed rejection, got code=%d msg=%q", code, msg)
	}
	code, msg = k4Call(t, h.nodeCreate, http.MethodPost, map[string]interface{}{
		"name": "garbage", "serverIp": "g.example.com", "http": "xyz",
	}, true)
	if code == 0 || !strings.Contains(msg, "http 参数无效") {
		t.Fatalf("create http=xyz: want fail-closed rejection, got code=%d msg=%q", code, msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name = 'garbage'`); n != 0 {
		t.Fatalf("rejected creates leaked node rows")
	}
	code, msg = k4Call(t, h.nodeCreate, http.MethodPost, map[string]interface{}{
		"name": "n1", "serverIp": "203.0.113.10",
	}, true)
	if code != 0 {
		t.Fatalf("control create failed: code=%d msg=%q", code, msg)
	}
	id := mustQueryInt(t, r, `SELECT id FROM node WHERE name = 'n1'`)
	for _, body := range []map[string]interface{}{
		{"id": id, "name": ""},
		{"id": id, "serverIp": ""},
	} {
		code, msg = k4Call(t, h.nodeUpdate, http.MethodPost, body, true)
		if code == 0 || !strings.Contains(msg, "节点名称和地址不能为空") {
			t.Fatalf("update %v: want non-empty rejection, got code=%d msg=%q", body, code, msg)
		}
	}
	var node model.Node
	if err := r.DB().Where("id = ?", id).First(&node).Error; err != nil {
		t.Fatal(err)
	}
	if node.Name != "n1" || node.ServerIP != "203.0.113.10" {
		t.Fatalf("rejected updates mutated node: name=%q server_ip=%q", node.Name, node.ServerIP)
	}
	// 对照：部分更新只改名，未提交字段保值。
	code, msg = k4Call(t, h.nodeUpdate, http.MethodPost, map[string]interface{}{"id": id, "name": "n1b"}, true)
	if code != 0 {
		t.Fatalf("control partial update failed: code=%d msg=%q", code, msg)
	}
	if err := r.DB().Where("id = ?", id).First(&node).Error; err != nil {
		t.Fatal(err)
	}
	if node.Name != "n1b" || node.ServerIP != "203.0.113.10" {
		t.Fatalf("partial update broke keep-value semantics: name=%q server_ip=%q", node.Name, node.ServerIP)
	}
}
