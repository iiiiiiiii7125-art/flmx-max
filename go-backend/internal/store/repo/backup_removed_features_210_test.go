package repo

import (
	"encoding/json"

	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// 2.1.0 四删除面（Gist/联邦/主题/Snell）的旧备份恢复点名回执：
// 与分组三件套（noteRemovedGroupSegments）和 nodeGroups 同口径——
// 携带已删段的旧备份恢复时必须计数点名，不得在解码层静默丢弃
// （联邦 peerShares 段、三件套外的 themes 段），不得静默落库复
// 活（configs 段的 gist_* 键、inbounds 段的自建 Snell 内核）。
// 本用例走 JSON 序列化边界（备份文件真实形态），保证解码壳的
// json 标签本身受检。
func TestRemovedFeatureSegments210Receipt(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "removed-features-210.db")
	if err := dest.DB().Exec(`INSERT INTO node(id, name, secret, server_ip, port, created_time, updated_time, status) VALUES(10, 'n1', 's', '203.0.113.1', 0, ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}

	raw := `{
		"version": "1.0",
		"tunnelGroups": [{"id": 1, "name": "tg1", "status": 1, "createdTime": ` + itoa(int(now)) + `}],
		"peerShares": [
			{"id": 1, "name": "share-a", "nodeId": 10, "token": "tok-a", "createdTime": ` + itoa(int(now)) + `},
			{"id": 2, "name": "share-b", "nodeId": 10, "token": "tok-b", "createdTime": ` + itoa(int(now)) + `}
		],
		"themes": [{"id": "cyberpunk"}, {"id": "dark-pro"}],
		"configs": {
			"app_name": "panel-x",
			"custom_probe_key": "keep-me",
			"gist_token": "ghp_should_never_land",
			"gist_id": "12345",
			"notify_gist_failure_pending": "1"
		},
		"inbounds": {
			"kernels": [
				{"id": 21, "nodeId": 10, "kernelType": "snell", "version": "latest", "enabled": 1, "createdTime": ` + itoa(int(now)) + `},
				{"id": 22, "nodeId": 10, "kernelType": "singbox", "version": "latest", "enabled": 1, "createdTime": ` + itoa(int(now)) + `}
			],
			"inbounds": [
				{"id": 301, "kernelId": 21, "nodeId": 10, "name": "snell-self", "protocol": "snell", "listen": "::", "port": 8480, "enabled": 1, "createdTime": ` + itoa(int(now)) + `,
				 "settings": "{\"psk\":\"abc\"}", "transport": "{\"type\":\"tcp\"}", "security": "{\"type\":\"none\"}"},
				{"id": 302, "kernelId": 0, "nodeId": 10, "name": "snell-imported", "protocol": "snell", "listen": "::", "port": 8481, "enabled": 1, "createdTime": ` + itoa(int(now)) + `,
				 "extra": "{\"type\":\"snell\",\"server\":\"203.0.113.9\",\"port\":8389,\"psk\":\"xyz\",\"version\":5}"},
				{"id": 303, "kernelId": 22, "nodeId": 10, "name": "vless-ok", "protocol": "vless", "listen": "::", "port": 8443, "enabled": 1, "createdTime": ` + itoa(int(now)) + `}
			]
		}
	}`
	var backup model.BackupData
	if err := json.Unmarshal([]byte(raw), &backup); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	// 解码壳承接自检：四面数据必须被解码层接住（壳缺失时此处即
	// 为 0，后续计数断言无从谈起）。
	if len(backup.PeerShares) != 2 || len(backup.Themes) != 2 {
		t.Fatalf("decode shells must catch peerShares/themes, got %d/%d", len(backup.PeerShares), len(backup.Themes))
	}

	result, err := dest.Import(&backup, []string{"configs", "inbounds"})
	if err != nil {
		t.Fatalf("import with removed-feature segments: %v", err)
	}

	// 结构化计数与文本披露同数。
	if result.PeerSharesIgnored != 2 || result.ThemesIgnored != 2 || result.GistConfigsIgnored != 3 {
		t.Fatalf("ignored counts peerShares/themes/gist = %d/%d/%d, want 2/2/3",
			result.PeerSharesIgnored, result.ThemesIgnored, result.GistConfigsIgnored)
	}
	if result.TunnelGroupsIgnored != 1 {
		t.Fatalf("control segment tunnelGroups ignored = %d, want 1", result.TunnelGroupsIgnored)
	}
	if result.InboundSnellKernelsDisabled != 1 || result.InboundSnellInboundsIgnored != 1 {
		t.Fatalf("snell counts kernelsDisabled/inboundsIgnored = %d/%d, want 1/1",
			result.InboundSnellKernelsDisabled, result.InboundSnellInboundsIgnored)
	}
	for _, want := range []string{"peerShares", "themes", "gist_token", "Snell 内核（1 条）", "自建 Snell 入站（1 条）", "tunnelGroups"} {
		found := false
		for _, w := range result.ImportWarnings {
			if strings.Contains(w, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("import warnings must name %q, got %v", want, result.ImportWarnings)
		}
	}
	receipt, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	for _, want := range []string{`"peerSharesIgnored":2`, `"themesIgnored":2`, `"gistConfigsIgnored":3`,
		`"inboundSnellKernelsDisabled":1`, `"inboundSnellInboundsIgnored":1`} {
		if !strings.Contains(string(receipt), want) {
			t.Fatalf("receipt JSON missing %s: %s", want, receipt)
		}
	}

	// gist 键不得落库（值含凭据本体）；同段正常键照常恢复。
	for _, key := range []string{"gist_token", "gist_id", "notify_gist_failure_pending"} {
		var n int64
		if err := dest.DB().Model(&model.ViteConfig{}).Where("name = ?", key).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("removed gist config key %s must not land, found %d rows", key, n)
		}
	}
	if result.ConfigsImported != 2 {
		t.Fatalf("configs imported = %d, want 2 (app_name + custom_probe_key)", result.ConfigsImported)
	}

	// Snell 内核按禁用恢复；自建 Snell 入站不落库；导入型 Snell
	// 节点与普通入站照常恢复（保留面不受牵连）。
	var kernel21 model.NodeKernel
	if err := dest.DB().Where("id = ?", 21).First(&kernel21).Error; err != nil {
		t.Fatalf("snell kernel row must be kept (disabled): %v", err)
	}
	if kernel21.Enabled != 0 {
		t.Fatalf("snell kernel must be restored disabled, got enabled=%d", kernel21.Enabled)
	}
	var inboundCount int64
	if err := dest.DB().Model(&model.NodeInbound{}).Where("id = ?", 301).Count(&inboundCount).Error; err != nil {
		t.Fatal(err)
	}
	if inboundCount != 0 {
		t.Fatalf("self-built snell inbound must not land, found %d rows", inboundCount)
	}
	for _, id := range []int64{302, 303} {
		var n int64
		if err := dest.DB().Model(&model.NodeInbound{}).Where("id = ?", id).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("inbound %d must be restored normally, found %d rows", id, n)
		}
	}

	// 无已删段的导入：四面计数为 0、无凭空 warning。
	dest2 := openBackupRepo(t, "removed-features-210-clean.db")
	result2, err := dest2.Import(&model.BackupData{Version: "1.0"}, []string{"users"})
	if err != nil {
		t.Fatalf("clean import: %v", err)
	}
	if result2.PeerSharesIgnored != 0 || result2.ThemesIgnored != 0 || result2.GistConfigsIgnored != 0 ||
		result2.InboundSnellKernelsDisabled != 0 || result2.InboundSnellInboundsIgnored != 0 {
		t.Fatalf("clean import removed-feature counts must all be 0, got %+v", result2)
	}
	if len(result2.ImportWarnings) != 0 {
		t.Fatalf("clean import must not warn about removed features, got %v", result2.ImportWarnings)
	}
}
