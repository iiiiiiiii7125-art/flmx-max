package handler

import (
	"strings"
	"testing"
)

// GARBAGE-AUDIT A1（面板内升级面）：helper 在健康与卷名校验全过
// 后，须按升级前 .env 版本 tag（.env.upgrade.bak）定点删除旧
// backend/frontend 镜像（含旧仓名形态），且删除失败只告警不回
// 滚。本用例钉住段落的存在、落点（PG 卷校验之后、升级完成之
// 前）与告警口径；真机删除行为见发版真机验活格。

func TestHelperScriptCarriesOldImageCleanupAfterHealthGates(t *testing.T) {
	script := (&systemUpgradeExecutor{}).helperScript()

	for _, want := range []string{
		"GARBAGE-AUDIT A1",
		".env.upgrade.bak",
		"docker rmi",
		"flmx-panel-backend",
		"flmx-flux-panel-backend",
		"flvx-flux-panel-backend",
		"flmx-vite-frontend",
		"升级不受影响",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("helper script must contain %q for the A1 old-image cleanup", want)
		}
	}

	pgCheck := strings.Index(script, "FLMX_EXPECTED_POSTGRES_VOLUME")
	cleanup := strings.Index(script, "GARBAGE-AUDIT A1")
	done := strings.Index(script, `log "升级完成"`)
	if pgCheck < 0 || cleanup < 0 || done < 0 {
		t.Fatalf("helper script landmarks missing (pg=%d cleanup=%d done=%d)", pgCheck, cleanup, done)
	}
	if !(pgCheck < cleanup && cleanup < done) {
		t.Fatalf("A1 cleanup must run after the volume checks and before the success line (pg=%d cleanup=%d done=%d)", pgCheck, cleanup, done)
	}

	// 同版本重装与现行镜像保护：old==目标版本时整步跳过的守卫
	// 必须在删除段内（目标版本经 FLMX_UPGRADE_VERSION 透传）。
	if !strings.Contains(script, `"${FLMX_UPGRADE_VERSION:-}"`) {
		t.Fatal("A1 cleanup must compare the old tag against FLMX_UPGRADE_VERSION and skip on equality")
	}
	// 绝不允许出现全局清理形态。
	for _, banned := range []string{"image prune", "system prune", "volume prune", "rmi $(docker"} {
		if strings.Contains(script, banned) {
			t.Fatalf("A1 cleanup must stay targeted, found banned global form %q", banned)
		}
	}
}
