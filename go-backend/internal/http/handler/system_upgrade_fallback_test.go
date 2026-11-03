package handler

import (
	"os"
	"testing"
)

// F4-02：面板内升级执行器在 PANEL_DEPLOY_DIR / PANEL_BACKEND_
// CONTAINER 双 env 丢失时，旧回退链会探旧谱系部署目录
// /opt/flvx-panel 并默认旧容器名 flux-panel-backend——同机另有同
// 账户旧谱系部署时，升级动作可能指向他方部署，只剩账户级镜像归
// 属断言兜底。现回退只认 FLMX 标准名（/opt/flmx-panel、
// flmx-panel-backend）；env 显式给出的存量旧名仍逐字沿用（随版
// Compose 恒设两键），再由镜像归属断言把关。本文件钉住 env 丢失
// 分支与 env 在场分支两面。

func TestSystemUpgradeExecutorFallbackUsesFlmxStandardNames(t *testing.T) {
	t.Setenv(panelDeployDirEnv, "")
	t.Setenv(panelBackendContainerEnv, "")

	exec := newSystemUpgradeExecutor()
	if exec.deployDir != "/opt/flmx-panel" {
		t.Fatalf("env-lost deployDir fallback = %q, want FLMX standard /opt/flmx-panel", exec.deployDir)
	}
	if exec.backendContainer != "flmx-panel-backend" {
		t.Fatalf("env-lost backendContainer fallback = %q, want FLMX standard flmx-panel-backend", exec.backendContainer)
	}
}

func TestSystemUpgradeExecutorFallbackIgnoresLegacyDeployDir(t *testing.T) {
	// 只有旧名目录存在、标准名目录不存在时，旧实现会把部署目录
	// 探成 /opt/flvx-panel；新口径必须仍回标准名（后续能力检查如
	// 实报不可升级）。沙箱/CI 无权创建 /opt 时跳过本格。
	legacyDir := "/opt/flvx-panel"
	created := false
	if _, err := os.Stat(legacyDir); os.IsNotExist(err) {
		if err := os.MkdirAll(legacyDir, 0o755); err == nil {
			created = true
			defer os.RemoveAll(legacyDir)
		}
	}
	if !created {
		if _, err := os.Stat(legacyDir); err != nil {
			t.Skip("cannot stage legacy deploy dir under /opt in this environment")
		}
	}
	if _, err := os.Stat("/opt/flmx-panel"); err == nil {
		t.Skip("/opt/flmx-panel exists in this environment; cannot isolate the legacy-only case")
	}

	t.Setenv(panelDeployDirEnv, "")
	exec := newSystemUpgradeExecutor()
	if exec.deployDir == legacyDir {
		t.Fatalf("executor fell back to legacy deploy dir %q; F4-02 requires FLMX standard names only", legacyDir)
	}
	if exec.deployDir != "/opt/flmx-panel" {
		t.Fatalf("env-lost deployDir = %q, want /opt/flmx-panel", exec.deployDir)
	}
}

func TestSystemUpgradeExecutorHonorsExplicitEnvNames(t *testing.T) {
	// 存量部署经随版 Compose 传入的旧名 env 是显式身份，构造器逐
	// 字沿用，不受回退口径收紧影响。
	t.Setenv(panelDeployDirEnv, "/opt/flvx-panel")
	t.Setenv(panelBackendContainerEnv, "flux-panel-backend")

	exec := newSystemUpgradeExecutor()
	if exec.deployDir != "/opt/flvx-panel" {
		t.Fatalf("explicit deployDir env = %q, want /opt/flvx-panel honored verbatim", exec.deployDir)
	}
	if exec.backendContainer != "flux-panel-backend" {
		t.Fatalf("explicit backendContainer env = %q, want flux-panel-backend honored verbatim", exec.backendContainer)
	}
}
