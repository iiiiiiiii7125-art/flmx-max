package handler

// C09 簇回归（审计闭环 Round 1）：系统升级与内核管理的超时预算三层
// 对账。覆盖 B05-001（/system/version 登记误分类 + 三层错位）、
// B05-002（批量升级服务端封顶）、B05-003（check-updates 重定向兜底）、
// B05-004（版本列表缓存统一键）、B05-005（自升级残留判定改心跳存活
// + helper pull 超时）、B11-001（内核版本缓存 per-key singleflight）。
// shell 门禁侧的变体自检见 scripts/check-timeout-budgets-selftest.sh。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// ---- B05-001：登记表 C080/T009 必须是 strict 专行（非 class） ----

func c09RegistryRow(t *testing.T, cell string) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "doc", "audit", "timeout-budgets.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, cell+"\t") {
			return strings.Split(line, "\t")
		}
	}
	t.Fatalf("registry row %s not found", cell)
	return nil
}

func TestC09DiscoveryRowsAreStrict(t *testing.T) {
	for _, cell := range []string{"C080", "T009"} {
		f := c09RegistryRow(t, cell)
		if len(f) < 15 {
			t.Fatalf("%s row too short: %d fields", cell, len(f))
		}
		// fe=150 / sv=180 / be=120 / check=strict（字段序见登记表头）。
		if f[4] != "150" || f[9] != "180" || f[11] != "120" || f[14] != "strict" {
			t.Errorf("%s = fe %s, sv %s, be %s, check %s; want 150/180/120/strict", cell, f[4], f[9], f[11], f[14])
		}
		if f[10] != "systemVersionWriteDeadline" {
			t.Errorf("%s sv_src = %q, want systemVersionWriteDeadline", cell, f[10])
		}
	}
	// C059（/node/install）原以 class + discovery-prelude-legacy 命名
	// 例外保留发现前置，波 2 已按 C080 同法收口：strict 专行、例外
	// 摘除、sv_src 为 nodeInstallWriteDeadline（值同为 180）。
	f := c09RegistryRow(t, "C059")
	if len(f) < 16 {
		t.Fatalf("C059 row too short: %d fields", len(f))
	}
	if f[4] != "150" || f[9] != "180" || f[11] != "120" || f[14] != "strict" {
		t.Errorf("C059 = fe %s, sv %s, be %s, check %s; want 150/180/120/strict", f[4], f[9], f[11], f[14])
	}
	if f[10] != "nodeInstallWriteDeadline" {
		t.Errorf("C059 sv_src = %q, want nodeInstallWriteDeadline", f[10])
	}
	if f[15] != "" {
		t.Errorf("C059 exception = %q, want empty (discovery-prelude-legacy removed)", f[15])
	}
}

// ---- B05-001：两个发现端点必须武装写截止放宽 ----

type c09DeadlineRecorder struct {
	httptest.ResponseRecorder
	deadlineSet bool
}

func (d *c09DeadlineRecorder) SetWriteDeadline(time.Time) error {
	d.deadlineSet = true
	return nil
}

func TestC09DiscoveryHandlersArmWriteDeadline(t *testing.T) {
	h := &Handler{}
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request){
		"systemVersion":      h.systemVersion,
		"systemCheckUpdates": h.systemCheckUpdates,
		// C059 收口：nodeInstall 同为发现端点，必须武装其登记放宽
		//（GET 在方法检查处返回，不触发真实发现）。
		"nodeInstall": h.nodeInstall,
	} {
		rec := &c09DeadlineRecorder{}
		// GET 在方法检查处即返回（不触发真实发现），但放宽必须已武装。
		req := httptest.NewRequest(http.MethodGet, "/api/v1/system/version", nil)
		fn(rec, req)
		if !rec.deadlineSet {
			t.Errorf("%s did not extend its write deadline", name)
		}
	}
}

// ---- B05-003：check-updates 的重定向兜底 ----

func c09CheckUpdatesStub(t *testing.T, redirectTag string) *Handler {
	t.Helper()
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded for 16.76.14.51."}`)
	}))
	t.Cleanup(limited.Close)
	var web *httptest.Server
	web = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			w.Header().Set("Location", web.URL+"/"+githubRepo+"/releases/tag/"+redirectTag)
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(web.Close)

	oldAPIBase := systemUpgradeAPIBaseURL
	systemUpgradeAPIBaseURL = limited.URL
	t.Cleanup(func() { systemUpgradeAPIBaseURL = oldAPIBase })
	swapGitHubBasesForTest(t, limited.URL, web.URL)

	r, err := repo.Open(filepath.Join(t.TempDir(), "c09-check.db"))
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.UpsertConfig("github_proxy_enabled", "false", time.Now().UnixMilli()); err != nil {
		t.Fatalf("disable github proxy: %v", err)
	}
	return &Handler{repo: r}
}

type c09Envelope struct {
	Code int                    `json:"code"`
	Data systemUpgradeCheckData `json:"data"`
}

func c09PostCheckUpdates(t *testing.T, h *Handler, channel string) c09Envelope {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/check-updates",
		strings.NewReader(fmt.Sprintf(`{"channel":%q}`, channel)))
	rr := httptest.NewRecorder()
	h.systemCheckUpdates(rr, req)
	var env c09Envelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, rr.Body.String())
	}
	return env
}

func TestC09CheckUpdatesFallsBackToRedirect(t *testing.T) {
	t.Setenv("FLUX_VERSION", "1.0.0")
	h := c09CheckUpdatesStub(t, "1.7.2")

	env := c09PostCheckUpdates(t, h, "stable")
	if env.Code != 0 {
		t.Fatalf("stable check-updates code = %d, want 0 (degraded success)", env.Code)
	}
	if env.Data.LatestVersion != "1.7.2" {
		t.Fatalf("latestVersion = %q, want 1.7.2 via redirect fallback", env.Data.LatestVersion)
	}
	if !env.Data.HasUpdate {
		t.Fatalf("hasUpdate = false, want true (1.7.2 > 1.0.0)")
	}
	if len(env.Data.Releases) != 0 {
		t.Fatalf("releases = %d entries, want empty in degraded mode", len(env.Data.Releases))
	}
	if !strings.Contains(env.Data.Reason, "降级") {
		t.Fatalf("reason = %q, want a degradation note", env.Data.Reason)
	}

	// dev 渠道不兜底：保留原错误。
	env = c09PostCheckUpdates(t, h, "dev")
	if env.Code != -2 {
		t.Fatalf("dev check-updates code = %d, want -2 (no redirect fallback)", env.Code)
	}
}

func TestC09CheckUpdatesRedirectFallbackRejectsBeta(t *testing.T) {
	h := c09CheckUpdatesStub(t, "1.7.2-beta1")
	env := c09PostCheckUpdates(t, h, "stable")
	if env.Code != -2 {
		t.Fatalf("stable check-updates with beta redirect tag: code = %d, want -2 (J-03 channel filter)", env.Code)
	}
}

// ---- B05-004：两路发现共用一份缓存 ----

func TestC09ReleasesCacheSharedBetweenFetchers(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	clearReleasesCacheForTest(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"1.0.14","name":"1.0.14","draft":false,"prerelease":false}]`)
	}))
	defer srv.Close()
	swapGitHubBasesForTest(t, srv.URL, srv.URL)
	oldAPIBase := systemUpgradeAPIBaseURL
	systemUpgradeAPIBaseURL = srv.URL
	t.Cleanup(func() { systemUpgradeAPIBaseURL = oldAPIBase })

	r, err := repo.Open(filepath.Join(t.TempDir(), "c09-cache.db"))
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if err := r.UpsertConfig("github_proxy_enabled", "false", time.Now().UnixMilli()); err != nil {
		t.Fatalf("disable github proxy: %v", err)
	}
	h := &Handler{repo: r}

	// 节点侧先拉：面板侧随后必须命中同一条目，不再发第二次请求。
	nodeSide, err := fetchGitHubReleasesWithProxy(50, "")
	if err != nil {
		t.Fatalf("fetchGitHubReleasesWithProxy: %v", err)
	}
	panelSide, err := h.fetchSystemUpgradeReleases(50)
	if err != nil {
		t.Fatalf("fetchSystemUpgradeReleases: %v", err)
	}
	if len(nodeSide) != 1 || len(panelSide) != 1 || panelSide[0].TagName != "1.0.14" {
		t.Fatalf("unexpected releases: node=%v panel=%v", nodeSide, panelSide)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (shared cache entry)", got)
	}
	// 读取时 slice：小 perPage 不触发新请求。
	one, err := h.fetchSystemUpgradeReleases(1)
	if err != nil || len(one) != 1 {
		t.Fatalf("fetchSystemUpgradeReleases(1) = %v, %v", one, err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits after slice read = %d, want 1", got)
	}

	// 反序同样只拉一次。
	clearReleasesCacheForTest(t)
	if _, err := h.fetchSystemUpgradeReleases(50); err != nil {
		t.Fatalf("panel-first fetch: %v", err)
	}
	if _, err := fetchGitHubReleasesWithProxy(50, ""); err != nil {
		t.Fatalf("node-second fetch: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("upstream hits after reverse order = %d, want 2 (one per cold cache)", got)
	}
}

// ---- B05-002：批量升级上限与算术 ----

func TestC09BatchUpgradeRejectsTooManyIDs(t *testing.T) {
	h := &Handler{}
	ids := make([]string, 0, maxBatchUpgradeNodes+1)
	for i := 0; i < maxBatchUpgradeNodes+1; i++ {
		ids = append(ids, fmt.Sprintf("%d", i+1))
	}
	body := fmt.Sprintf(`{"ids":[%s],"version":"1.7.2"}`, strings.Join(ids, ","))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/batch-upgrade", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.nodeBatchUpgrade(rr, req)
	want := fmt.Sprintf("单次批量升级最多 %d 台节点", maxBatchUpgradeNodes)
	if !strings.Contains(rr.Body.String(), want) {
		t.Fatalf("response = %s, want it to contain %q", rr.Body.String(), want)
	}
}

func TestC09BatchCapArithmetic(t *testing.T) {
	bound := (maxBatchUpgradeNodes + batchWorkers - 1) / batchWorkers * int(upgradeTimeout/time.Second)
	sv := int(nodeBatchUpgradeWriteDeadline / time.Second)
	if bound >= sv {
		t.Fatalf("batch bound %ds must stay below server write deadline %ds", bound, sv)
	}
}

// ---- B05-005：残留判定心跳存活 ----

func TestC09UpgradeBlockingUsesHeartbeat(t *testing.T) {
	dir := t.TempDir()
	exec := &systemUpgradeExecutor{deployDir: dir, backendContainer: "flux-panel-backend"}
	hbPath := filepath.Join(dir, systemUpgradeHeartbeatFileName)
	now := time.Now()
	staleStart := systemUpgradeStatusData{
		InProgress: true,
		Version:    "1.7.2",
		StartedAt:  now.Add(-40 * time.Minute).UTC().Format(time.RFC3339),
	}

	// 无心跳文件（旧口径）：开始已超 30 分钟 → 残留放行。
	if exec.upgradeInProgressBlocking(staleStart, now) {
		t.Fatal("stale-by-time status without heartbeat must not block")
	}
	// 心跳新鲜：helper 仍活着，无论开始多久都拦截。
	if err := os.WriteFile(hbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !exec.upgradeInProgressBlocking(staleStart, now) {
		t.Fatal("fresh heartbeat must block a new upgrade even past the stale threshold")
	}
	// 心跳过期：helper 已死 → 立即放行，不必干等满 30 分钟。
	old := now.Add(-10 * time.Minute)
	if err := os.Chtimes(hbPath, old, old); err != nil {
		t.Fatal(err)
	}
	if exec.upgradeInProgressBlocking(staleStart, now) {
		t.Fatal("stale heartbeat must release the lock (helper is dead)")
	}
	// 无心跳 + 未超阈值（跨版本进行中状态）：旧时间规则仍拦截。
	if err := os.Remove(hbPath); err != nil {
		t.Fatal(err)
	}
	recent := systemUpgradeStatusData{
		InProgress: true,
		Version:    "1.7.2",
		StartedAt:  now.Add(-5 * time.Minute).UTC().Format(time.RFC3339),
	}
	if !exec.upgradeInProgressBlocking(recent, now) {
		t.Fatal("recent in-progress status without heartbeat must still block (legacy time rule)")
	}
	// 非进行中状态从不拦截。
	done := systemUpgradeStatusData{InProgress: false}
	if exec.upgradeInProgressBlocking(done, now) {
		t.Fatal("finished status must not block")
	}
}

func TestC09HelperScriptHasPullTimeoutAndHeartbeat(t *testing.T) {
	exec := &systemUpgradeExecutor{deployDir: "/opt/flmx-panel", backendContainer: "flux-panel-backend"}
	script := exec.helperScript()
	pullNeedle := fmt.Sprintf("timeout %d docker compose pull", systemUpgradePullTimeoutSeconds)
	pullIdx := strings.Index(script, pullNeedle)
	if pullIdx < 0 {
		t.Fatalf("helper script pull is not bounded by %q", pullNeedle)
	}
	hbIdx := strings.Index(script, "touch \"$HEARTBEAT_FILE\"")
	if hbIdx < 0 || hbIdx > pullIdx {
		t.Fatal("helper heartbeat must start before the pull stage")
	}
	if !strings.Contains(script, systemUpgradeHeartbeatFileName) {
		t.Fatalf("helper script must use heartbeat file %q", systemUpgradeHeartbeatFileName)
	}
	if !strings.Contains(script, "rollback files") {
		t.Fatal("pull failure must still roll back files")
	}
}

// ---- B11-001：内核版本缓存 singleflight ----

func c09DelayedReleasesStub(t *testing.T, delay time.Duration, hits *atomic.Int32) {
	t.Helper()
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"tag_name":"v1.14.0","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z"}]`)
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	resetKernelVersionCache(t)
}

func TestC09KernelVersionsSingleflightMergesSameType(t *testing.T) {
	var hits atomic.Int32
	c09DelayedReleasesStub(t, 1200*time.Millisecond, &hits)
	info, ok := kernel.Lookup(kernel.TypeSingbox)
	if !ok {
		t.Fatal("sing-box info missing")
	}
	const callers = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			items, err := kernelReleaseVersionsWithProxy(info, "")
			if err == nil && len(items) != 1 {
				err = fmt.Errorf("items = %d, want 1", len(items))
			}
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (same-type cold fetches merged)", got)
	}
}

func TestC09KernelVersionsDifferentTypesParallel(t *testing.T) {
	var hits atomic.Int32
	c09DelayedReleasesStub(t, 2*time.Second, &hits)
	xray, ok := kernel.Lookup(kernel.TypeXray)
	if !ok {
		t.Fatal("xray info missing")
	}
	singbox, ok := kernel.Lookup(kernel.TypeSingbox)
	if !ok {
		t.Fatal("sing-box info missing")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, errs[0] = kernelReleaseVersionsWithProxy(xray, "")
	}()
	go func() {
		defer wg.Done()
		<-start
		_, errs[1] = kernelReleaseVersionsWithProxy(singbox, "")
	}()
	begin := time.Now()
	close(start)
	wg.Wait()
	elapsed := time.Since(begin)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("upstream hits = %d, want 2 (one per type)", got)
	}
	// 串行（旧全局锁）需 ≥4s；并行应约 2s。阈值留足沙箱抖动余量。
	if elapsed > 3500*time.Millisecond {
		t.Fatalf("different kernel types still serialize: elapsed %v (serial would be >= 4s)", elapsed)
	}
}
