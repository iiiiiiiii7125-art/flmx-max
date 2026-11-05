package handler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B7-03（披露半）: PG 升级无升级前快照这一事实必须在回执与状态
// 面显式点名 (pgSnapshotDeferred), 不得只剩恒空的 snapshotPath
// 让运维误判已有兜底。本组钉三面: 状态落盘往返、回执 JSON 形
// 态、helper 终态回写保留。

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

func TestB703HelperTerminalStatusPreservesDeferred(t *testing.T) {
	script := (&systemUpgradeExecutor{}).helperScript()
	if !strings.Contains(script, "FLMX_UPGRADE_PG_SNAPSHOT_DEFERRED") {
		t.Fatal("helper write_status must consult the deferred env passthrough")
	}
	if !strings.Contains(script, `\"pgSnapshotDeferred\": true`) {
		t.Fatal("helper write_status must emit pgSnapshotDeferred into the terminal status")
	}
}
