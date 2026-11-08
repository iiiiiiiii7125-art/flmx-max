package handler

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
		{"panel.example.com", ""},         // 无显式 scheme 不派生（归一化产物恒带 scheme）
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
	if !strings.Contains(cmd, "sha256sum ./install.sh") || !strings.Contains(cmd, "VERSION=1.8.0 ./install.sh -a 'wss://panel.example.com:8443' -s 'secret-x'") {
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

// R7-002 段 A 行为门：把 buildNodeInstallCommand 生成的命令在桩
// curl 下真跑——第二锚三态（异路同值放行 / 异路异值硬中止且脚本
// 不执行 / 全部异路无应答降级点名后放行）逐态断言，而非只查命令
// 文本含某子串。桩 curl 按 URL 前缀把请求映射到 panel/direct/
// ghfast 三个服务目录，其余候选一律 22 失败（模拟不可达）。
type nodeInstallStubRoute struct {
	script  string // 空 = 该线路不提供 install.sh
	shaOf   string // .sha256 内容取此字符串的哈希；空且 script 非空时取 script 本身
	shaOnly bool   // 只提供 .sha256（第二锚只需校验文件）
}

func runNodeInstallCommandWithStub(t *testing.T, routes map[string]nodeInstallStubRoute) (string, error) {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	serveDir := filepath.Join(root, "serve")
	workDir := filepath.Join(root, "work")
	for _, d := range []string{binDir, serveDir, workDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for key, r := range routes {
		dir := filepath.Join(serveDir, key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if r.script != "" && !r.shaOnly {
			if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte(r.script), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		hashOf := r.shaOf
		if hashOf == "" {
			hashOf = r.script
		}
		if hashOf != "" {
			sum := fmt.Sprintf("%x", sha256.Sum256([]byte(hashOf)))
			if err := os.WriteFile(filepath.Join(dir, "install.sh.sha256"), []byte(sum+"  install.sh\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	stub := `#!/bin/bash
out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2;;
    --connect-timeout|--max-time) shift 2;;
    -*) shift;;
    *) url="$1"; shift;;
  esac
done
key=""
case "$url" in
  *"/api/v1/open_api/gh/"*) key="panel";;
  "https://ghfast.top/"*) key="ghfast";;
  "https://github.com/"*) key="direct";;
  *) exit 22;;
esac
f="$STUB_SERVE/$key/${url##*/}"
[ -f "$f" ] || exit 22
cp "$f" "$out"
`
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", buildNodeInstallCommand("1.8.0", "wss://panel.example.com:8443", "secret-x"))
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_SERVE="+serveDir,
	)
	out, err := cmd.CombinedOutput()
	if _, statErr := os.Stat(filepath.Join(workDir, "executed.marker")); statErr == nil {
		return string(out) + "\n<<executed>>", err
	}
	return string(out), err
}

func TestBuildNodeInstallCommandSecondAnchor(t *testing.T) {
	goodScript := "#!/bin/sh\ntouch ./executed.marker\n"
	otherScript := "#!/bin/sh\necho tampered\n"

	t.Run("cross-route-agree-executes", func(t *testing.T) {
		out, err := runNodeInstallCommandWithStub(t, map[string]nodeInstallStubRoute{
			"panel":  {script: goodScript},
			"direct": {script: goodScript},
		})
		if err != nil {
			t.Fatalf("异路同值应放行执行, err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "<<executed>>") || !strings.Contains(out, "跨线路锚复核通过") {
			t.Fatalf("应经第二锚复核通过并执行脚本, out=%q", out)
		}
	})

	t.Run("cross-route-mismatch-aborts", func(t *testing.T) {
		out, err := runNodeInstallCommandWithStub(t, map[string]nodeInstallStubRoute{
			"panel":  {script: goodScript},
			"direct": {shaOf: otherScript, shaOnly: true},
		})
		if err == nil {
			t.Fatalf("异路异值必须硬中止, out=%q", out)
		}
		if strings.Contains(out, "<<executed>>") || !strings.Contains(out, "跨线路锚复核失败") {
			t.Fatalf("异值时脚本不得执行且须点名失败, out=%q", out)
		}
	})

	t.Run("no-cross-route-answer-degrades-with-note", func(t *testing.T) {
		out, err := runNodeInstallCommandWithStub(t, map[string]nodeInstallStubRoute{
			"panel": {script: goodScript},
		})
		if err != nil {
			t.Fatalf("无异路应答应降级放行, err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "<<executed>>") || !strings.Contains(out, "跨线路锚复核已降级") {
			t.Fatalf("降级须点名且脚本仍执行, out=%q", out)
		}
	})

	t.Run("direct-winner-mismatch-aborts", func(t *testing.T) {
		out, err := runNodeInstallCommandWithStub(t, map[string]nodeInstallStubRoute{
			"direct": {script: goodScript},
			"ghfast": {shaOf: otherScript, shaOnly: true},
		})
		if err == nil {
			t.Fatalf("直连定版后异路异值必须硬中止, out=%q", out)
		}
		if strings.Contains(out, "<<executed>>") || !strings.Contains(out, "跨线路锚复核失败") {
			t.Fatalf("异值时脚本不得执行且须点名失败, out=%q", out)
		}
	})
}

// R2-P2-08 行为门：生成命令的首跳不再静态恒定——带延迟桩的真
// 跑断言本体首试落点由探测实测延迟决定，而非候选静态序。桩
// curl 在提供 .sha256 时向 stdout 打印该路线的伪造 time_total
// （生成命令的探测段以 $(...) 捕获为实测延迟），并把每次成功
// 提供 install.sh 本体的路线记入 STUB_BODY_LOG——本体首个成
// 功者即获胜候选，日志首行即首跳落点。三路线提供同一脚本
// （哈希一致，第二锚可正常复核通过）。
func runNodeInstallCommandWithLatencyStub(t *testing.T, lat map[string]string) (string, []string, error) {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	serveDir := filepath.Join(root, "serve")
	workDir := filepath.Join(root, "work")
	for _, d := range []string{binDir, serveDir, workDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\ntouch ./executed.marker\n"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(script)))
	for _, key := range []string{"panel", "direct", "ghfast"} {
		dir := filepath.Join(serveDir, key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "install.sh.sha256"), []byte(sum+"  install.sh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stub := `#!/bin/bash
out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2;;
    --connect-timeout|--max-time) shift 2;;
    -*) shift;;
    *) url="$1"; shift;;
  esac
done
key=""
case "$url" in
  *"/api/v1/open_api/gh/"*) key="panel";;
  "https://ghfast.top/"*) key="ghfast";;
  "https://github.com/"*) key="direct";;
  *) exit 22;;
esac
f="$STUB_SERVE/$key/${url##*/}"
[ -f "$f" ] || exit 22
cp "$f" "$out"
case "${url##*/}" in
  install.sh.sha256)
    case "$key" in
      panel) printf '%s' "$STUB_LAT_PANEL";;
      direct) printf '%s' "$STUB_LAT_DIRECT";;
      ghfast) printf '%s' "$STUB_LAT_GHFAST";;
    esac;;
  install.sh) echo "$key" >> "$STUB_BODY_LOG";;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	bodyLog := filepath.Join(workDir, "body.log")
	cmd := exec.Command("bash", "-c", buildNodeInstallCommand("1.8.0", "wss://panel.example.com:8443", "secret-x"))
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_SERVE="+serveDir,
		"STUB_BODY_LOG="+bodyLog,
		"STUB_LAT_PANEL="+lat["panel"],
		"STUB_LAT_DIRECT="+lat["direct"],
		"STUB_LAT_GHFAST="+lat["ghfast"],
	)
	out, err := cmd.CombinedOutput()
	var body []string
	if data, readErr := os.ReadFile(bodyLog); readErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				body = append(body, line)
			}
		}
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "executed.marker")); statErr == nil {
		return string(out) + "\n<<executed>>", body, err
	}
	return string(out), body, err
}

func TestBuildNodeInstallCommandProbeRankedFirstHop(t *testing.T) {
	t.Run("static-first-but-slowest-panel-loses-to-fastest-direct", func(t *testing.T) {
		out, body, err := runNodeInstallCommandWithLatencyStub(t, map[string]string{
			"panel": "0.300", "direct": "0.050", "ghfast": "0.200",
		})
		if err != nil {
			t.Fatalf("三路线齐备时命令应成功, err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "<<executed>>") {
			t.Fatalf("脚本应被执行, out=%q", out)
		}
		if len(body) != 1 || body[0] != "direct" {
			t.Fatalf("本体首试应落在实测最快的直连（面板源静态居首但实测最慢），body=%v out=%q", body, out)
		}
	})

	t.Run("panel-wins-when-measured-fastest", func(t *testing.T) {
		out, body, err := runNodeInstallCommandWithLatencyStub(t, map[string]string{
			"panel": "0.010", "direct": "0.400", "ghfast": "0.200",
		})
		if err != nil {
			t.Fatalf("三路线齐备时命令应成功, err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "<<executed>>") {
			t.Fatalf("脚本应被执行, out=%q", out)
		}
		if len(body) != 1 || body[0] != "panel" {
			t.Fatalf("面板源实测最快时本体首试应落在面板源（赢面来自实测）, body=%v out=%q", body, out)
		}
	})
}

func TestAgentArchFromReportedVersion(t *testing.T) {
	cases := []struct{ v, want string }{
		{"1.8.0 (debian/amd64)", "amd64"},
		{"1.8.0 (alpine/arm64)", "arm64"},
		{"1.7.9 (debian/arm64)", "arm64"},
		{"1.8.0", ""},                // 无后缀
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
// GATE F3：清理链最后登记 drain（t.Cleanup 为 LIFO，drain 最先执
// 行）——本用例触发的预热 goroutine 必须在基址/缓存目录恢复与
// TempDir 删除之前全部落定，否则其缓存写入会与 RemoveAll 相撞，
// 且恢复基址后还会敲真实 github.com。
func swapGHMirrorGitHubBase(t *testing.T, base string) {
	t.Helper()
	// R5-FC（L8-F-04）：gh_mirror 上游已改钉死拨号守卫，本地 stub
	// 须显式放行回环段（C-02 先例口径）。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	savedBase := ghMirrorBases.github
	ghMirrorBases.github = base
	t.Cleanup(func() { ghMirrorBases.github = savedBase })
	savedDir := ghMirrorDir
	ghMirrorDir = t.TempDir()
	t.Cleanup(func() { ghMirrorDir = savedDir })
	t.Cleanup(drainPreheatForTest)
}

// stubGHMirror404 把 gh_mirror 基址换成本地 404 服务并隔离缓存
// 目录，供不研究预热本身、但会走到下发预热的用例使用：预热全程
// 本地化（不再敲真实 github.com），期望哈希恒为空（与预热未命中
// 时的下发口径一致），且用例结束前排空全部预热 goroutine
// （swapGHMirrorGitHubBase 内含 drain 清理）。
func stubGHMirror404(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	swapGHMirrorGitHubBase(t, srv.URL)
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
