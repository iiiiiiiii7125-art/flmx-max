package socket

import (
	"os"
	"strings"
	"testing"
)

// R2-004: in the container install form, the panel-driven agent
// upgrade/rollback can never work — the handlers hardcode the
// script layout's binary path and a service manager the image does
// not have, so the attempt died with a bare staging-path error.
// The handlers must refuse up front with a receipt that names the
// correct path (re-run the Docker/Compose install command), before
// touching the filesystem or the network. The script form's entry
// behavior stays exactly as before.

func stubUpgradeInContainer(t *testing.T, v bool) {
	t.Helper()
	old := upgradeInContainer
	upgradeInContainer = func() bool { return v }
	t.Cleanup(func() { upgradeInContainer = old })
}

func TestHandleUpgradeAgentContainerGuidance(t *testing.T) {
	stubUpgradeInContainer(t, true)
	_, statErr := os.Stat("/etc/flux_agent")
	noLegacyDir := os.IsNotExist(statErr)

	err := (&WebSocketReporter{}).handleUpgradeAgent(map[string]interface{}{"version": "1.9.5"})
	if err == nil {
		t.Fatal("container form upgrade must refuse, got nil error")
	}
	if !strings.Contains(err.Error(), "容器形态") || !strings.Contains(err.Error(), "安装命令") {
		t.Fatalf("receipt must name the container form and the install command, got: %v", err)
	}
	if strings.Contains(err.Error(), "暂存") {
		t.Fatalf("receipt must not be the staging failure (the refusal must precede any file action), got: %v", err)
	}
	if noLegacyDir {
		if _, err := os.Stat("/etc/flux_agent"); !os.IsNotExist(err) {
			t.Fatalf("container refusal created /etc/flux_agent — a file action leaked past the guard")
		}
	}
}

func TestHandleRollbackAgentContainerGuidance(t *testing.T) {
	stubUpgradeInContainer(t, true)
	_, err := (&WebSocketReporter{}).handleRollbackAgent(nil)
	if err == nil {
		t.Fatal("container form rollback must refuse, got nil error")
	}
	if !strings.Contains(err.Error(), "容器形态") || !strings.Contains(err.Error(), "安装命令") {
		t.Fatalf("receipt must name the container form and the install command, got: %v", err)
	}
	if strings.Contains(err.Error(), "没有可用的备份文件") {
		t.Fatalf("receipt must not be the script-layout backup miss, got: %v", err)
	}
}

func TestHandleUpgradeAgentNonContainerEntryUnchanged(t *testing.T) {
	stubUpgradeInContainer(t, false)
	err := (&WebSocketReporter{}).handleUpgradeAgent(map[string]interface{}{})
	if err == nil || !strings.Contains(err.Error(), "版本号不能为空") {
		t.Fatalf("script form entry validation changed, got: %v", err)
	}
}

func TestHandleRollbackAgentNonContainerEntryUnchanged(t *testing.T) {
	stubUpgradeInContainer(t, false)
	if _, err := os.Stat("/etc/flux_agent/flux_agent.old"); !os.IsNotExist(err) {
		t.Skip("a real backup file exists on this host; control case not applicable")
	}
	_, err := (&WebSocketReporter{}).handleRollbackAgent(nil)
	if err == nil || !strings.Contains(err.Error(), "没有可用的备份文件") {
		t.Fatalf("script form rollback entry behavior changed, got: %v", err)
	}
}
