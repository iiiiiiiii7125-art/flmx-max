package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B7-03（披露半，遗留口径）: 2.1.18 及更早的 PG 升级无升级前
// 快照, 该事实在回执与状态面以 pgSnapshotDeferred 显式点名,
// 不得以恒空的 snapshotPath 让运维误判已有兜底。自 R18-B8-03
// 起 PG 模式亦落快照, 该字段仅作旧状态文件的兼容保留（新发起
// 的升级恒为 false）; 本组钉两面：旧字段的状态落盘往返与回执
// JSON 形态仍可解析，helper 不再产生新的切出标记。

func TestB703StatusRoundTripCarriesPgSnapshotDeferred(t *testing.T) {
	dir := t.TempDir()
	exec := &systemUpgradeExecutor{deployDir: dir}
	st := systemUpgradeStatusData{
		InProgress:         false,
		Version:            "1.9.10",
		Result:             "success",
		PgSnapshotDeferred: true,
	}
	if err := exec.writeUpgradeStatus(st); err != nil {
		t.Fatalf("write status: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, systemUpgradeStatusFileName))
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}
	if !strings.Contains(string(raw), `"pgSnapshotDeferred":true`) {
		t.Fatalf("status file must name the deferred PG snapshot, got %s", raw)
	}
	back := exec.readUpgradeStatus()
	if !back.PgSnapshotDeferred {
		t.Fatalf("status read-back lost pgSnapshotDeferred: %+v", back)
	}

	// SQLite 形态 (未切出) 不得虚报: 字段缺席。
	exec2 := &systemUpgradeExecutor{deployDir: t.TempDir()}
	if err := exec2.writeUpgradeStatus(systemUpgradeStatusData{InProgress: true, SnapshotPath: "backups/x"}); err != nil {
		t.Fatalf("write sqlite status: %v", err)
	}
	raw2, _ := os.ReadFile(filepath.Join(exec2.deployDir, systemUpgradeStatusFileName))
	if strings.Contains(string(raw2), "pgSnapshotDeferred") {
		t.Fatalf("non-deferred status must not carry the field, got %s", raw2)
	}
}

func TestB703RunDataJSONNamesPgSnapshotDeferred(t *testing.T) {
	raw, err := json.Marshal(systemUpgradeRunData{Version: "1.9.10", PgSnapshotDeferred: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"pgSnapshotDeferred":true`) {
		t.Fatalf("run receipt must expose pgSnapshotDeferred, got %s", raw)
	}
	raw, _ = json.Marshal(systemUpgradeRunData{Version: "1.9.10"})
	if strings.Contains(string(raw), "pgSnapshotDeferred") {
		t.Fatalf("sqlite receipt must omit the field, got %s", raw)
	}
}

func TestB703HelperTerminalStatusNoLongerDefers(t *testing.T) {
	// R18-B8-03：PG 快照已落地，helper 不得再产生新的切出标记；
	// 旧状态文件的 pgSnapshotDeferred 仍由 Go 面字段兼容解析。
	script := (&systemUpgradeExecutor{}).helperScript()
	if strings.Contains(script, "FLMX_UPGRADE_PG_SNAPSHOT_DEFERRED") || strings.Contains(script, "pgSnapshotDeferred") {
		t.Fatal("helper must not reference the retired PG snapshot deferral")
	}
}
