package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R18-R2-D1-02: D1-01 一刀切的反面——旧面板先把 agent 升到高于
// 面板自身的版本，新 agent 重启连回后每条管理命令都被回
// MissingInnerTimestamp，管理通道全死（转发面不受影响，节点
// 仍显示在线、指标新鲜，只表现为每个管理命令都失败），直到
// 面板升级才恢复。upgrade.md 只规定了面板升级顺序，从未拦住
// “先升 agent”这条路。顺序门在下发前拦住它并点名原因；
// MissingInnerTimestamp 同时补进面板的预解析错误六型集
// （ws/server.go），旧面板绕过门禁后的诊断也不再漏掉本型。
func TestR18R2D102AgentUpgradePanelCompatibility(t *testing.T) {
	cases := []struct {
		name    string
		panel   string
		target  string
		wantErr string // 空串 = 放行
	}{
		{"target above panel is refused", "2.1.18", "2.1.19", "先把面板升级到 2.1.19"},
		{"target equal to panel is allowed", "2.1.19", "2.1.19", ""},
		{"target below panel is allowed", "2.1.19", "2.1.18", ""},
		{"dev panel cannot prove incompatibility", "dev", "2.1.19", ""},
		{"unparsable panel version fails closed", "latest", "2.1.19", "无法比较"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("FLMX_VERSION", c.panel)
			err := checkAgentUpgradePanelCompatibility(c.target)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("target %s with panel %s must be allowed, got %v", c.target, c.panel, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("target %s with panel %s must be refused naming the panel upgrade, got %v", c.target, c.panel, err)
			}
		})
	}
}

// 顺序门必须接在下发之前：离线节点 + 旧面板 + 高目标版本时，
// 回执必须是门禁原因，而不是派发失败（派发失败意味着门没拦住）。
func TestR18R2D102NodeUpgradeGateBeforeDispatch(t *testing.T) {
	t.Setenv("FLMX_VERSION", "2.1.18")
	r, err := repo.Open(t.TempDir() + "/r2-d1-02-gate.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status, version) VALUES('r2-d1-02-node', 's1202', '198.51.100.42', '1000-2000', ?, 1, '1.3.4')`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")

	out := decodeCode(t, postJSONToHandler(t, h.nodeUpgrade, map[string]interface{}{"id": nodeID, "version": "2.1.19"}).Body.String())
	if out.Code == 0 || !strings.Contains(out.Msg, "先把面板升级到 2.1.19") {
		t.Fatalf("old panel must refuse upgrading the agent above itself, got code=%d msg=%q", out.Code, out.Msg)
	}

	// 批量入口同口径：整批一次拒绝，逐条结果同带门禁原因。
	rec := postJSONToHandler(t, h.nodeBatchUpgrade, map[string]interface{}{"ids": []int64{nodeID}, "version": "2.1.19"})
	if body := rec.Body.String(); !strings.Contains(body, "先把面板升级到 2.1.19") {
		t.Fatalf("batch upgrade must refuse with the panel-upgrade reason, got body=%s", body)
	}
}
