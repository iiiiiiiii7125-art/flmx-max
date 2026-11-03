package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R2-F2-A2 钉格："latest" 再装静默空转。面板必须在下发前把 "latest"
// 解析为具体版本并落库；存量 "latest" 行在下发入口收敛；同版本重装
// 回执点名。修复前：落库恒为字面 "latest"，agent 按 bin/<type>/latest
// 短路跳过下载，回执却报成功。

const fixR2A2ReleasesPayload = `[
{"tag_name":"v1.15.0-rc.1","draft":false,"prerelease":true,"published_at":"2026-02-01T00:00:00Z"},
{"tag_name":"v1.14.3","draft":false,"prerelease":false,"published_at":"2026-01-20T00:00:00Z"},
{"tag_name":"v1.14.2","draft":false,"prerelease":false,"published_at":"2026-01-05T00:00:00Z"}
]`

func TestFixR2A2ResolveLatestSkipsPrerelease(t *testing.T) {
	stubReleasesAPI(t, fixR2A2ReleasesPayload)
	resetKernelVersionCache(t)
	info, ok := kernel.Lookup(kernel.TypeSingbox)
	if !ok {
		t.Fatal("sing-box info missing")
	}
	v, err := resolveLatestConcreteVersion(info)
	if err != nil {
		t.Fatalf("resolveLatestConcreteVersion: %v", err)
	}
	if v != "1.14.3" {
		t.Fatalf("latest must resolve to the newest stable release 1.14.3 (prerelease skipped), got %q", v)
	}
}

func TestFixR2A2KernelInstallLatestStoresConcrete(t *testing.T) {
	stubReleasesAPI(t, fixR2A2ReleasesPayload)
	resetKernelVersionCache(t)
	r, err := repo.Open(t.TempDir() + "/fixa2.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	// 存量行：版本存的是字面 "latest"，节点实际在跑 1.14.2。
	nodeID := seedRelayTargetNode(t, r, "fixa2-node", "198.51.100.21")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "latest")
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"installed_version": "1.14.2"}); err != nil {
		t.Fatal(err)
	}
	// 节点离线，下发必然失败（回执为下发失败面），但落库版本必须在
	// 下发前就已收敛为具体版本——修复前此处恒为 "latest"。
	_, _ = postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "latest")
	after, err := r.GetNodeKernel(k.ID)
	if err != nil || after == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	if after.Version != "1.14.3" {
		t.Fatalf("installing \"latest\" must store the resolved concrete version 1.14.3, stored %q", after.Version)
	}

	// 新装路径同口径：无存量行时以 "latest" 安装，落库具体版本。
	node2 := seedRelayTargetNode(t, r, "fixa2-node2", "198.51.100.22")
	_, _ = postKernelInstall(t, h, node2, kernel.TypeSingbox, "latest")
	k2, err := r.GetNodeKernelByType(node2, kernel.TypeSingbox)
	if err != nil || k2 == nil {
		t.Fatalf("reload created kernel: %v", err)
	}
	if k2.Version != "1.14.3" {
		t.Fatalf("fresh install of \"latest\" must store 1.14.3, stored %q", k2.Version)
	}
}

func TestFixR2A2ApplyKernelConvergesStoredLatest(t *testing.T) {
	stubReleasesAPI(t, fixR2A2ReleasesPayload)
	resetKernelVersionCache(t)
	r, err := repo.Open(t.TempDir() + "/fixa2-apply.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "fixa2-apply-node", "198.51.100.23")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "latest")
	// 节点离线：apply 本身会失败，但版本收敛发生在下发之前。
	_ = h.applyKernel(k.ID, false)
	after, err := r.GetNodeKernel(k.ID)
	if err != nil || after == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	if after.Version != "1.14.3" {
		t.Fatalf("apply must converge a stored \"latest\" to the concrete 1.14.3 before dispatch, stored %q", after.Version)
	}
}

func TestFixR2A2SameVersionInstallWarning(t *testing.T) {
	cases := []struct {
		name          string
		latest        bool
		prevInstalled string
		version       string
		want          string
	}{
		{"latest already newest", true, "sing-box 1.14.3", "1.14.3", "已是最新版 1.14.3，无需更新"},
		{"concrete reinstall", false, "1.14.3", "1.14.3", "已安装该版本 1.14.3，本次未重新下载"},
		{"upgrade is silent-free", true, "1.14.2", "1.14.3", ""},
		{"first install", false, "", "1.14.3", ""},
	}
	for _, c := range cases {
		if got := sameVersionInstallWarning(c.latest, c.prevInstalled, c.version); got != c.want {
			t.Fatalf("%s: sameVersionInstallWarning(%v, %q, %q) = %q, want %q", c.name, c.latest, c.prevInstalled, c.version, got, c.want)
		}
	}
}

// R2-F2-C-L01 钉格：目标侧编辑分享地址造出站策略冲突必须在保存期被拒。
// 引用方以 ipv6_only 链向目标（初始分享地址为 IPv6 字面量，合法）；
// 把目标分享地址改为 IPv4 字面量若在保存期放行，引用方链式流量将在
// 渲染期被静默丢弃（F2-N2-01 的目标侧镜像）。
func TestFixR2CL01TargetShareEditReferrerPolicy(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fixcl01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	front := seedRelayTargetNode(t, r, "cl01-front", "198.51.100.31")
	landing := seedRelayTargetNode(t, r, "cl01-land", "203.0.113.31")
	frontBox := seedChainKernel(t, r, front, kernel.TypeSingbox)
	landXray := seedChainKernel(t, r, landing, kernel.TypeXray)
	target := seedChainInbound(t, r, landXray, "cl01-target", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388}, 0)
	// 目标初始分享地址：IPv6 字面量（auto 模式＋ShareHost 非空＝custom）。
	stored, err := r.GetNodeInbound(target.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload target: %v", err)
	}
	stored.ShareHost = "2001:db8::9"
	if err := r.SaveNodeInbound(stored); err != nil {
		t.Fatal(err)
	}
	seed := seedChainInbound(t, r, frontBox, "cl01-seed", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8389}, 0)
	seedIn, err := inboundFromModel(*seed)
	if err != nil {
		t.Fatal(err)
	}
	// 引用方：ipv6_only 链向目标，初始组合合法、保存应成功。
	refBody := inboundRequest{
		NodeID: front, KernelType: kernel.TypeSingbox, Name: "cl01-referrer",
		Protocol: kernel.ProtocolShadowsocks, Port: 8395,
		Settings: seedIn.Settings, Security: seedIn.Security,
		OutboundID: target.ID, OutboundIPStrategy: strPtr(kernel.OutboundIPv6Only),
	}
	raw, err := json.Marshal(refBody)
	if err != nil {
		t.Fatal(err)
	}
	if out := callInboundSave(t, h, false, string(raw)); out.Code != 0 {
		t.Fatalf("ipv6_only referrer + IPv6-literal target must save: code=%d msg=%q", out.Code, out.Msg)
	}

	updateTarget := func(shareHost string) (int, string) {
		cur, err := r.GetNodeInbound(target.ID)
		if err != nil || cur == nil {
			t.Fatalf("reload target: %v", err)
		}
		curIn, err := inboundFromModel(*cur)
		if err != nil {
			t.Fatal(err)
		}
		upd := inboundRequest{
			ID: target.ID, Name: cur.Name, Protocol: curIn.Protocol, Listen: curIn.Listen,
			Port: cur.Port, Settings: curIn.Settings, Transport: curIn.Transport, Security: curIn.Security,
			ShareHost: shareHost,
		}
		raw, err := json.Marshal(upd)
		if err != nil {
			t.Fatal(err)
		}
		out := callInboundSave(t, h, true, string(raw))
		return out.Code, out.Msg
	}

	// 目标侧改 IPv4 字面量：与引用方 ipv6_only 冲突，保存期必须拒绝
	// 并点名，且不得落库。
	code, msg := updateTarget("203.0.113.99")
	if code == 0 {
		t.Fatal("target edit to an IPv4 literal conflicting with a referrer's ipv6_only strategy must be rejected at save")
	}
	if !strings.Contains(msg, "链式下一跳") || !strings.Contains(msg, "仅 IPv6 出站无法连接 IPv4 地址") {
		t.Fatalf("rejection must name the referrer chain conflict, got %q", msg)
	}
	after, err := r.GetNodeInbound(target.ID)
	if err != nil || after == nil {
		t.Fatalf("reload target after rejection: %v", err)
	}
	if after.ShareHost != "2001:db8::9" {
		t.Fatalf("rejected edit must not persist, stored share host %q", after.ShareHost)
	}

	// 对照：改另一个 IPv6 字面量不冲突，应放行并落库。
	if code, msg := updateTarget("2001:db8::10"); code != 0 {
		t.Fatalf("non-conflicting target edit must save: code=%d msg=%q", code, msg)
	}
	after, err = r.GetNodeInbound(target.ID)
	if err != nil || after == nil {
		t.Fatalf("reload target after ok edit: %v", err)
	}
	if after.ShareHost != "2001:db8::10" {
		t.Fatalf("accepted edit must persist, stored share host %q", after.ShareHost)
	}
}

// R2-F2-B-L01 钉格：批量启停 ids 未去重时，重复 id 第二次命中「已是
// 目标状态」分支再计一次成功，SuccessCount 虚高。修复后按去重计数。
func TestFixR2BL01BatchToggleDedupesIDs(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fixbl01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "bl01-node", "198.51.100.41")
	k := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	a := seedChainInbound(t, r, k, "bl01-a", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8401}, 0)
	b := seedChainInbound(t, r, k, "bl01-b", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8402}, 0)

	body := `{"ids":[` + strconv.FormatInt(a.ID, 10) + `,` + strconv.FormatInt(a.ID, 10) + `,` + strconv.FormatInt(b.ID, 10) + `],"enabled":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound/batch-toggle", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.inboundBatchToggle(rec, req)
	var env struct {
		Code int                  `json:"code"`
		Msg  string               `json:"msg"`
		Data batchOperationResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode batch toggle response: %v (body=%s)", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("batch toggle failed: code=%d msg=%q", env.Code, env.Msg)
	}
	if env.Data.SuccessCount != 2 || env.Data.FailCount != 0 {
		t.Fatalf("duplicate ids must count once: success=%d fail=%d, want 2/0", env.Data.SuccessCount, env.Data.FailCount)
	}
}
