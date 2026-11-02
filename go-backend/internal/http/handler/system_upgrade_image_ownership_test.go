package handler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R2-006（源出首轮 AI-08）：面板内升级身份链此前只验容器名格式与
// 镜像 ID 可读，全链无镜像归属断言——env 透传丢失时构造器回退共享
// 旧名 flux-panel-backend，同机他方旧装的同名容器会被误认成本面板
// 后端，helper 将以他方镜像在本方部署目录执行升级。本文件钉死：
// 归属根与脚本侧 panel_image_prefix 同口径（ghcr.io/<githubRepo
// owner>/ 前缀），不在前缀下即不可升级并指引脚本更新路径。

func TestPanelImageRefPrefixDerivesFromGithubRepo(t *testing.T) {
	owner := githubRepo
	if i := strings.Index(owner, "/"); i >= 0 {
		owner = owner[:i]
	}
	want := "ghcr.io/" + strings.ToLower(owner) + "/"
	if got := panelImageRefPrefix(); got != want {
		t.Fatalf("panelImageRefPrefix() = %q, want %q", got, want)
	}
}

func TestPanelImageRefOwned(t *testing.T) {
	prefix := panelImageRefPrefix()
	cases := []struct {
		name string
		ref  string
		want bool
	}{
		{"own backend tag", prefix + "flmx-flux-panel-backend:1.9.4", true},
		{"own backend digest", prefix + "flmx-flux-panel-backend@sha256:abc123", true},
		{"own legacy image name under same prefix", prefix + "flux-panel-backend:1.8.2", true},
		{"foreign owner same image name", "ghcr.io/someone-else/flmx-flux-panel-backend:1.9.4", false},
		{"prefix boundary spoof", prefix[:len(prefix)-1] + ".evil.example/flmx-flux-panel-backend:1.9.4", false},
		{"bare local build name", "flux-panel-backend", false},
		{"other registry", "docker.io/library/flux-panel-backend:latest", false},
		{"empty", "", false},
		{"whitespace only", "   ", false},
	}
	for _, c := range cases {
		if got := panelImageRefOwned(c.ref); got != c.want {
			t.Errorf("%s: panelImageRefOwned(%q) = %v, want %v", c.name, c.ref, got, c.want)
		}
	}
}

// fakeDockerForOwnership 在 PATH 上放一个 docker 替身：--version 与
// compose version 恒成功；inspect 按格式串区分 {{.Image}}（返回固定
// 镜像 ID)与 {{.Config.Image}}（返回 imageRef，failRef 时报错）。
func fakeDockerForOwnership(t *testing.T, imageRef string, failRef bool) {
	t.Helper()
	dir := t.TempDir()
	refAction := fmt.Sprintf("echo '%s'; exit 0", imageRef)
	if failRef {
		refAction = "echo 'No such object' >&2; exit 1"
	}
	script := `#!/bin/sh
case "$1" in
  --version)
    echo 'Docker version 27.0.0'
    exit 0
    ;;
  compose)
    echo 'Docker Compose version v2.33.0'
    exit 0
    ;;
  inspect)
    case "$3" in
      *Config.Image*)
        ` + refAction + `
        ;;
      *)
        echo 'sha256:0123456789abcdef'
        exit 0
        ;;
    esac
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func ownershipTestExecutor(t *testing.T) *systemUpgradeExecutor {
	t.Helper()
	deployDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(deployDir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, ".env"), []byte("FLUX_VERSION=1.9.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &systemUpgradeExecutor{deployDir: deployDir, backendContainer: "flux-panel-backend"}
}

func TestVerifyBackendImageOwnershipAcceptsOwnImage(t *testing.T) {
	fakeDockerForOwnership(t, panelImageRefPrefix()+"flmx-flux-panel-backend:1.9.4", false)
	e := ownershipTestExecutor(t)
	if err := e.verifyBackendImageOwnership(context.Background()); err != nil {
		t.Fatalf("own image must pass ownership assertion, got %v", err)
	}
	cap := e.capability(context.Background())
	for _, reason := range cap.Reasons {
		if strings.Contains(reason, "不属于本面板") {
			t.Fatalf("own image produced an ownership refusal reason: %v", cap.Reasons)
		}
	}
}

func TestVerifyBackendImageOwnershipRejectsForeignImage(t *testing.T) {
	fakeDockerForOwnership(t, "ghcr.io/someone-else/flmx-flux-panel-backend:1.9.4", false)
	e := ownershipTestExecutor(t)
	err := e.verifyBackendImageOwnership(context.Background())
	if err == nil {
		t.Fatal("foreign image must fail the ownership assertion")
	}
	if !strings.Contains(err.Error(), "不属于本面板") || !strings.Contains(err.Error(), "更新面板") {
		t.Fatalf("refusal must name the mismatch and point at the script update path, got: %v", err)
	}
	cap := e.capability(context.Background())
	if cap.Capable {
		t.Fatalf("capability must be false for a foreign backend image, reasons=%v", cap.Reasons)
	}
	joined := strings.Join(cap.Reasons, "; ")
	if !strings.Contains(joined, "不属于本面板") || !strings.Contains(joined, "更新面板") {
		t.Fatalf("capability reasons must carry the ownership refusal with script guidance, got: %v", cap.Reasons)
	}
}

func TestVerifyBackendImageOwnershipFailsClosedOnInspectError(t *testing.T) {
	fakeDockerForOwnership(t, "", true)
	e := ownershipTestExecutor(t)
	err := e.verifyBackendImageOwnership(context.Background())
	if err == nil {
		t.Fatal("inspect failure must fail closed, got nil error")
	}
	if !strings.Contains(err.Error(), "inspect backend image ref") {
		t.Fatalf("inspect failure must surface the inspect error, got: %v", err)
	}
	cap := e.capability(context.Background())
	if cap.Capable {
		t.Fatalf("capability must be false when the image ref cannot be inspected, reasons=%v", cap.Reasons)
	}
}
