package handler

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// FIX-KCPMEM（P1-04/P1-05）回归门：KCP 窗口/缓冲按节点内存档位
// 封顶（大档不低于现行值）、无声明恒走保守缺省档、agent 缺省对
// 齐面、FEC 关闭档生成形态（不下发 shard 键＋fec_off 标记）。

func TestFixKCPMemTierTable(t *testing.T) {
	cases := []struct {
		memMB int
		want  kcpMemTier
	}{
		{-1, kcpTierSmall}, {0, kcpTierSmall}, {256, kcpTierSmall},
		{512, kcpTierSmall}, {513, kcpTierMedium}, {1024, kcpTierMedium},
		{2048, kcpTierMedium}, {2049, kcpTierLarge}, {8192, kcpTierLarge},
	}
	for _, c := range cases {
		if got := kcpMemTierForMB(c.memMB); got != c.want {
			t.Fatalf("mem %d MiB: got tier %s, want %s", c.memMB, got.name, c.want.name)
		}
	}
	// large 档必须逐字等于本修复前的恒定下发值（大内存不降级）。
	if kcpTierLarge.sndWnd != 4096 || kcpTierLarge.rcvWnd != 4096 ||
		kcpTierLarge.sockBuf != 4194304 || kcpTierLarge.smuxBuf != 4194304 ||
		kcpTierLarge.streamBuf != 2097152 {
		t.Fatalf("large tier drifted from pre-fix constants: %+v", kcpTierLarge)
	}
	// 档位单调不减（内存越大参数不越小）。
	if kcpTierSmall.sndWnd > kcpTierMedium.sndWnd || kcpTierMedium.sndWnd > kcpTierLarge.sndWnd ||
		kcpTierSmall.sockBuf > kcpTierMedium.sockBuf || kcpTierMedium.sockBuf > kcpTierLarge.sockBuf {
		t.Fatal("tiers must be monotonic non-decreasing")
	}
	// 单会话最坏内存核算（与 kcp_tuning.go 档位表注释同口径）：
	// (snd+rcv)×1350 + sockbuf 收发两向 + smuxbuf + streambuf×1 流。
	worst := func(tier kcpMemTier) int {
		return (tier.sndWnd+tier.rcvWnd)*1350 + tier.sockBuf*2 + tier.smuxBuf + tier.streamBuf
	}
	if got := worst(kcpTierSmall); got != 5052416 { // ≈4.8MiB
		t.Fatalf("small worst-case: got %d", got)
	}
	if got := worst(kcpTierMedium); got != 12869632 { // ≈12.3MiB
		t.Fatalf("medium worst-case: got %d", got)
	}
	if got := worst(kcpTierLarge); got != 25739264 { // ≈24.5MiB
		t.Fatalf("large worst-case: got %d", got)
	}
}

func TestFixKCPMemMetadataPerTier(t *testing.T) {
	for _, tier := range []kcpMemTier{kcpTierSmall, kcpTierMedium, kcpTierLarge} {
		md := tunnelKCPMetadata("key", tier, false)
		for k, want := range map[string]interface{}{
			"kcp.sndwnd": tier.sndWnd, "kcp.rcvwnd": tier.rcvWnd,
			"kcp.sockbuf": tier.sockBuf, "kcp.smuxbuf": tier.smuxBuf,
			"kcp.streambuf": tier.streamBuf,
			// FEC 缺省档：10/3 原样下发、无 fec_off 标记。
			"kcp.datashard": 10, "kcp.parityshard": 3,
		} {
			if md[k] != want {
				t.Fatalf("tier %s param %s: got %v want %v", tier.name, k, md[k], want)
			}
		}
		if _, ok := md["kcp.fec_off"]; ok {
			t.Fatalf("tier %s: fec_off marker must be absent in default mode", tier.name)
		}
	}
}

func TestFixKCPFecOffMetadataShape(t *testing.T) {
	md := tunnelKCPMetadata("key", kcpTierSmall, true)
	if _, ok := md["kcp.datashard"]; ok {
		t.Fatal("fec off must not emit kcp.datashard")
	}
	if _, ok := md["kcp.parityshard"]; ok {
		t.Fatal("fec off must not emit kcp.parityshard")
	}
	if md["kcp.fec_off"] != true {
		t.Fatalf("fec off must emit the kcp.fec_off=true marker, got %v", md["kcp.fec_off"])
	}
	// 归一化与读面口径：仅 default/off 合法，读面只认确切 off。
	for in, want := range map[string]string{"": "", "default": "default", " OFF ": "off", "Default": "default"} {
		got, err := normalizeKCPFecMode(in)
		if err != nil || got != want {
			t.Fatalf("normalizeKCPFecMode(%q): got %q err %v", in, got, err)
		}
	}
	if _, err := normalizeKCPFecMode("half"); err == nil {
		t.Fatal("invalid fec mode must be rejected at save time")
	}
	if !kcpFecOff("off") || kcpFecOff("default") || kcpFecOff("") || kcpFecOff("garbage") {
		t.Fatal("kcpFecOff must be true only for the exact off value")
	}
	// 无 repo 的 Handler：FEC 恒 default、内存声明恒空（fail 向存量/保守）。
	h := &Handler{}
	if h.kcpFecMode() != kcpFecModeDefault {
		t.Fatal("handler without repo must default fec mode")
	}
	if h.kcpNodeMemSpec() != "" {
		t.Fatal("handler without repo must have empty mem spec")
	}
}

func TestFixKCPMemChainTierWiring(t *testing.T) {
	// 无 repo 的 Handler 生成本地链：拨号/监听两面同走 small 档，
	// 两面 metadata 逐字同源（同节点对同隧道两端一致）。
	h := &Handler{jwtSecret: fixkTestSecret}
	node := &nodeRecord{ID: 2, ServerIPv4: "10.0.0.2"}
	svcs := h.buildTunnelChainServiceConfig(42, tunnelRuntimeNode{NodeID: 2, Protocol: "kcp", Port: 24001, ChainType: 3}, node, 1)
	if len(svcs) != 1 {
		t.Fatalf("expected one service, got %d", len(svcs))
	}
	listenerMD := fixkKCPMetadata(t, svcs[0]["listener"].(map[string]interface{}))
	if listenerMD["kcp.sndwnd"] != kcpTierSmall.sndWnd || listenerMD["kcp.sockbuf"] != kcpTierSmall.sockBuf {
		t.Fatalf("listener must use the small default tier without declarations: %v", listenerMD)
	}
	chain, err := h.buildTunnelChainConfig(42, 1,
		[]tunnelRuntimeNode{{NodeID: 2, Protocol: "kcp", Port: 24001, ConnectIP: "10.0.0.2"}},
		map[int64]*nodeRecord{1: {ID: 1, ServerIPv4: "10.0.0.1"}, 2: node}, "")
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}
	hops := chain["hops"].([]map[string]interface{})
	nodes := hops[0]["nodes"].([]map[string]interface{})
	dialerMD := fixkKCPMetadata(t, nodes[0]["dialer"].(map[string]interface{}))
	if !reflect.DeepEqual(dialerMD, listenerMD) {
		t.Fatalf("same-tier dialer/listener metadata drift:\n dialer=%v\n listener=%v", dialerMD, listenerMD)
	}
	// 声明查表：spec 解析走 repo 口径，脏 spec 整体作废回 small。
	if got := kcpNodeMemTier(`{"2": 8192}`, 2); got != kcpTierLarge {
		t.Fatalf("declared 8GiB node must be large tier, got %s", got.name)
	}
	if got := kcpNodeMemTier(`{"2": 8192}`, 3); got != kcpTierSmall {
		t.Fatalf("undeclared node must stay small, got %s", got.name)
	}
	if got := kcpNodeMemTier(`{"2": -5}`, 2); got != kcpTierSmall {
		t.Fatalf("dirty spec must fall back to small, got %s", got.name)
	}
	if repo.KCPNodeMemMB("", 2) != 0 || repo.KCPNodeMemMB(`{"2": 300}`, 2) != 300 {
		t.Fatal("repo mem lookup mismatch")
	}
}

// TestFixKCPMemFederationServiceFecWiring 是 INT-3 第二轮验收打回
// 的补修门：联邦角色服务监听面（buildFederationServiceConfig）
// 此前 fecOff 恒写死 false——kcp_fec_mode=off 时 middle/exit 监听
// 侧 ServeConn 仍按 10/3 为已接受会话构造 FEC 编码器（发送方向
// 冗余不归零），P1-05 差集 ≠∅。补修后本面与拨号面同口径消费：
// off 不下发 shard 键＋下发 fec_off 标记，且监听 metadata 与真源
// tunnelKCPMetadata(small 档) 逐字一致（联邦恒 small 档不变）。
func TestFixKCPMemFederationServiceFecWiring(t *testing.T) {
	const key = "fed-kcp-key"
	for _, role := range []string{"middle", "exit"} {
		chainName := ""
		if role == "middle" {
			chainName = "fed_chain_1"
		}
		for _, fecOff := range []bool{true, false} {
			svc := buildFederationServiceConfig("fed_svc_1", "192.0.2.2:24002", "kcp", role, chainName, 1, "", key, fecOff)
			listener, ok := svc["listener"].(map[string]interface{})
			if !ok {
				t.Fatalf("role %s fecOff %v: listener missing: %v", role, fecOff, svc)
			}
			md := fixkKCPMetadata(t, listener)
			want := tunnelKCPMetadata(key, kcpTierSmall, fecOff)
			if !reflect.DeepEqual(md, want) {
				t.Fatalf("role %s fecOff %v: federation listener metadata drift from source of truth:\n got=%v\n want=%v", role, fecOff, md, want)
			}
			if fecOff {
				if md["kcp.fec_off"] != true {
					t.Fatalf("role %s: fec off must emit the kcp.fec_off=true marker, got %v", role, md)
				}
				if _, ok := md["kcp.datashard"]; ok {
					t.Fatalf("role %s: fec off must not emit kcp.datashard, got %v", role, md)
				}
				if _, ok := md["kcp.parityshard"]; ok {
					t.Fatalf("role %s: fec off must not emit kcp.parityshard, got %v", role, md)
				}
			} else {
				if md["kcp.datashard"] != 10 || md["kcp.parityshard"] != 3 {
					t.Fatalf("role %s: fec default must emit 10/3 shards, got %v", role, md)
				}
				if _, ok := md["kcp.fec_off"]; ok {
					t.Fatalf("role %s: fec default must not emit the fec_off marker, got %v", role, md)
				}
			}
		}
	}
	// 非 KCP 协议无 KCP 面：fecOff 不得给 tls 监听凭空造 metadata。
	svc := buildFederationServiceConfig("fed_svc_1", "192.0.2.2:24002", "tls", "exit", "", 1, "", "", true)
	listener := svc["listener"].(map[string]interface{})
	if _, ok := listener["metadata"]; ok {
		t.Fatalf("tls federation listener must carry no metadata, got %v", listener)
	}
}

// TestFixKCPMemFederationRuntimeFecEndToEnd 把上一门的穿线验到
// 真实下发层：repo 配置 kcp_fec_mode 后经 applyPeerShareRoleRuntime
// 物化，捕获 UpdateService 命令载荷断言监听面 metadata——off 时
// 只有 fec_off 标记、无 shard 键；default 时 10/3 原样、无标记。
// middle 角色另断言 UpdateChains 拨号面与监听面同一 fecOff 值
// （两面同口径，防一面穿线一面写死的回归）。
func TestFixKCPMemFederationRuntimeFecEndToEnd(t *testing.T) {
	for _, mode := range []string{"off", "default"} {
		for _, role := range []string{"exit", "middle"} {
			t.Run(mode+"/"+role, func(t *testing.T) {
				agent := newCleanupAgent(t)
				agent.h.jwtSecret = "test-jwt-secret"
				now := time.Now().UnixMilli()
				if err := agent.h.repo.UpsertConfig(kcpFecModeConfigKey, mode, now); err != nil {
					t.Fatal(err)
				}
				share := &repo.PeerShare{Name: "kcp-fec-share", NodeID: 1, Token: "kcp-fec-token", PortRangeStart: 31000, PortRangeEnd: 31010, IsActive: 1, CreatedTime: now, UpdatedTime: now}
				if err := agent.h.repo.CreatePeerShare(share); err != nil {
					t.Fatal(err)
				}
				runtime := &repo.PeerShareRuntime{ShareID: share.ID, NodeID: 1, ReservationID: "kcp-fec-res", ResourceKey: "kcp-fec-resource", Role: role, Protocol: "kcp", Strategy: "round", Port: 31005, Applied: 1, Status: 1, CreatedTime: now, UpdatedTime: now}
				if role == "middle" {
					runtime.Target = `[{"host":"127.0.0.1","port":32000,"protocol":"kcp"}]`
				}
				if err := agent.h.repo.CreatePeerShareRuntime(runtime); err != nil {
					t.Fatal(err)
				}
				runtime.BindingID = fmt.Sprint(runtime.ID)
				runtime.ServiceName = fmt.Sprintf("fed_svc_%d", runtime.ID)
				if role == "middle" {
					runtime.ChainName = federationRuntimeChainName(runtime.BindingID)
				}
				if err := agent.h.repo.UpdatePeerShareRuntime(runtime); err != nil {
					t.Fatal(err)
				}
				if err := agent.h.applyPeerShareRoleRuntime(runtime); err != nil {
					t.Fatalf("applyPeerShareRoleRuntime: %v", err)
				}
				assertFecShape := func(face string, md map[string]interface{}) {
					t.Helper()
					if mode == "off" {
						if md["kcp.fec_off"] != true {
							t.Fatalf("%s: fec off must carry the fec_off marker, got %v", face, md)
						}
						if _, ok := md["kcp.datashard"]; ok {
							t.Fatalf("%s: fec off must not emit kcp.datashard, got %v", face, md)
						}
						if _, ok := md["kcp.parityshard"]; ok {
							t.Fatalf("%s: fec off must not emit kcp.parityshard, got %v", face, md)
						}
					} else {
						if md["kcp.datashard"] != float64(10) || md["kcp.parityshard"] != float64(3) {
							t.Fatalf("%s: fec default must emit 10/3 shards, got %v", face, md)
						}
						if _, ok := md["kcp.fec_off"]; ok {
							t.Fatalf("%s: fec default must not emit the fec_off marker, got %v", face, md)
						}
					}
				}
				cmds := agent.commandsOfType("UpdateService")
				if len(cmds) == 0 {
					t.Fatal("no UpdateService command captured")
				}
				var services []map[string]interface{}
				if err := json.Unmarshal(cmds[len(cmds)-1].Data, &services); err != nil || len(services) == 0 {
					t.Fatalf("decode UpdateService data %s: %v", string(cmds[len(cmds)-1].Data), err)
				}
				listener, _ := services[0]["listener"].(map[string]interface{})
				md, _ := listener["metadata"].(map[string]interface{})
				if md == nil {
					t.Fatalf("federation kcp service listener metadata missing: %v", services[0])
				}
				assertFecShape("service listener", md)
				if role == "middle" {
					chainCmds := agent.commandsOfType("UpdateChains")
					if len(chainCmds) == 0 {
						t.Fatal("no UpdateChains command captured")
					}
					var payload map[string]interface{}
					if err := json.Unmarshal(chainCmds[len(chainCmds)-1].Data, &payload); err != nil {
						t.Fatalf("decode UpdateChains data: %v", err)
					}
					data, _ := payload["data"].(map[string]interface{})
					hops, _ := data["hops"].([]interface{})
					hop0, _ := hops[0].(map[string]interface{})
					nodes, _ := hop0["nodes"].([]interface{})
					node0, _ := nodes[0].(map[string]interface{})
					dialer, _ := node0["dialer"].(map[string]interface{})
					dialerMD, _ := dialer["metadata"].(map[string]interface{})
					if dialerMD == nil {
						t.Fatalf("middle chain dialer metadata missing: %v", node0)
					}
					assertFecShape("chain dialer", dialerMD)
				}
			})
		}
	}
}
