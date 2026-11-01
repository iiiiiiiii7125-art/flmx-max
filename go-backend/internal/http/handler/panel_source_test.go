package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 面板源分发一期③+二期（DESIGN panel-source）：安装命令面板候选
// prepend、下发前预热与期望哈希、节点架构提取。

func TestPanelMirrorPrefixFromAgentAddr(t *testing.T) {
	cases := []struct{ addr, want string }{
		{"wss://panel.example.com", "https://panel.example.com/api/v1/open_api/gh"},
		{"wss://panel.example.com:8443", "https://panel.example.com:8443/api/v1/open_api/gh"},
		{"ws://10.0.0.8:6365", "http://10.0.0.8:6365/api/v1/open_api/gh"},
		{"panel.example.com", ""},        // 无显式 scheme 不派生（归一化产物恒带 scheme）
		{"https://panel.example.com", ""}, // agentAddr 只会是 ws/wss 形态
		{"", ""},
	}
	for _, c := range cases {
		if got := panelMirrorPrefixFromAgentAddr(c.addr); got != c.want {
			t.Fatalf("panelMirrorPrefixFromAgentAddr(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}

func TestBuildNodeInstallCommandPanelCandidateFirst(t *testing.T) {
	cmd := buildNodeInstallCommand("1.8.0", "wss://panel.example.com:8443", "secret-x")
	panel := `"https://panel.example.com:8443/api/v1/open_api/gh"`
	iPanel := strings.Index(cmd, panel)
	iDirect := strings.Index(cmd, ` "" `)
	iMirror := strings.Index(cmd, `"https://ghfast.top"`)
	if iPanel < 0 {
		t.Fatalf("安装命令应含面板源候选 %s, got %q", panel, cmd)
	}
	if !(iPanel < iDirect && iDirect < iMirror) {
		t.Fatalf("候选顺序应为 面板源 > 直连 > 镜像, got %q", cmd)
	}
	// F-060 同源校验步与执行步保持原样。
	if !strings.Contains(cmd, "sha256sum ./install.sh") || !strings.Contains(cmd, "VERSION=1.8.0 ./install.sh -a wss://panel.example.com:8443 -s secret-x") {
		t.Fatalf("安装命令的校验/执行段不应被改动, got %q", cmd)
	}

	// 派生不出的地址（无 scheme）-> 无面板候选，命令与旧形态一致。
	cmd2 := buildNodeInstallCommand("1.8.0", "panel.example.com", "secret-x")
	if strings.Contains(cmd2, "open_api/gh") {
		t.Fatalf("无 scheme 的地址不得派生面板候选, got %q", cmd2)
	}
	if !strings.Contains(cmd2, `for p in "" "https://ghfast.top"`) {
		t.Fatalf("无面板候选时首候选应为直连, got %q", cmd2)
	}
}

func TestAgentArchFromReportedVersion(t *testing.T) {
	cases := []struct{ v, want string }{
		{"1.8.0 (debian/amd64)", "amd64"},
		{"1.8.0 (alpine/arm64)", "arm64"},
		{"1.7.9 (debian/arm64)", "arm64"},
		{"1.8.0", ""},               // 无后缀
		{"1.8.0 (debian/s390x)", ""}, // 未知架构不带哈希
		{"", ""},
		{"dev", ""},
	}
	for _, c := range cases {
		if got := agentArchFromReportedVersion(c.v); got != c.want {
			t.Fatalf("agentArchFromReportedVersion(%q) = %q, want %q", c.v, got, c.want)
		}
	}
}

// swapGHMirrorGitHubBase 把 gh_mirror 的 GitHub 基址换成本地 stub
// 并把缓存目录指向独立临时目录（与 r6_b5 夹具同式）。
func swapGHMirrorGitHubBase(t *testing.T, base string) {
	t.Helper()
	savedBase := ghMirrorBases.github
	ghMirrorBases.github = base
	t.Cleanup(func() { ghMirrorBases.github = savedBase })
	savedDir := ghMirrorDir
	ghMirrorDir = t.TempDir()
	t.Cleanup(func() { ghMirrorDir = savedDir })
}

const panelSourceTestHashAmd64 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const panelSourceTestHashArm64 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func panelSourceStubServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/gost-amd64.sha256"):
			fmt.Fprint(w, panelSourceTestHashAmd64+"  gost-amd64\n")
		case strings.HasSuffix(r.URL.Path, "/gost-arm64.sha256"):
			fmt.Fprint(w, panelSourceTestHashArm64+"  gost-arm64\n")
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			fmt.Fprint(w, panelSourceTestHashAmd64+"  gost\n")
		default:
			fmt.Fprint(w, "fake-binary-bytes")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGHMirrorReleaseSHA256FromCacheAndUpstream(t *testing.T) {
	srv := panelSourceStubServer(t)
	swapGHMirrorGitHubBase(t, srv.URL)

	if got := ghMirrorReleaseSHA256("1.2.3", "amd64"); got != panelSourceTestHashAmd64 {
		t.Fatalf("amd64 期望哈希 = %q, want %q", got, panelSourceTestHashAmd64)
	}
	if got := ghMirrorReleaseSHA256("1.2.3", "arm64"); got != panelSourceTestHashArm64 {
		t.Fatalf("arm64 期望哈希 = %q, want %q", got, panelSourceTestHashArm64)
	}
}

func TestGHMirrorReleaseSHA256UnavailableReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	swapGHMirrorGitHubBase(t, srv.URL)

	if got := ghMirrorReleaseSHA256("1.2.3", "amd64"); got != "" {
		t.Fatalf("上游 404 时期望哈希应为空（不带哈希下发）, got %q", got)
	}
}

func TestPreheatAgentReleaseHashesCollectsBothArches(t *testing.T) {
	srv := panelSourceStubServer(t)
	swapGHMirrorGitHubBase(t, srv.URL)

	hashes := preheatAgentReleaseHashes("1.2.3")
	if hashes["amd64"] != panelSourceTestHashAmd64 || hashes["arm64"] != panelSourceTestHashArm64 {
		t.Fatalf("双架构期望哈希应齐备, got %v", hashes)
	}
}

// 预热不得阻塞下发：上游挂起（2s 才回 500）且等待窗口只有 50ms
// 时，收集函数必须在窗口量级返回空结果而不是等上游。
func TestPreheatAgentReleaseHashesNeverBlocksDispatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	swapGHMirrorGitHubBase(t, srv.URL)

	savedWait := upgradePreheatHashWait
	upgradePreheatHashWait = 50 * time.Millisecond
	t.Cleanup(func() { upgradePreheatHashWait = savedWait })

	start := time.Now()
	hashes := preheatAgentReleaseHashes("1.2.3")
	elapsed := time.Since(start)
	if len(hashes) != 0 {
		t.Fatalf("上游挂起时不应收集到哈希, got %v", hashes)
	}
	if elapsed > time.Second {
		t.Fatalf("预热等待必须有界（窗口 50ms），实际阻塞 %v", elapsed)
	}
}
