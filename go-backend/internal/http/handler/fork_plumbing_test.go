package handler

import (
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// Fork 落地（Fix-F2：版本 plumbing）：1.14.4-flmx1 须通过全部 sing-box
// 版本门，-rc1 仍被拒；F3 安装面实测版本校验；F5 latest 拒绝；
// 伴生行经同一安装入口。

// TestForkVersionPassesAllSingboxGates：后缀剥离后 1.14.4-flmx1 通过
// 1.12 / 1.13 / 1.14 全部版本门，且 ACME / AnyTLS / Naive / Snell
// 渲染开关全开（开关值即 buildKernelApplyPayload 与 validateChainVersion
// 的计算口径：!versionBelowStrict）。
func TestForkVersionPassesAllSingboxGates(t *testing.T) {
	const fork = "1.14.4-flmx1"
	for _, floor := range [][2]int{{1, 12}, {1, 13}, {1, 14}} {
		if versionBelowStrict(fork, floor[0], floor[1]) {
			t.Errorf("versionBelowStrict(%q, %d, %d) must be false (fork passes the %d.%d floor)", fork, floor[0], floor[1], floor[0], floor[1])
		}
	}
	// 渲染开关：HTTPClient(1.14) / Snell(1.14) / Naive(1.13) 全开。
	if !(!versionBelowStrict(fork, 1, 14)) || !(!versionBelowStrict(fork, 1, 13)) {
		t.Error("fork must open SingboxHTTPClient / SingboxSnell (1.14) and SingboxNaive (1.13)")
	}
	// ACME(1.14) 与 AnyTLS(1.12) 自有门放行。
	acme := &kernel.Inbound{Protocol: kernel.ProtocolVLESS}
	acme.Security.CertMode = kernel.CertModeACME
	if msg := kernelVersionGate(kernel.TypeSingbox, fork, acme); msg != "" {
		t.Errorf("ACME gate must pass fork %q, got %q", fork, msg)
	}
	anytls := &kernel.Inbound{Protocol: kernel.ProtocolAnyTLS}
	if msg := kernelVersionGate(kernel.TypeSingbox, fork, anytls); msg != "" {
		t.Errorf("AnyTLS gate must pass fork %q, got %q", fork, msg)
	}
	// 出站 IP 策略门（1.12）放行。
	if err := validateOutboundIPVersion(kernel.TypeSingbox, fork, kernel.OutboundIPv4Only); err != nil {
		t.Errorf("outbound-IP gate must pass fork %q, got %v", fork, err)
	}
	// Naive 拨号门（1.13）与 Snell/Naive 链路渲染门放行。
	naive := &kernel.Endpoint{Inbound: kernel.Inbound{Protocol: kernel.ProtocolNaive}}
	if msg := naiveDialGate(kernel.TypeSingbox, fork, naive); msg != "" {
		t.Errorf("Naive dial gate must pass fork %q, got %q", fork, msg)
	}
	opts := kernel.RenderOptions{
		SingboxSnell:      !versionBelowStrict(fork, 1, 14),
		SingboxNaive:      !versionBelowStrict(fork, 1, 13),
		SingboxHTTPClient: !versionBelowStrict(fork, 1, 14),
	}
	snell := kernel.Endpoint{Host: "203.0.113.9", Port: 8388,
		Inbound: kernel.Inbound{Protocol: kernel.ProtocolSnell, Port: 8388,
			Settings: kernel.Settings{Password: "snell-psk"}}}
	// 自建 Snell 目标本就无法渲染为链式出站（singboxOutbound 无自建
	// Snell 分支），Snell 开关的断言是版本门文案：fork 派生的
	// SingboxSnell=true 时不得报版本门错误；开关关时必须报版本门错误。
	if err := kernel.CheckChainWith(kernel.TypeSingbox, snell, opts); err != nil && strings.Contains(err.Error(), "1.14 及以上才能连接 Snell 节点") {
		t.Errorf("Snell chain check with fork-derived opts must not hit the version gate, got %v", err)
	}
	closedOpts := opts
	closedOpts.SingboxSnell = false
	if err := kernel.CheckChainWith(kernel.TypeSingbox, snell, closedOpts); err == nil || !strings.Contains(err.Error(), "1.14 及以上才能连接 Snell 节点") {
		t.Errorf("Snell chain check with SingboxSnell=false must hit the version gate, got %v", err)
	}
	if err := kernel.CheckChainWith(kernel.TypeSingbox, *naive, opts); err != nil {
		t.Errorf("Naive chain render must pass fork %q, got %v", fork, err)
	}
	// 对照：-rc1 仍被全部两段门拒绝。
	for _, v := range []string{"1.14.4-rc1", "1.14.0-rc1"} {
		if msg := kernelVersionGate(kernel.TypeSingbox, v, acme); msg == "" {
			t.Errorf("ACME gate must still reject %q", v)
		}
		if msg := kernelVersionGate(kernel.TypeSingbox, v, anytls); msg == "" {
			t.Errorf("AnyTLS gate must still reject %q", v)
		}
	}
}

// TestKernelApplyVersionMismatch 钉死 F3 纯函数：实测版本须包含请求版本。
func TestKernelApplyVersionMismatch(t *testing.T) {
	cases := []struct {
		kernelType, requested, reported string
		wantMismatch                    bool
	}{
		{kernel.TypeSingbox, "1.14.4-flmx1", "sing-box version 1.14.4-flmx1 linux/amd64", false},
		{kernel.TypeSingbox, "1.14.4-flmx1", "sing-box version 1.14.4 linux/amd64", true}, // 上游构建缺 -flmx1
		{kernel.TypeSingbox, "1.14.4-flmx1", "sing-box version dev linux/amd64", true},    // fork 构建输出 dev
		{kernel.TypeSingbox, "1.14.4-flmx1", "", true},                                    // 探不到版本不得按成功确认
		{kernel.TypeSingbox, "1.14.4", "sing-box version 1.14.4-flmx1 linux/amd64", false},
		{kernel.TypeSingboxCompanion, "1.14.4-flmx1", "sing-box version 1.14.4-flmx1 linux/amd64", false},
		{kernel.TypeSingboxCompanion, "1.14.4-flmx1", "sing-box version dev linux/amd64", true},
		{kernel.TypeSingbox, "latest", "sing-box version dev linux/amd64", false}, // 哨兵不校验
		{kernel.TypeSingbox, "", "sing-box version dev linux/amd64", false},
		{kernel.TypeXray, "26.11.9", "v25.1.1", false}, // 非 sing-box 同族不在此校验内
	}
	for _, c := range cases {
		got := kernelApplyVersionMismatch(c.kernelType, c.requested, c.reported)
		if (got != "") != c.wantMismatch {
			t.Errorf("kernelApplyVersionMismatch(%q, %q, %q) = %q, wantMismatch=%v", c.kernelType, c.requested, c.reported, got, c.wantMismatch)
		}
	}
}

// TestForkInstallRejectsLatestForSingbox：F5——sing-box 安装拒绝字面 latest。
func TestForkInstallRejectsLatestForSingbox(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fork-latest.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "fork-latest", "198.51.100.61")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14.3")
	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "latest")
	if code == 0 {
		t.Fatal("sing-box install of \"latest\" must be refused (F5)")
	}
	if !strings.Contains(msg, "不支持 latest") {
		t.Fatalf("refusal must name the latest rejection, got %q", msg)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14.3" {
		t.Fatalf("refused install must not rewrite the stored version, got %q", after.Version)
	}
}

// TestForkCompanionInstallEntry：伴生行经同一安装入口同升——
// Lookup 的 singbox-companion 特例共用 sing-box 下载信息与 fork 仓，
// 预检走 fork 仓 tag，离线节点上版本登记照常落库为待安装意图。
func TestForkCompanionInstallEntry(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v1.14.4-flmx1": 200})
	r, err := repo.Open(t.TempDir() + "/fork-comp-entry.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "fork-comp-entry", "198.51.100.62")
	seedB8Kernel(t, r, nodeID, kernel.TypeSingboxCompanion, "1.14.3")
	code, _ := postKernelInstall(t, h, nodeID, kernel.TypeSingboxCompanion, "1.14.4-flmx1")
	if code == 0 {
		t.Fatal("companion install on an offline node must fail at apply (pending face expected)")
	}
	after, err := r.GetNodeKernelByType(nodeID, kernel.TypeSingboxCompanion)
	if err != nil || after == nil {
		t.Fatalf("companion row must survive the offline install: %v", err)
	}
	if after.Version != "1.14.4-flmx1" {
		t.Fatalf("companion install must store the fork version as intent, got %q", after.Version)
	}
}

// TestForkInstallVersionMismatchRollsBack：F3 安装级——mock agent 回执
// 版本与请求版本不一致时安装失败、版本登记回滚，实测版本不落库。
func TestForkInstallVersionMismatchRollsBack(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v1.14.4-flmx1": 200})
	h, r, _, _, k, nodeID := setupKernelNotifyRig(t)
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{
		"version": "1.14.3", "installed_version": "sing-box version 1.14.3",
	}); err != nil {
		t.Fatal(err)
	}
	// 把 rig 的 xray 行改作 sing-box 行：mock 回执版本恒为 v25.1.1，
	// 与请求的 1.14.4-flmx1 不一致。
	if err := r.DB().Exec("UPDATE node_kernel SET kernel_type=? WHERE id=?", kernel.TypeSingbox, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "1.14.4-flmx1")
	if code == 0 {
		t.Fatal("install whose reported version mismatches must fail (F3)")
	}
	if !strings.Contains(msg, "不一致") || !strings.Contains(msg, "已回滚") {
		t.Fatalf("failure receipt must name the mismatch and the rollback, got %q", msg)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14.3" {
		t.Fatalf("failed install must roll the version back to 1.14.3, got %q", after.Version)
	}
	if after.Status != kernelStatusError {
		t.Fatalf("mismatched install must leave status error, got %q", after.Status)
	}
	if after.InstalledVersion != "sing-box version 1.14.3" {
		t.Fatalf("mismatched reported version must not overwrite installed_version, got %q", after.InstalledVersion)
	}
}

// TestForkInstallVersionMatchSucceeds：F3 对照组——mock 回执版本包含
// 请求版本时安装成功、实测版本落库。
func TestForkInstallVersionMatchSucceeds(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v1.14.4-flmx1": 200})
	h, r, mock, _, k, nodeID := setupKernelNotifyRig(t)
	mock.setApplyVersion("sing-box version 1.14.4-flmx1 linux/amd64")
	if err := r.DB().Exec("UPDATE node_kernel SET kernel_type=? WHERE id=?", kernel.TypeSingbox, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "1.14.4-flmx1")
	if code != 0 {
		t.Fatalf("install with a matching reported version must succeed, got code=%d msg=%q", code, msg)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14.4-flmx1" || !strings.Contains(after.InstalledVersion, "1.14.4-flmx1") {
		t.Fatalf("matching install must store version=%q installed=%q", after.Version, after.InstalledVersion)
	}
}
