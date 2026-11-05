package handler

import (
	"encoding/json"
	"testing"

	"go-backend/internal/http/client"
)

// B6-05：远端用量同步此前把固定键集整体 Marshal 后无条件覆写
// RemoteConfig，relay 凭据直接取 Connect 回执原值——回执不带凭据
// （旧提供方/提供方凭据补发失败）时本地存量凭据被抹成空串，建链
// 断裂直至下一次带凭据的同步自愈；与状态同步/建隧道两路共用的
// mergeRemoteConnectCredentials「空值保留存量」口径相悖。本文件钉
// 合并口径：用量字段照常刷新，凭据仅在回执非空时覆写。

func TestB605UsageSyncRetainsStoredCredentialsOnEmptyReply(t *testing.T) {
	stored := `{"shareId":7,"maxBandwidth":100,"currentFlow":5,"relayUsername":"relay-user","relayPassword":"relay-secret","panelVersion":"1.9.8","customKey":"keep-me"}`
	info := &client.RemoteNodeInfo{
		ShareID: 7, MaxBandwidth: 200, CurrentFlow: 42, ExpiryTime: 1893456000000,
		PortRangeStart: 31000, PortRangeEnd: 32000,
		// RelayUsername/RelayPassword 留空：模拟旧提供方回执。
		PanelVersion: "1.9.9",
	}
	merged := mergeRemoteUsageSyncConfig(stored, info)
	user, pass := parseRemoteShareRelayAuth(merged)
	if user != "relay-user" || pass != "relay-secret" {
		t.Fatalf("stored relay credentials were wiped by an empty-credential reply: user=%q pass=%q merged=%s", user, pass, merged)
	}
	shareID, maxBandwidth, currentFlow, expiryTime, portRangeStart, portRangeEnd := parseRemoteShareUsageConfig(merged)
	if shareID != 7 || maxBandwidth != 200 || currentFlow != 42 || expiryTime != 1893456000000 || portRangeStart != 31000 || portRangeEnd != 32000 {
		t.Fatalf("usage fields not refreshed: merged=%s", merged)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(merged), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["customKey"] != "keep-me" {
		t.Fatalf("unrelated stored keys were dropped by usage sync: merged=%s", merged)
	}
	if cfg["panelVersion"] != "1.9.9" {
		t.Fatalf("panel version not refreshed: merged=%s", merged)
	}
}

func TestB605UsageSyncAdoptsReplyCredentialsWhenPresent(t *testing.T) {
	stored := `{"shareId":7,"relayUsername":"old-user","relayPassword":"old-secret"}`
	info := &client.RemoteNodeInfo{ShareID: 7, RelayUsername: "new-user", RelayPassword: "new-secret"}
	merged := mergeRemoteUsageSyncConfig(stored, info)
	user, pass := parseRemoteShareRelayAuth(merged)
	if user != "new-user" || pass != "new-secret" {
		t.Fatalf("reply credentials were not adopted: user=%q pass=%q merged=%s", user, pass, merged)
	}
}

func TestB605UsageSyncFromEmptyConfig(t *testing.T) {
	info := &client.RemoteNodeInfo{ShareID: 9, MaxBandwidth: 10}
	merged := mergeRemoteUsageSyncConfig("", info)
	shareID, maxBandwidth, _, _, _, _ := parseRemoteShareUsageConfig(merged)
	if shareID != 9 || maxBandwidth != 10 {
		t.Fatalf("usage fields missing from fresh config: merged=%s", merged)
	}
	user, pass := parseRemoteShareRelayAuth(merged)
	if user != "" || pass != "" {
		t.Fatalf("phantom credentials in fresh config: user=%q pass=%q", user, pass)
	}
}
