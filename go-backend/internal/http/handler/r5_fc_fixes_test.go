package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// Round 5 FC 簇回归门：限流分桶同族（L2-09 / L8-F-02）、gh 镜像
// 上游钉死与内容校验（L8-F-04 / L2-19 / L2-22）、同节点升级互斥
// （L2-10）。

// --- 限流分桶：gh 镜像按真实客户端分桶，不再全站共用一桶 ---

func resetGHMirrorRateForTest(t *testing.T, max int) {
	t.Helper()
	ghMirrorRateMu.Lock()
	saved := ghMirrorRate
	ghMirrorRate = make(map[string]*ghMirrorBucket)
	ghMirrorRateMu.Unlock()
	savedMax := ghMirrorRateMax
	ghMirrorRateMax = max
	t.Cleanup(func() {
		ghMirrorRateMu.Lock()
		ghMirrorRate = saved
		ghMirrorRateMu.Unlock()
		ghMirrorRateMax = savedMax
	})
}

func ghMirrorRequest(remoteAddr, xff string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/gh/https://github.com/not-whitelisted", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	return req
}

func TestGHMirrorRateLimitBucketsByRealClient(t *testing.T) {
	resetGHMirrorRateForTest(t, 3)
	h := &Handler{}
	call := func(remoteAddr, xff string) int {
		rec := httptest.NewRecorder()
		h.githubMirror(rec, ghMirrorRequest(remoteAddr, xff))
		return rec.Code
	}
	// 同一真实客户端（经本机反代，XFF 区分）打满自己的桶：
	// 前 3 次过限流（路径不合法回 404），第 4 次 429。
	for i := 0; i < 3; i++ {
		if code := call("127.0.0.1:9000", "203.0.113.7"); code != http.StatusNotFound {
			t.Fatalf("第 %d 次应过限流回 404，got %d", i+1, code)
		}
	}
	if code := call("127.0.0.1:9000", "203.0.113.7"); code != http.StatusTooManyRequests {
		t.Fatalf("同客户端第 4 次应 429，got %d", code)
	}
	// 另一真实客户端走同一反代：独立桶，不受前者连带（旧裸
	// RemoteAddr 键下这里会直接 429——批量升级自触发形态）。
	if code := call("127.0.0.1:9000", "203.0.113.8"); code != http.StatusNotFound {
		t.Fatalf("不同客户端应有独立桶，got %d", code)
	}
	// 无 XFF 的直连请求按对端 IP 分桶，亦独立。
	if code := call("198.51.100.4:9000", ""); code != http.StatusNotFound {
		t.Fatalf("直连请求应按对端 IP 独立分桶，got %d", code)
	}
}

func TestGHMirrorRateLimitUnresolvedFallsIntoSharedBucket(t *testing.T) {
	resetGHMirrorRateForTest(t, 3)
	h := &Handler{}
	call := func(remoteAddr string) int {
		rec := httptest.NewRecorder()
		h.githubMirror(rec, ghMirrorRequest(remoteAddr, ""))
		return rec.Code
	}
	// RemoteAddr 不可解析：旧实现整段跳过限流（fail-open），
	// 新实现记入共享桶——不同形态的垃圾地址共用一桶，打满后
	// 一律 429。
	for i := 0; i < 3; i++ {
		if code := call("garbage-addr"); code != http.StatusNotFound {
			t.Fatalf("共享桶第 %d 次应先过限流，got %d", i+1, code)
		}
	}
	if code := call("another-garbage"); code != http.StatusTooManyRequests {
		t.Fatalf("不可解析地址应共用一桶并被限流，got %d（fail-open 复发）", code)
	}
}

// --- gh_mirror tag 查询：Location 主机校验（L2-19） ---

func TestGHMirrorLatestTagValidatesLocationHost(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8") // 钉死拨号放行本地 stub
	savedBases := ghMirrorBases
	t.Cleanup(func() { ghMirrorBases = savedBases })

	var location string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	ghMirrorBases.github = srv.URL

	// 站外 Location：旧实现只按 marker 切串照单全收，必须拒绝。
	location = "https://evil.example.com/owner/fc-foreign/releases/tag/9.9.9"
	if tag, err := ghMirrorLatestTag("owner/fc-foreign", ""); err == nil {
		t.Fatalf("站外 Location 必须拒绝，got tag=%q", tag)
	}

	// 与基址同主机的 Location：正常解析（相对主机继承形态亦可，
	// 此处用绝对地址）。
	location = srv.URL + "/owner/fc-ok/releases/tag/1.2.3"
	tag, err := ghMirrorLatestTag("owner/fc-ok", "")
	if err != nil || tag != "1.2.3" {
		t.Fatalf("同主机 Location 应解析出 tag，got tag=%q err=%v", tag, err)
	}
}

// --- gh_mirror 下载：API 内容校验（L8-F-04） ---

func TestGHMirrorDownloadValidatesAPIJSON(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	savedBases := ghMirrorBases
	savedDir := ghMirrorDir
	ghMirrorDir = t.TempDir()
	t.Cleanup(func() { ghMirrorBases = savedBases; ghMirrorDir = savedDir })

	var apiBody string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, apiBody)
	}))
	t.Cleanup(apiSrv.Close)
	ghMirrorBases.api = apiSrv.URL

	binSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html>not json, still a fine binary</html>")
	}))
	t.Cleanup(binSrv.Close)
	ghMirrorBases.github = binSrv.URL

	// 落地页 HTML 冒充 API 响应：不得入缓存。
	apiBody = "<html><title>landing</title></html>"
	badDest := filepath.Join(ghMirrorDir, "bad-api")
	if err := ghMirrorDownload(apiSrv.URL+"/repos/owner/repo/releases", badDest); err == nil {
		t.Fatal("非 JSON 的 API 响应必须被拒绝")
	}
	if _, err := os.Stat(badDest); !os.IsNotExist(err) {
		t.Fatalf("被拒内容不得落缓存文件，stat err=%v", err)
	}

	// 合法 JSON 数组：正常入缓存。
	apiBody = `[{"tag_name":"1.0.0"}]`
	goodDest := filepath.Join(ghMirrorDir, "good-api")
	if err := ghMirrorDownload(apiSrv.URL+"/repos/owner/repo/releases", goodDest); err != nil {
		t.Fatalf("合法 API JSON 应下载成功: %v", err)
	}
	if body, err := os.ReadFile(goodDest); err != nil || string(body) != apiBody {
		t.Fatalf("缓存内容应与上游一致，got %q err=%v", body, err)
	}

	// 非 API 面（二进制）不做 JSON 校验，同内容应放行。
	binDest := filepath.Join(ghMirrorDir, "bin")
	if err := ghMirrorDownload(binSrv.URL+"/owner/repo/releases/download/1.0.0/gost-amd64", binDest); err != nil {
		t.Fatalf("二进制面不应被 JSON 校验拦截: %v", err)
	}
}

// --- 同节点升级类操作互斥（L2-10） ---

func TestNodeUpgradeOpRegistryMutualExclusion(t *testing.T) {
	h := &Handler{}
	if _, ok := h.tryAcquireNodeUpgradeOp(7, "升级"); !ok {
		t.Fatal("首次登记应成功")
	}
	if existing, ok := h.tryAcquireNodeUpgradeOp(7, "回退"); ok || existing != "升级" {
		t.Fatalf("同节点第二操作应被拒并告知在途操作，got (%q, %v)", existing, ok)
	}
	if _, ok := h.tryAcquireNodeUpgradeOp(8, "回退"); !ok {
		t.Fatal("不同节点应互不影响")
	}
	h.releaseNodeUpgradeOp(7)
	if _, ok := h.tryAcquireNodeUpgradeOp(7, "回退"); !ok {
		t.Fatal("释放后应可再登记")
	}
}

type fcOpEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func callNodeOp(t *testing.T, fn func(http.ResponseWriter, *http.Request), payload map[string]interface{}) fcOpEnvelope {
	t.Helper()
	req := newAuthenticatedJSONRequest(t, payload)
	rec := httptest.NewRecorder()
	fn(rec, req)
	var env fcOpEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	return env
}

func TestNodeUpgradeOpsMutuallyExclusiveAcrossEntrypoints(t *testing.T) {
	stubGHMirror404(t) // 下发预热本地化＋排空
	r, err := repo.Open(t.TempDir() + "/fc-mutex.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	// 节点 A：UpgradeAgent 被 barrier 挡住（升级在途）；节点 B：
	// barrier 预放行（命令立即成功）。
	barrierA := &waveBarrier{release: make(chan struct{})}
	barrierB := &waveBarrier{release: make(chan struct{})}
	close(barrierB.release)
	idA := seedWaveNode(t, r, "fc-a", "198.51.100.21")
	idB := seedWaveNode(t, r, "fc-b", "198.51.100.22")
	stopA := startWaveMockNode(t, srv.URL, "fc-a-secret", barrierA)
	defer stopA()
	stopB := startWaveMockNode(t, srv.URL, "fc-b-secret", barrierB)
	defer stopB()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(idA) }, "fc-a online")
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(idB) }, "fc-b online")

	// 单节点升级 A，在 agent 侧阻塞至 barrier 放行。
	upgradeDone := make(chan fcOpEnvelope, 1)
	go func() {
		req := newAuthenticatedJSONRequest(t, map[string]interface{}{"id": idA, "version": "9.9.9"})
		rec := httptest.NewRecorder()
		h.nodeUpgrade(rec, req)
		var env fcOpEnvelope
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		upgradeDone <- env
	}()
	waitForCondition(t, 10*time.Second, func() bool { return barrierA.count() >= 1 }, "A 的 UpgradeAgent 在途")

	// 在途期间：回退与再次升级都必须在下发前被拒（旧行为是排进
	// agent 队列、被升级重启切断后误报失败）。
	if env := callNodeOp(t, h.nodeRollback, map[string]interface{}{"id": idA}); env.Code == 0 || !strings.Contains(env.Msg, "已有升级操作正在进行") {
		t.Fatalf("在途时回退应被拒并告知原因，got %+v", env)
	}
	if env := callNodeOp(t, h.nodeUpgrade, map[string]interface{}{"id": idA, "version": "9.9.10"}); env.Code == 0 || !strings.Contains(env.Msg, "已有升级操作正在进行") {
		t.Fatalf("在途时再次升级应被拒并告知原因，got %+v", env)
	}

	// 批量含 A、B：A 逐条明确失败（在途），B 不受影响正常成功。
	req := newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{idA, idB}, "version": "9.9.9"})
	rec := httptest.NewRecorder()
	h.nodeBatchUpgrade(rec, req)
	var payload batchResultsPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode batch: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Data.Results) != 2 {
		t.Fatalf("批量回执应有 2 条，got %+v", payload.Data.Results)
	}
	for _, item := range payload.Data.Results {
		switch item.ID {
		case idA:
			if item.Success || !strings.Contains(item.Message, "已有升级操作正在进行") {
				t.Fatalf("批量中在途节点应逐条失败并说明，got %+v", item)
			}
		case idB:
			if !item.Success {
				t.Fatalf("批量中未在途节点应正常成功，got %+v", item)
			}
		}
	}

	// 放行 A 的升级 → 完成成功；之后回退恢复可用。
	close(barrierA.release)
	select {
	case env := <-upgradeDone:
		if env.Code != 0 {
			t.Fatalf("A 的在途升级放行后应成功，got %+v", env)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("A 的升级放行后未在限时内返回")
	}
	if env := callNodeOp(t, h.nodeRollback, map[string]interface{}{"id": idA}); env.Code != 0 {
		t.Fatalf("升级完成后回退应恢复可用，got %+v", env)
	}
	h.upgradeMu.Lock()
	left := len(h.nodeUpgradeOps)
	h.upgradeMu.Unlock()
	if left != 0 {
		t.Fatalf("全部操作结束后登记应为空，残留 %d 条", left)
	}
}
