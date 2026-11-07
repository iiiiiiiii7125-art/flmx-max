package handler

import (
	"reflect"
	"testing"

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
