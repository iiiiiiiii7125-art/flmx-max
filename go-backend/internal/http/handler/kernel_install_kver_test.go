package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// H131-KVER：内核安装无版本预检，失败留畸形版本记录。
// 预检（kernelReleaseTagExists）＋回滚（rollbackKernelInstallRecord）
// 的用例；真机复现见 U87-U6.md H131-KVER 节。

// stubReleaseTagAPI 桩 GitHub release tag 端点：tag（如 "v9.9.9"）→
// HTTP 状态，未列出的 tag 一律 404；返回 stub 基址。与
// stubReleasesAPI 同口径：本地服务器须配 allowlist、路线默认钉
// 直连-only（需代理路线的用例自行再 seedGitHubRoutesForTest 覆盖）。
func stubReleaseTagAPI(t *testing.T, statuses map[string]int) string {
	t.Helper()
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		tag := parts[len(parts)-1]
		status, ok := statuses[tag]
		if !ok {
			status = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			fmt.Fprintf(w, `{"tag_name":%q,"draft":false,"prerelease":false}`, tag)
		} else {
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	seedGitHubRoutesForTest(t, true, nil)
	return srv.URL
}

func TestKernelReleaseTagExists(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v1.14.4": http.StatusOK, "v1.14.4-flmx1": http.StatusOK, "v5.5.5": http.StatusInternalServerError})
	info, ok := kernel.Lookup(kernel.TypeSingbox)
	if !ok {
		t.Fatal("sing-box info missing")
	}
	if exists, determined := kernelReleaseTagExists(info, "1.14.4"); !determined || !exists {
		t.Fatalf("1.14.4: got exists=%v determined=%v, want true/true", exists, determined)
	}
	// Fork tag：预检走 fork 仓（ReleaseRepo 已换仓），v1.14.4-flmx1 须存在。
	if exists, determined := kernelReleaseTagExists(info, "1.14.4-flmx1"); !determined || !exists {
		t.Fatalf("1.14.4-flmx1: got exists=%v determined=%v, want true/true", exists, determined)
	}
	if exists, determined := kernelReleaseTagExists(info, "9.9.9"); !determined || exists {
		t.Fatalf("9.9.9: got exists=%v determined=%v, want false/true", exists, determined)
	}
	if exists, determined := kernelReleaseTagExists(info, "5.5.5"); determined || exists {
		t.Fatalf("5.5.5 (500): got exists=%v determined=%v, want false/false (inconclusive)", exists, determined)
	}
}

// 代理 404 不具权威性：直连 404 才算不存在。代理先 404、直连后 404
// 时仍须判不存在；若直连存在，代理 404 不得否决。
func TestKernelReleaseTagExistsProxy404NotAuthoritative(t *testing.T) {
	base := stubReleaseTagAPI(t, map[string]int{"v1.14.4": http.StatusOK})
	// 直连判死 → 代理先行、直连兜底；代理前缀即 stub 自身。
	seedGitHubRoutesForTest(t, false, []string{base})
	info, ok := kernel.Lookup(kernel.TypeSingbox)
	if !ok {
		t.Fatal("sing-box info missing")
	}
	if exists, determined := kernelReleaseTagExists(info, "9.9.9"); !determined || exists {
		t.Fatalf("9.9.9 via proxy-then-direct: got exists=%v determined=%v, want false/true", exists, determined)
	}
	if exists, determined := kernelReleaseTagExists(info, "1.14.4"); !determined || !exists {
		t.Fatalf("1.14.4 via proxy-then-direct: got exists=%v determined=%v, want true/true", exists, determined)
	}
}

// 预检：9.9.9 在安装写记录前就被快速拒绝，存量版本纹丝不动。
func TestKernelInstallPrecheckRejectsUnknownVersionKVER(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{})
	r, err := repo.Open(t.TempDir() + "/kver-precheck.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "kver-precheck", "198.51.100.21")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14.4")
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{
		"installed_version": "sing-box version 1.14.4", "status": kernelStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "9.9.9")
	elapsed := time.Since(start)
	if code == 0 {
		t.Fatal("installing sing-box 9.9.9 must be refused by the precheck")
	}
	if !strings.Contains(msg, "不存在") {
		t.Fatalf("refusal must name the missing version, got %q", msg)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("precheck refusal took %v, must fail fast (no minutes-long retries)", elapsed)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14.4" || after.InstalledVersion != "sing-box version 1.14.4" {
		t.Fatalf("refused install must not touch the stored row, got version=%q installed=%q", after.Version, after.InstalledVersion)
	}
}

// 未触达面（pending）：节点离线、下发未达 agent 时版本写保留为
// 待安装意图（R2-F2-A2/A3 钉死语义），不回滚；回执沿用旧文案。
func TestKernelInstallKeepsVersionWhenApplyNeverReachesAgentKVER(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v1.14.3": http.StatusOK})
	r, err := repo.Open(t.TempDir() + "/kver-rollback.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "kver-rollback", "198.51.100.22")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14.4")
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{
		"installed_version": "sing-box version 1.14.4", "status": kernelStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}

	// 节点离线（seed 默认 status=0），下发必败；1.14.3 经预检存在。
	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "1.14.3")
	if code == 0 {
		t.Fatal("install on an offline node must fail at apply")
	}
	if !strings.Contains(msg, "内核已登记") {
		t.Fatalf("pending install must keep the registered-failure receipt, got %q", msg)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14.3" {
		t.Fatalf("pending install must keep the requested version as intent, got %q", after.Version)
	}
	if after.Status != kernelStatusPending {
		t.Fatalf("offline apply must leave status pending, got %q", after.Status)
	}
	if after.LastError == "" {
		t.Fatal("failure trace must stay in last_error")
	}
}

// Fork 版同口径：1.14.4-flmx1 经 fork 仓预检存在，离线节点上版本
// 写保留为待安装意图（pending），不回滚。
func TestKernelInstallForkVersionStoresOnOfflineNodeKVER(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v1.14.4-flmx1": http.StatusOK})
	r, err := repo.Open(t.TempDir() + "/kver-fork.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "kver-fork", "198.51.100.26")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14.4")
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{
		"installed_version": "sing-box version 1.14.4", "status": kernelStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}

	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "1.14.4-flmx1")
	if code == 0 {
		t.Fatal("install on an offline node must fail at apply")
	}
	if !strings.Contains(msg, "内核已登记") {
		t.Fatalf("pending install must keep the registered-failure receipt, got %q", msg)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14.4-flmx1" {
		t.Fatalf("pending install must keep the fork version as intent, got %q", after.Version)
	}
	if after.Status != kernelStatusPending {
		t.Fatalf("offline apply must leave status pending, got %q", after.Status)
	}
}

// 回滚面（error）：mock agent 明确拒绝下发（下载失败同型）时，
// 版本回到安装前，失败痕迹留在 last_error，实装版本字段不动。
func TestKernelInstallRollbackRestoresVersionOnAgentFailureKVER(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v26.11.9": http.StatusOK})
	h, r, mock, _, k, nodeID := setupKernelNotifyRig(t)
	mock.setApply("fail", "mock agent exploded")

	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeXray, "26.11.9")
	if code == 0 {
		t.Fatal("install with a failing agent apply must fail")
	}
	if !strings.Contains(msg, "已回滚") {
		t.Fatalf("failure receipt must name the rollback, got %q", msg)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "v25.1.1" {
		t.Fatalf("failed install must roll the version back to v25.1.1, got %q", after.Version)
	}
	if after.Status != kernelStatusError {
		t.Fatalf("agent-failed apply must leave status error, got %q", after.Status)
	}
	if after.LastError == "" {
		t.Fatal("failure trace must stay in last_error after rollback")
	}
}

// 未触达面（pending，新建行）：首装下发未达 agent 时行保留为待安装
// 意图，不删行（与 R2-F2-A3 回退行落库口径一致）。
func TestKernelInstallKeepsNewRowWhenApplyNeverReachesAgentKVER(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v1.14.4": http.StatusOK})
	r, err := repo.Open(t.TempDir() + "/kver-newrow.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "kver-newrow", "198.51.100.23")

	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "1.14.4")
	if code == 0 {
		t.Fatal("install on an offline node must fail at apply")
	}
	if !strings.Contains(msg, "内核已登记") {
		t.Fatalf("pending install must keep the registered-failure receipt, got %q", msg)
	}
	row, err := r.GetNodeKernelByType(nodeID, kernel.TypeSingbox)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("pending first install must keep the row as intent")
	}
	if row.Version != "1.14.4" || row.Status != kernelStatusPending {
		t.Fatalf("pending first install must keep version 1.14.4 / status pending, got %q / %q", row.Version, row.Status)
	}
}

// 回滚 helper（新建行、无入站）：error 面删行；pending 面不由
// helper 决定（见 rollbackKernelInstallAfterFailure）。
func TestRollbackKernelInstallRecordDeletesFreshRow(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/kver-del.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	nodeID := seedRelayTargetNode(t, r, "kver-del", "198.51.100.25")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14.4")
	if ok := rollbackKernelInstallRecord(r, k.ID, false, "", 0, k.CreatedTime, "1.14.4"); !ok {
		t.Fatal("fresh row without inbounds must be removed by rollback")
	}
	row, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("rolled-back fresh row must be gone, got version=%q", row.Version)
	}
	// 创建戳不符（并发胜者行）时不删。
	k2 := seedB8Kernel(t, r, nodeID, kernel.TypeXray, "26.11.9")
	if ok := rollbackKernelInstallRecord(r, k2.ID, false, "", 0, k2.CreatedTime+1, "26.11.9"); ok {
		t.Fatal("rollback must not delete a row whose creation stamp is not this install's")
	}
	row, err = r.GetNodeKernel(k2.ID)
	if err != nil || row == nil {
		t.Fatalf("foreign row must survive, got %v / %v", row, err)
	}
}
func TestRollbackKernelInstallRecordKeepsRowWithInbounds(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/kver-guard.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	nodeID := seedRelayTargetNode(t, r, "kver-guard", "198.51.100.24")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14.4")
	seedChainInbound(t, r, k, "guard-in", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Settings:  kernel.Settings{UUID: "22222222-2222-4222-8222-222222222222"},
		Transport: kernel.Transport{Type: kernel.TransportTCP}}, 0)

	now := time.Now().UnixMilli()
	// 以行自身的 CreatedTime 过创建戳关，让入站守卫成为唯一拦路项。
	if ok := rollbackKernelInstallRecord(r, k.ID, false, "", 0, k.CreatedTime, "1.14.4"); ok {
		t.Fatal("rollback must not delete a row that has inbounds attached")
	}
	row, err := r.GetNodeKernel(k.ID)
	if err != nil || row == nil {
		t.Fatalf("guarded row must survive, got %v / %v", row, err)
	}
	// 存量行回滚仍走恢复分支。
	before, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok := rollbackKernelInstallRecord(r, k.ID, true, "1.14.3", before.UpdatedTime, now, "1.14.4"); !ok {
		t.Fatal("existing-row rollback must succeed")
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14.3" {
		t.Fatalf("existing-row rollback must restore the previous version, got %q", after.Version)
	}
}
