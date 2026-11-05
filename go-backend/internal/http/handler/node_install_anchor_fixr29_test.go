package handler

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// FIX-R2-9 面3（面板生成的节点安装命令内嵌第二锚按获胜候选分层，
// 与脚本侧 R2-01-01 同律）：获胜候选记在 $w——直连（""）与面板源
// （第一方，命令里由 $pp 记）获胜且无异路复核时维持点名降级放行
// （既有 TestBuildNodeInstallCommandSecondAnchor 的
// no-cross-route-answer 子例即面板获胜形态、口径不变）；第三方
// 代理获胜且无异路给出合法校验值时必须 fail-closed：点名中止、
// 删除已下载脚本、整条命令失败、脚本不执行。桩形态与
// panel_source_test.go 的 R7-002 行为门一致（桩 curl 按 URL 前缀
// 映射 panel/direct/ghfast 三个服务目录，其余候选一律 22 失败），
// 本组 runner 额外回报工作目录里 install.sh 是否残留。

func runNodeInstallCommandFixR29(t *testing.T, routes map[string]nodeInstallStubRoute) (string, error, bool) {
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
	_, statErr := os.Stat(filepath.Join(workDir, "install.sh"))
	installLeft := statErr == nil
	if _, statErr := os.Stat(filepath.Join(workDir, "executed.marker")); statErr == nil {
		return string(out) + "\n<<executed>>", err, installLeft
	}
	return string(out), err, installLeft
}

func TestBuildNodeInstallCommandAnchorWinnerLayersFixR29(t *testing.T) {
	goodScript := "#!/bin/sh\ntouch ./executed.marker\n"

	t.Run("proxy-winner-no-corroboration-aborts", func(t *testing.T) {
		out, err, installLeft := runNodeInstallCommandFixR29(t, map[string]nodeInstallStubRoute{
			"ghfast": {script: goodScript},
		})
		if err == nil {
			t.Fatalf("代理获胜且无异路复核必须 fail-closed 中止, out=%q", out)
		}
		if strings.Contains(out, "<<executed>>") {
			t.Fatalf("中止时脚本不得执行, out=%q", out)
		}
		if !strings.Contains(out, "获胜线路为代理") || !strings.Contains(out, "已中止安装") {
			t.Fatalf("中止须点名获胜线路为代理与原因, out=%q", out)
		}
		if installLeft {
			t.Fatalf("fail-closed 中止须删除已下载的 install.sh")
		}
	})

	t.Run("proxy-winner-corroboration-agrees-executes", func(t *testing.T) {
		out, err, _ := runNodeInstallCommandFixR29(t, map[string]nodeInstallStubRoute{
			"ghfast": {script: goodScript},
			"direct": {shaOf: goodScript, shaOnly: true},
		})
		if err != nil {
			t.Fatalf("代理获胜但异路同值复核通过应放行执行, err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "<<executed>>") || !strings.Contains(out, "跨线路锚复核通过") {
			t.Fatalf("应经第二锚复核通过并执行脚本, out=%q", out)
		}
	})

	t.Run("direct-winner-no-corroboration-degrades", func(t *testing.T) {
		out, err, _ := runNodeInstallCommandFixR29(t, map[string]nodeInstallStubRoute{
			"direct": {script: goodScript},
		})
		if err != nil {
			t.Fatalf("直连获胜无异路应答应降级放行, err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "<<executed>>") || !strings.Contains(out, "跨线路锚复核已降级") {
			t.Fatalf("降级须点名且脚本仍执行, out=%q", out)
		}
	})
}
