package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// portAllowOf 从 KernelApply 载荷的 ports 表里取指定入站的 allow
// 列表（无白名单时 ok=false）。
func portAllowOf(t *testing.T, payload map[string]interface{}, inboundID int64) ([]string, bool) {
	t.Helper()
	ports, ok := payload["ports"].([]map[string]interface{})
	if !ok {
		t.Fatalf("ports type = %T, want []map[string]interface{}", payload["ports"])
	}
	for _, p := range ports {
		id, _ := p["inboundId"].(int64)
		if id != inboundID {
			continue
		}
		allow, ok := p["allow"].([]string)
		return allow, ok
	}
	t.Fatalf("inbound %d not in payload ports", inboundID)
	return nil, false
}

func allowContains(t *testing.T, allow []string, want string) {
	t.Helper()
	for _, a := range allow {
		if a == want {
			return
		}
	}
	t.Fatalf("allow = %v, want it to contain %q", allow, want)
}

func allowNotContains(t *testing.T, allow []string, unwanted string) {
	t.Helper()
	for _, a := range allow {
		if a == unwanted {
			t.Fatalf("allow = %v, must not contain %q", allow, unwanted)
		}
	}
}

// TestEffectiveInboundWhitelistNodeOverride 钉节点级白名单覆盖层：
// 开→全部入站生效节点列表（逐入站存量字段不动）；关→逐入站照旧；
// 新入站渲染时合并即自动继承。
func TestEffectiveInboundWhitelistNodeOverride(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/node-wl.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	node := seedRelayTargetNode(t, r, "wl-node", "198.51.100.30")
	k := seedChainKernel(t, r, node, kernel.TypeSingbox)

	first := seedU5Inbound(t, r, k, "first", 8443, true, 0)
	first.RelayWhitelist = 1
	first.WhitelistExtra = "10.0.0.1"
	if err := r.SaveNodeInbound(first); err != nil {
		t.Fatal(err)
	}
	plain := seedU5Inbound(t, r, k, "plain", 8444, true, 0)

	// 关：逐入站照旧。
	if on, extra, err := h.effectiveInboundWhitelist(*first); err != nil || !on || extra != "10.0.0.1" {
		t.Fatalf("node off: effective(first) = (%v, %q, %v), want (true, %q, nil)", on, extra, err, "10.0.0.1")
	}
	if on, _, err := h.effectiveInboundWhitelist(*plain); err != nil || on {
		t.Fatalf("node off: effective(plain) = (%v, %v), want (false, nil)", on, err)
	}
	payload, _, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	if allow, ok := portAllowOf(t, payload, first.ID); !ok {
		t.Fatal("node off: first must carry an allow list (per-inbound whitelist)")
	} else {
		allowContains(t, allow, "10.0.0.1/32")
	}
	if _, ok := portAllowOf(t, payload, plain.ID); ok {
		t.Fatal("node off: plain must not carry an allow list")
	}

	// 开：全部入站生效节点列表，逐入站存量被覆盖（字段本身不动）。
	if err := r.UpdateNodeWhitelist(node, 1, "203.0.113.0/24"); err != nil {
		t.Fatal(err)
	}
	if on, extra, err := h.effectiveInboundWhitelist(*first); err != nil || !on || extra != "203.0.113.0/24" {
		t.Fatalf("node on: effective(first) = (%v, %q, %v), want (true, %q, nil)", on, extra, err, "203.0.113.0/24")
	}
	if on, extra, err := h.effectiveInboundWhitelist(*plain); err != nil || !on || extra != "203.0.113.0/24" {
		t.Fatalf("node on: effective(plain) = (%v, %q, %v), want (true, %q, nil)", on, extra, err, "203.0.113.0/24")
	}
	stored, err := r.GetNodeInbound(first.ID)
	if err != nil || stored == nil || stored.RelayWhitelist != 1 || stored.WhitelistExtra != "10.0.0.1" {
		t.Fatalf("node on: stored per-inbound fields must stay untouched, got %+v (err %v)", stored, err)
	}
	payload, _, err = h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first.ID, plain.ID} {
		allow, ok := portAllowOf(t, payload, id)
		if !ok {
			t.Fatalf("node on: inbound %d must carry an allow list", id)
		}
		allowContains(t, allow, "203.0.113.0/24")
		allowNotContains(t, allow, "10.0.0.1/32")
	}

	// 新入站（开之后创建、逐入站白名单关）渲染时即继承节点列表。
	late := seedU5Inbound(t, r, k, "late", 8445, true, 0)
	payload, _, err = h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	allow, ok := portAllowOf(t, payload, late.ID)
	if !ok {
		t.Fatal("node on: late inbound must inherit the node allow list")
	}
	allowContains(t, allow, "203.0.113.0/24")

	// 再关：逐入站照旧（first 回到 10.0.0.1/32，plain 无名单）。
	if err := r.UpdateNodeWhitelist(node, 0, "203.0.113.0/24"); err != nil {
		t.Fatal(err)
	}
	payload, _, err = h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	allow, ok = portAllowOf(t, payload, first.ID)
	if !ok {
		t.Fatal("node off again: first must carry its per-inbound allow list")
	}
	allowContains(t, allow, "10.0.0.1/32")
	if _, ok := portAllowOf(t, payload, plain.ID); ok {
		t.Fatal("node off again: plain must not carry an allow list")
	}
}

// TestNodeWhitelistSettingsFromReq 钉节点级白名单的请求面解析：
// 非法条目整单拒绝，合法条目归一为规范串，空串合法。
func TestNodeWhitelistSettingsFromReq(t *testing.T) {
	if _, _, _, _, err := nodeWhitelistSettingsFromReq(map[string]interface{}{"whitelistExtra": "not-an-ip"}); err == nil {
		t.Fatal("invalid whitelist entry must be rejected")
	}
	if _, _, _, _, err := nodeWhitelistSettingsFromReq(map[string]interface{}{"whitelistExtra": "203.0.113.1, 10.0.0.0/8"}); err != nil {
		t.Fatalf("valid list rejected: %v", err)
	}
	enabled, enabledPresent, extra, extraPresent, err := nodeWhitelistSettingsFromReq(map[string]interface{}{
		"whitelistEnabled": true, "whitelistExtra": "203.0.113.1, 10.0.0.0/8",
	})
	if err != nil || !enabledPresent || !extraPresent || enabled != 1 || extra != "203.0.113.1/32, 10.0.0.0/8" {
		t.Fatalf("parse = (%d, %v, %q, %v, %v), want (1, true, %q, true, nil)", enabled, enabledPresent, extra, extraPresent, err, "203.0.113.1/32, 10.0.0.0/8")
	}
	if _, _, extra, _, err := nodeWhitelistSettingsFromReq(map[string]interface{}{"whitelistExtra": ""}); err != nil || extra != "" {
		t.Fatalf("empty list = (%q, %v), want (\"\", nil)", extra, err)
	}
	if enabled, present, _, _, err := nodeWhitelistSettingsFromReq(map[string]interface{}{"whitelistEnabled": false}); err != nil || !present || enabled != 0 {
		t.Fatalf("explicit false = (%d, %v, %v), want (0, true, nil)", enabled, present, err)
	}
}

// TestNodeUpdateWhitelistAndConnLog 钉 nodeUpdate 写面：非法白名单
// 列表整单拒绝且不落库；合法开关＋列表落库；连接日志开关落库。
func TestNodeUpdateWhitelistAndConnLog(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/node-upd-wl.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	now := time.Now().UnixMilli()
	nodeID, err := r.CreateNode("upd-node", "upd-secret", "198.51.100.40", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, nil, "")
	if err != nil {
		t.Fatal(err)
	}

	env := kr1CallNodeUpdate(t, h, map[string]interface{}{"id": nodeID, "whitelistExtra": "bogus!!!"})
	if env.Code == 0 {
		t.Fatalf("invalid whitelist list must be rejected, got %+v", env)
	}
	node, err := r.GetNodeByID(nodeID)
	if err != nil || node == nil || node.WhitelistExtra != "" || node.WhitelistEnabled != 0 {
		t.Fatalf("rejected update must not land, node = %+v (err %v)", node, err)
	}

	env = kr1CallNodeUpdate(t, h, map[string]interface{}{"id": nodeID, "whitelistEnabled": true, "whitelistExtra": "203.0.113.0/24"})
	if env.Code != 0 {
		t.Fatalf("valid whitelist update code = %d (%s), want 0", env.Code, env.Msg)
	}
	node, err = r.GetNodeByID(nodeID)
	if err != nil || node == nil || node.WhitelistEnabled != 1 || node.WhitelistExtra != "203.0.113.0/24" {
		t.Fatalf("whitelist not stored, node = %+v (err %v)", node, err)
	}

	env = kr1CallNodeUpdate(t, h, map[string]interface{}{"id": nodeID, "connLogEnabled": true})
	if env.Code != 0 {
		t.Fatalf("connlog switch update code = %d (%s), want 0", env.Code, env.Msg)
	}
	node, err = r.GetNodeByID(nodeID)
	if err != nil || node == nil || node.ConnLogEnabled != 1 {
		t.Fatalf("connlog switch not stored, node = %+v (err %v)", node, err)
	}
}

// TestWhitelistReconcileFailureNote 钉白名单同步回执的点名口径：
// 离线类失败不算失败，真失败点名内核＋原因。
func TestWhitelistReconcileFailureNote(t *testing.T) {
	if got := whitelistReconcileFailureNote([]kernelReconcileFailure{
		{KernelType: "sing-box", Err: errors.New("节点不在线")},
	}); got != "" {
		t.Fatalf("offline failure must be filtered, got %q", got)
	}
	got := whitelistReconcileFailureNote([]kernelReconcileFailure{
		{KernelType: "sing-box", Err: errors.New("nftables: apt-get install failed: 404 Not Found")},
		{KernelType: "xray", Err: errors.New("节点不在线")},
	})
	if !strings.Contains(got, "sing-box") || !strings.Contains(got, "nftables") || strings.Contains(got, "xray") {
		t.Fatalf("note = %q, want sing-box + nftables reason only", got)
	}
	if got := whitelistReconcileFailureNote([]kernelReconcileFailure{
		{KernelType: "", Err: errors.New("db gone")},
	}); !strings.Contains(got, "节点内核查询失败") {
		t.Fatalf("empty-kernel failure note = %q, want the kernel-list failure named", got)
	}
	if got := whitelistReconcileFailureNote(nil); got != "" {
		t.Fatalf("no failures note = %q, want empty", got)
	}
}

// TestIngestNodeNftablesStatus 钉 agent nftables 运行态的摄入与
// 节点响应字段：失败点名原因、自动安装与已安装分态、未上报不写。
func TestIngestNodeNftablesStatus(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/node-nft.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	now := time.Now().UnixMilli()
	nodeID, err := r.CreateNode("nft-node", "nft-secret", "198.51.100.41", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	readStatus := func() (string, string) {
		t.Helper()
		node, err := r.GetNodeByID(nodeID)
		if err != nil || node == nil {
			t.Fatalf("get node: %v", err)
		}
		return node.NftablesStatus, node.NftablesError
	}

	// 未上报：不写（"" 仍是未知，不把“没上报”当成失败）。
	h.ingestNodeNftablesStatus(nodeID, agentKernelStatus{})
	if st, _ := readStatus(); st != "" {
		t.Fatalf("no report: status = %q, want empty (unknown)", st)
	}

	h.ingestNodeNftablesStatus(nodeID, agentKernelStatus{NftAvailable: true})
	if st, e := readStatus(); st != "installed" || e != "" {
		t.Fatalf("installed: (%q, %q), want (installed, \"\")", st, e)
	}

	h.ingestNodeNftablesStatus(nodeID, agentKernelStatus{NftAvailable: true, NftAutoInstalled: true})
	if st, e := readStatus(); st != "auto_installed" || e != "" {
		t.Fatalf("auto-installed: (%q, %q), want (auto_installed, \"\")", st, e)
	}

	reason := "nftables: apt-get install failed: 404 Not Found"
	h.ingestNodeNftablesStatus(nodeID, agentKernelStatus{NftAvailable: false, NftError: reason})
	if st, e := readStatus(); st != "failed" || e != reason {
		t.Fatalf("failed: (%q, %q), want (failed, %q)", st, e, reason)
	}

	// 节点响应（ListNodes）带全部新字段。
	rows, err := r.ListNodes()
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListNodes = (%v, %v), want 1 row", rows, err)
	}
	row := rows[0]
	if row["nftablesStatus"] != "failed" || row["nftablesError"] != reason {
		t.Fatalf("ListNodes nft fields = (%v, %v), want (failed, %q)", row["nftablesStatus"], row["nftablesError"], reason)
	}
	if row["connLogEnabled"] != 0 || row["whitelistEnabled"] != 0 || row["whitelistExtra"] != "" {
		t.Fatalf("ListNodes connlog/whitelist fields = (%v, %v, %v), want (0, 0, \"\")", row["connLogEnabled"], row["whitelistEnabled"], row["whitelistExtra"])
	}

	// 同态重摄入不改写（幂等、无 churn）。
	h.ingestNodeNftablesStatus(nodeID, agentKernelStatus{NftAvailable: false, NftError: reason})
	if st, e := readStatus(); st != "failed" || e != reason {
		t.Fatalf("re-ingest same: (%q, %q), want unchanged", st, e)
	}
}
