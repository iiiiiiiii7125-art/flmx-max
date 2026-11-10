package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// C1-02 coverage closure (LEDGER 族八)：全量 26 条零口径路由中，
// 11 条实为 handler 方法值直调（h.<method> 作参数传入测试辅助
// 函数，审计的「直调须带括号」口径漏计）；剩余 15 条确为三口径
// 全零，本文件逐条补行为测试（入口守卫＋可离线断言的差异点；
// 需真 agent/外网的尾部在各测试注释中点名，不冒充已覆盖）。
// 另补 /error（errorPage）：此前仅源码字面量断言引用。

func c102Handler(t *testing.T) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "c102.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "unit-test-secret"), r
}

// c102Call 直接调 handler（无 claims）；返回 HTTP 状态与信封。
func c102Call(t *testing.T, fn func(http.ResponseWriter, *http.Request), method, body string) (int, envelope) {
	t.Helper()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec.Code, decodeEnvelope(t, rec)
}

// c102CallAs 以 claims 上下文调 handler（userID/roleID 走
// userRoleFromRequest/userIDFromRequest 的同一口径）。
func c102CallAs(t *testing.T, fn func(http.ResponseWriter, *http.Request), method, body string, userID int64, roleID int) (int, envelope) {
	t.Helper()
	rec := httptest.NewRecorder()
	fn(rec, claimsCtxReq(method, "/", "", body, userID, roleID))
	return rec.Code, decodeEnvelope(t, rec)
}

func c102SeedNode(t *testing.T, r *repo.Repository, now int64) *model.Node {
	t.Helper()
	n := &model.Node{Name: "c102-node", Secret: "s", ServerIP: "10.0.0.1", Port: "22", Status: 1, CreatedTime: now}
	if err := r.DB().Create(n).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return n
}

func c102SeedKernel(t *testing.T, r *repo.Repository, nodeID int64, now int64) *model.NodeKernel {
	t.Helper()
	k := &model.NodeKernel{NodeID: nodeID, KernelType: "singbox", Version: "1.0", InstalledVersion: "1.0", Status: "running", ConfigHash: "h", AppliedHash: "h", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeKernel(k); err != nil {
		t.Fatalf("seed kernel: %v", err)
	}
	return k
}

func c102SeedInbound(t *testing.T, r *repo.Repository, kernelID, nodeID int64, name string, port int, companionOf int64) *model.NodeInbound {
	t.Helper()
	in := &model.NodeInbound{KernelID: kernelID, NodeID: nodeID, Name: name, Protocol: "vless", Port: port, Enabled: 1, CompanionOf: companionOf}
	if err := r.DB().Create(in).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	return in
}

// TestC102KernelList covers POST /api/v1/kernel/list: view 含 Synced、
// InboundCount（计量伴生入站不计入）与节点名/在线态；GET 被方法守卫拒。
func TestC102KernelList(t *testing.T) {
	h, r := c102Handler(t)
	now := time.Now().UnixMilli()
	n := c102SeedNode(t, r, now)
	k := c102SeedKernel(t, r, n.ID, now)
	c102SeedInbound(t, r, k.ID, n.ID, "c102-in", 10001, 0)
	origin := c102SeedInbound(t, r, k.ID, n.ID, "c102-origin", 10002, 0)
	c102SeedInbound(t, r, k.ID, n.ID, "c102-companion", 10003, origin.ID)

	_, env := c102Call(t, h.kernelList, http.MethodPost, `{"nodeId":`+strconv.FormatInt(n.ID, 10)+`}`)
	if env.Code != 0 {
		t.Fatalf("kernelList code=%d msg=%q", env.Code, env.Msg)
	}
	var views []map[string]interface{}
	if err := json.Unmarshal(env.Data, &views); err != nil {
		t.Fatalf("decode views: %v (data=%s)", err, env.Data)
	}
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1: %s", len(views), env.Data)
	}
	v := views[0]
	if v["synced"] != true {
		t.Errorf("synced = %v, want true (ConfigHash == AppliedHash)", v["synced"])
	}
	if v["inboundCount"] != float64(2) {
		t.Errorf("inboundCount = %v, want 2 (companion excluded)", v["inboundCount"])
	}
	if v["nodeName"] != "c102-node" || v["nodeOnline"] != true {
		t.Errorf("node view = %v, want name c102-node online true", v)
	}
	if _, env := c102Call(t, h.kernelList, http.MethodGet, ""); env.Code == 0 {
		t.Errorf("GET kernelList must fail the method guard, got code 0")
	}
}

func int64ToStr(v int64) string {
	return strconv.FormatInt(v, 10)
}

// TestC102KernelLogs covers POST /api/v1/kernel/logs 的前置分支：
// id 不存在 →「内核不存在」；GET/坏 JSON 被守卫拒。取日志本身
// 需真 agent（sendNodeCommandWithTimeout），沙箱不覆、由 LIVE 真机格验。
func TestC102KernelLogs(t *testing.T) {
	h, _ := c102Handler(t)
	_, env := c102Call(t, h.kernelLogs, http.MethodPost, `{"id":99999}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "内核不存在") {
		t.Errorf("missing kernel: code=%d msg=%q, want 内核不存在", env.Code, env.Msg)
	}
	if _, env := c102Call(t, h.kernelLogs, http.MethodGet, ""); env.Code == 0 {
		t.Errorf("GET kernelLogs must fail the method guard, got code 0")
	}
	if _, env := c102Call(t, h.kernelLogs, http.MethodPost, `{bad`); env.Code == 0 {
		t.Errorf("bad JSON kernelLogs must fail, got code 0")
	}
}

// TestC102KernelVersions covers POST /api/v1/kernel/versions：未知
// 内核 →「未知内核」；realm 走静态 Versions 表（无外网）；GET 被拒。
func TestC102KernelVersions(t *testing.T) {
	h, _ := c102Handler(t)
	_, env := c102Call(t, h.kernelVersions, http.MethodPost, `{"kernelType":"realm"}`)
	if env.Code != 0 {
		t.Fatalf("realm versions code=%d msg=%q", env.Code, env.Msg)
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("decode versions: %v (data=%s)", err, env.Data)
	}
	found := false
	for _, it := range items {
		if it["version"] == kernel.RealmVersion {
			found = true
		}
	}
	if !found {
		t.Errorf("realm versions %s do not contain %q", env.Data, kernel.RealmVersion)
	}
	_, env = c102Call(t, h.kernelVersions, http.MethodPost, `{"kernelType":"no-such-kernel"}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "未知内核") {
		t.Errorf("unknown kernel: code=%d msg=%q, want 未知内核", env.Code, env.Msg)
	}
	if _, env := c102Call(t, h.kernelVersions, http.MethodGet, ""); env.Code == 0 {
		t.Errorf("GET kernelVersions must fail the method guard, got code 0")
	}
}

// TestC102InboundRelayDelete covers POST /api/v1/inbound/relay/delete：
// Managed=0 的中转直删行（无转发可拆、白名单刷新因入站不存在而
// 空转）；再删 →「中转不存在」；id 非法被拒。
func TestC102InboundRelayDelete(t *testing.T) {
	h, r := c102Handler(t)
	now := time.Now().UnixMilli()
	rel := &model.NodeInboundRelay{InboundID: 99999, ForwardID: 0, Name: "c102-relay", Managed: 0, CreatedTime: now}
	if err := r.DB().Create(rel).Error; err != nil {
		t.Fatalf("seed relay: %v", err)
	}
	_, env := c102Call(t, h.inboundRelayDelete, http.MethodPost, `{"id":`+int64ToStr(rel.ID)+`}`)
	if env.Code != 0 {
		t.Fatalf("delete code=%d msg=%q", env.Code, env.Msg)
	}
	if got, err := r.GetInboundRelay(rel.ID); err == nil && got != nil {
		t.Errorf("relay row still present after delete")
	}
	_, env = c102Call(t, h.inboundRelayDelete, http.MethodPost, `{"id":`+int64ToStr(rel.ID)+`}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "中转不存在") {
		t.Errorf("re-delete: code=%d msg=%q, want 中转不存在", env.Code, env.Msg)
	}
	if _, env := c102Call(t, h.inboundRelayDelete, http.MethodPost, `{"id":0}`); env.Code == 0 {
		t.Errorf("id 0 delete must fail, got code 0")
	}
}

// TestC102NodeCheckStatus covers POST /api/v1/node/check-status：
// 直通 ListNodes；GET 被方法守卫拒。
func TestC102NodeCheckStatus(t *testing.T) {
	h, r := c102Handler(t)
	now := time.Now().UnixMilli()
	c102SeedNode(t, r, now)
	_, env := c102Call(t, h.nodeCheckStatus, http.MethodPost, `{}`)
	if env.Code != 0 {
		t.Fatalf("check-status code=%d msg=%q", env.Code, env.Msg)
	}
	var nodes []map[string]interface{}
	if err := json.Unmarshal(env.Data, &nodes); err != nil {
		t.Fatalf("decode nodes: %v (data=%s)", err, env.Data)
	}
	if len(nodes) != 1 || nodes[0]["name"] != "c102-node" {
		t.Errorf("nodes = %s, want the seeded c102-node", env.Data)
	}
	if _, env := c102Call(t, h.nodeCheckStatus, http.MethodGet, ""); env.Code == 0 {
		t.Errorf("GET nodeCheckStatus must fail the method guard, got code 0")
	}
}

// TestC102NodeLatencyProbe covers POST /api/v1/node/latency/probe 的
// 入口守卫：未配延迟测试点 → 明示错误；坏 JSON 被拒。真实探测
// （probeNodeLatency 出站）由 LIVE 真机格验。
func TestC102NodeLatencyProbe(t *testing.T) {
	h, _ := c102Handler(t)
	_, env := c102Call(t, h.nodeLatencyProbe, http.MethodPost, `{"ids":[]}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "还没有设置延迟测试点") {
		t.Errorf("no latency points: code=%d msg=%q, want 还没有设置延迟测试点", env.Code, env.Msg)
	}
	if _, env := c102Call(t, h.nodeLatencyProbe, http.MethodPost, `{bad`); env.Code == 0 {
		t.Errorf("bad JSON nodeLatencyProbe must fail, got code 0")
	}
}

// TestC102NftPendingDeleteList covers POST /api/v1/node/nftables/pending-deletes：
// 管理员见待删行（含 forwardId/nodeId/retryCount）；普通用户 403；GET 被拒。
func TestC102NftPendingDeleteList(t *testing.T) {
	h, r := c102Handler(t)
	now := time.Now().UnixMilli()
	row := &model.NftPendingDelete{ForwardID: 999, NodeID: 999, RetryCount: 2, NextRetryAt: 123, LastError: "boom", CreatedAt: now, UpdatedAt: now}
	if err := r.DB().Create(row).Error; err != nil {
		t.Fatalf("seed pending delete: %v", err)
	}
	_, env := c102CallAs(t, h.nftPendingDeleteList, http.MethodPost, `{}`, 1, 0)
	if env.Code != 0 {
		t.Fatalf("admin list code=%d msg=%q", env.Code, env.Msg)
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("decode items: %v (data=%s)", err, env.Data)
	}
	if len(items) != 1 || items[0]["forwardId"] != float64(999) || items[0]["retryCount"] != float64(2) {
		t.Errorf("items = %s, want the seeded pending delete", env.Data)
	}
	if _, env := c102CallAs(t, h.nftPendingDeleteList, http.MethodPost, `{}`, 2, 1); env.Code != 403 {
		t.Errorf("non-admin pending-deletes: code=%d, want 403", env.Code)
	}
	if _, env := c102CallAs(t, h.nftPendingDeleteList, http.MethodGet, ``, 1, 0); env.Code == 0 {
		t.Errorf("GET nftPendingDeleteList must fail the method guard, got code 0")
	}
}

// TestC102ListReleases covers POST /api/v1/node/releases 的离线分支：
// dev 通道回空列表（不触网）；坏 JSON 被拒。真实拉取
// （fetchGitHubReleases）由 LIVE 真机格验。
func TestC102ListReleases(t *testing.T) {
	h, _ := c102Handler(t)
	_, env := c102Call(t, h.listReleases, http.MethodPost, `{"channel":"dev"}`)
	if env.Code != 0 {
		t.Fatalf("dev channel code=%d msg=%q", env.Code, env.Msg)
	}
	var items []interface{}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("decode releases: %v (data=%s)", err, env.Data)
	}
	if len(items) != 0 {
		t.Errorf("dev channel releases = %s, want empty list", env.Data)
	}
	if _, env := c102Call(t, h.listReleases, http.MethodPost, `{bad`); env.Code == 0 {
		t.Errorf("bad JSON listReleases must fail, got code 0")
	}
}

// TestC102NodeRouteGet covers POST /api/v1/node/route：无缓存路由
// → code 0 且 data 缺省（response.R 的 data 为 omitempty，OK(nil)
// 不输出 data 字段）；id 非法被拒；GET 被拒。
func TestC102NodeRouteGet(t *testing.T) {
	h, _ := c102Handler(t)
	_, env := c102Call(t, h.nodeRouteGet, http.MethodPost, `{"id":7}`)
	if env.Code != 0 {
		t.Fatalf("route get code=%d msg=%q", env.Code, env.Msg)
	}
	if s := string(env.Data); s != "" && s != "null" {
		t.Errorf("uncached route data = %s, want absent or null", env.Data)
	}
	if _, env := c102Call(t, h.nodeRouteGet, http.MethodPost, `{"id":0}`); env.Code == 0 {
		t.Errorf("id 0 route get must fail, got code 0")
	}
	if _, env := c102Call(t, h.nodeRouteGet, http.MethodGet, ""); env.Code == 0 {
		t.Errorf("GET nodeRouteGet must fail the method guard, got code 0")
	}
}

// TestC102NodeRouteProbe covers POST /api/v1/node/route/probe 的
// 入口守卫：未配延迟测试点 → 明示错误（先于真实 trace）；id 非法被拒。
func TestC102NodeRouteProbe(t *testing.T) {
	h, _ := c102Handler(t)
	_, env := c102Call(t, h.nodeRouteProbe, http.MethodPost, `{"id":7}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "还没有设置延迟测试点") {
		t.Errorf("no latency points: code=%d msg=%q, want 还没有设置延迟测试点", env.Code, env.Msg)
	}
	if _, env := c102Call(t, h.nodeRouteProbe, http.MethodPost, `{"id":0}`); env.Code == 0 {
		t.Errorf("id 0 route probe must fail, got code 0")
	}
}

// TestC102SubscriptionTemplateCatalog covers POST /api/v1/subscription/template/catalog：
// 纯函数面，回规则分类表；GET 被拒。
func TestC102SubscriptionTemplateCatalog(t *testing.T) {
	h, _ := c102Handler(t)
	_, env := c102Call(t, h.subscriptionTemplateCatalog, http.MethodPost, `{}`)
	if env.Code != 0 {
		t.Fatalf("catalog code=%d msg=%q", env.Code, env.Msg)
	}
	var cats []interface{}
	if err := json.Unmarshal(env.Data, &cats); err != nil {
		t.Fatalf("decode catalog: %v (data=%s)", err, env.Data)
	}
	if len(cats) == 0 {
		t.Errorf("catalog is empty, want the template rule categories")
	}
	if _, env := c102Call(t, h.subscriptionTemplateCatalog, http.MethodGet, ""); env.Code == 0 {
		t.Errorf("GET subscriptionTemplateCatalog must fail the method guard, got code 0")
	}
}

// TestC102SubscriptionTemplateList covers POST /api/v1/subscription/template/list：
// 存一本地模板后列表含其视图（usage 0）；GET 被拒。
func TestC102SubscriptionTemplateList(t *testing.T) {
	h, _ := c102Handler(t)
	_, saveEnv := tplPost(t, h.subscriptionTemplateSave, `{"name":"c102模板","kind":"clash","content":`+tplJSONStr(tplTestClash)+`}`)
	if saveEnv["code"] != float64(0) {
		t.Fatalf("save template: %v", saveEnv)
	}
	_, env := c102Call(t, h.subscriptionTemplateList, http.MethodPost, `{}`)
	if env.Code != 0 {
		t.Fatalf("list code=%d msg=%q", env.Code, env.Msg)
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("decode list: %v (data=%s)", err, env.Data)
	}
	if len(items) != 1 || items[0]["name"] != "c102模板" {
		t.Errorf("list = %s, want the saved c102模板", env.Data)
	}
	if _, env := c102Call(t, h.subscriptionTemplateList, http.MethodGet, ""); env.Code == 0 {
		t.Errorf("GET subscriptionTemplateList must fail the method guard, got code 0")
	}
}

// TestC102SubscriptionTemplateRefresh covers POST /api/v1/subscription/template/refresh
// 的离线分支：不存在的 id 与非网址模板 →「模板不存在或不是网址模板」。
// 真实刷新（fetchTemplate 出站）由 LIVE 真机格验。
func TestC102SubscriptionTemplateRefresh(t *testing.T) {
	h, _ := c102Handler(t)
	_, env := c102Call(t, h.subscriptionTemplateRefresh, http.MethodPost, `{"id":99999}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "模板不存在或不是网址模板") {
		t.Errorf("missing template: code=%d msg=%q, want 模板不存在或不是网址模板", env.Code, env.Msg)
	}
	_, saveEnv := tplPost(t, h.subscriptionTemplateSave, `{"name":"c102本地","kind":"clash","content":`+tplJSONStr(tplTestClash)+`}`)
	if saveEnv["code"] != float64(0) {
		t.Fatalf("save template: %v", saveEnv)
	}
	data := saveEnv["data"].(map[string]interface{})
	id := int64(data["id"].(float64))
	_, env = c102Call(t, h.subscriptionTemplateRefresh, http.MethodPost, `{"id":`+int64ToStr(id)+`}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "模板不存在或不是网址模板") {
		t.Errorf("local template refresh: code=%d msg=%q, want 模板不存在或不是网址模板", env.Code, env.Msg)
	}
}

// TestC102GetUserInbounds covers POST /api/v1/user-inbound/list：
// 管理员见指定用户的分配；普通用户 403；userId 非法被拒。
func TestC102GetUserInbounds(t *testing.T) {
	h, r := c102Handler(t)
	now := time.Now().UnixMilli()
	adminID, err := r.CreateUser("c102-admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	userID, err := r.CreateUser("c102-user", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	n := c102SeedNode(t, r, now)
	k := c102SeedKernel(t, r, n.ID, now)
	in := c102SeedInbound(t, r, k.ID, n.ID, "c102-in", 10001, 0)
	if err := r.SetUserInboundAssignments(userID, []repo.UserInboundAssignment{{InboundID: in.ID, RelayID: 0}}); err != nil {
		t.Fatalf("grant inbound: %v", err)
	}
	_, env := c102CallAs(t, h.getUserInbounds, http.MethodPost, `{"userId":`+int64ToStr(userID)+`}`, adminID, 0)
	if env.Code != 0 {
		t.Fatalf("admin list code=%d msg=%q", env.Code, env.Msg)
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("decode items: %v (data=%s)", err, env.Data)
	}
	if len(items) != 1 || items[0]["inboundId"] != float64(in.ID) {
		t.Errorf("items = %s, want the granted inbound", env.Data)
	}
	if _, env := c102CallAs(t, h.getUserInbounds, http.MethodPost, `{"userId":`+int64ToStr(userID)+`}`, userID, 1); env.Code != 403 {
		t.Errorf("non-admin user-inbound list: code=%d, want 403", env.Code)
	}
	if _, env := c102CallAs(t, h.getUserInbounds, http.MethodPost, `{"userId":0}`, adminID, 0); env.Code == 0 {
		t.Errorf("userId 0 list must fail, got code 0")
	}
}

// TestC102GetMyInbounds covers POST /api/v1/user-inbound/mine：
// 本人见自己被分配的入站（含节点名）与 v1 签名；无分配 → 空；
// 未登录 →「未登录」。
func TestC102GetMyInbounds(t *testing.T) {
	h, r := c102Handler(t)
	now := time.Now().UnixMilli()
	userID, err := r.CreateUser("c102-mine", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	n := c102SeedNode(t, r, now)
	k := c102SeedKernel(t, r, n.ID, now)
	in := c102SeedInbound(t, r, k.ID, n.ID, "c102-mine-in", 10001, 0)
	if err := r.SetUserInboundAssignments(userID, []repo.UserInboundAssignment{{InboundID: in.ID, RelayID: 0}}); err != nil {
		t.Fatalf("grant inbound: %v", err)
	}
	_, env := c102CallAs(t, h.getMyInbounds, http.MethodPost, `{}`, userID, 1)
	if env.Code != 0 {
		t.Fatalf("mine code=%d msg=%q", env.Code, env.Msg)
	}
	var data struct {
		UserID   int64 `json:"userId"`
		Inbounds []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			NodeName string `json:"nodeName"`
		} `json:"inbounds"`
		Relays []interface{} `json:"relays"`
		Sig    string        `json:"sig"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode mine: %v (data=%s)", err, env.Data)
	}
	if data.UserID != userID || len(data.Inbounds) != 1 || data.Inbounds[0].Name != "c102-mine-in" || data.Inbounds[0].NodeName != "c102-node" {
		t.Errorf("mine = %s, want the granted inbound with node name", env.Data)
	}
	if len(data.Sig) != 32 {
		t.Errorf("sig = %q, want a 32-char v1 signature", data.Sig)
	}
	otherID, err := r.CreateUser("c102-none", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	_, env = c102CallAs(t, h.getMyInbounds, http.MethodPost, `{}`, otherID, 1)
	if env.Code != 0 {
		t.Fatalf("unassigned mine code=%d msg=%q", env.Code, env.Msg)
	}
	var empty struct {
		Inbounds []interface{} `json:"inbounds"`
		Relays   []interface{} `json:"relays"`
	}
	if err := json.Unmarshal(env.Data, &empty); err != nil {
		t.Fatalf("decode empty mine: %v (data=%s)", err, env.Data)
	}
	if len(empty.Inbounds) != 0 || len(empty.Relays) != 0 {
		t.Errorf("unassigned mine = %s, want empty inbounds/relays", env.Data)
	}
	if _, env := c102CallAs(t, h.getMyInbounds, http.MethodPost, `{}`, 0, 1); env.Code == 0 || !strings.Contains(env.Msg, "未登录") {
		t.Errorf("anonymous mine: code=%d msg=%q, want 未登录", env.Code, env.Msg)
	}
}

// TestC102ErrorPage covers /error (errorPage)：后端直出的唯一 HTML
// 面——404、no-cache、含「错误 404」标题。此前仅源码字面量断言引用。
func TestC102ErrorPage(t *testing.T) {
	h, _ := c102Handler(t)
	req := httptest.NewRequest(http.MethodGet, "/error", nil)
	rec := httptest.NewRecorder()
	h.errorPage(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("errorPage status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("errorPage Content-Type = %q, want text/html", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("errorPage Cache-Control = %q, want no-cache", cc)
	}
	if !strings.Contains(rec.Body.String(), "错误 404") {
		t.Errorf("errorPage body lacks 错误 404 title: %.200s", rec.Body.String())
	}
}
