package handler

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// E-01（发版后审计，F-PORT-01 未守住）：pickTunnelPort 经 parsePorts
// 展开节点端口范围时无 65535 钳制——范围存为 "1-99999" 后自动分配
// 10 次 6 成 4 败、存为 "65530-99999" 后 3/3 全败（"端口必须在
// 1-65535 范围内"）。parsePorts 是四处调用点的公共 choke point，
// 钳制落在此处、与已钳制的 parsePortRangeSpec 同口径。

func TestParsePortsClampsTo65535(t *testing.T) {
	ports, err := parsePorts("1-99999")
	if err != nil {
		t.Fatalf("parsePorts(\"1-99999\") must not error, got %v", err)
	}
	if len(ports) != 65535 {
		t.Fatalf("parsePorts(\"1-99999\") must clamp to 1-65535 (65535 ports), got %d", len(ports))
	}
	if ports[0] != 1 || ports[len(ports)-1] != 65535 {
		t.Fatalf("parsePorts(\"1-99999\") must span 1..65535, got %d..%d", ports[0], ports[len(ports)-1])
	}

	// 审计复现的窄范围：全部落在 65530-65535。
	ports, err = parsePorts("65530-99999")
	if err != nil {
		t.Fatalf("parsePorts(\"65530-99999\") must not error, got %v", err)
	}
	if len(ports) != 6 || ports[0] != 65530 || ports[5] != 65535 {
		t.Fatalf("parsePorts(\"65530-99999\") must be [65530..65535], got %v", ports)
	}

	// 全段越界：无有效端口、无错误（调用方按空集 fail-closed）。
	ports, err = parsePorts("70000-99999")
	if err != nil {
		t.Fatalf("parsePorts(\"70000-99999\") must not error, got %v", err)
	}
	if len(ports) != 0 {
		t.Fatalf("parsePorts(\"70000-99999\") must be empty, got %d ports", len(ports))
	}

	// 单值越界同样丢弃，不报错。
	ports, err = parsePorts("70000")
	if err != nil {
		t.Fatalf("parsePorts(\"70000\") must not error, got %v", err)
	}
	if len(ports) != 0 {
		t.Fatalf("parsePorts(\"70000\") must be empty, got %v", ports)
	}
	ports, err = parsePorts("65535")
	if err != nil || len(ports) != 1 || ports[0] != 65535 {
		t.Fatalf("parsePorts(\"65535\") must be [65535], got %v err=%v", ports, err)
	}

	// 下界钳到 1：端口 0 不是合法端口，不得进入分配候选。
	ports, err = parsePorts("0-100")
	if err != nil {
		t.Fatalf("parsePorts(\"0-100\") must not error, got %v", err)
	}
	if len(ports) != 100 || ports[0] != 1 || ports[99] != 100 {
		t.Fatalf("parsePorts(\"0-100\") must be [1..100], got len=%d first=%d last=%d", len(ports), ports[0], ports[len(ports)-1])
	}

	// 合法多段规格行为不变。
	ports, err = parsePorts("10000-10010,20000-20010")
	if err != nil || len(ports) != 22 {
		t.Fatalf("parsePorts multi-segment must keep 22 ports, got %d err=%v", len(ports), err)
	}

	// 既有错误语义不变：不可解析仍报错。
	for _, bad := range []string{"abc", "10-5", "1-2-3", "10-", "-5"} {
		if _, err := parsePorts(bad); err == nil {
			t.Fatalf("parsePorts(%q) must still error", bad)
		}
	}
}

// E-01：validateLocalNodePort 经同一 parsePorts 做集合成员判定。
// 全段越界规格（如 "70000-80000"）此前集合含非法端口、合法端口均
// 不在其中而被拒；钳制后空集不能反转为放行，仍须拒绝。不可解析规格
// 与空规格保持既有宽松行为。
func TestValidateLocalNodePortClampedSpec(t *testing.T) {
	node := &nodeRecord{Name: "clamped", PortRange: "1-99999"}
	if err := validateLocalNodePort(node, 60000); err != nil {
		t.Fatalf("port 60000 is inside clamped 1-99999 and must pass: %v", err)
	}
	if err := validateLocalNodePort(node, 70000); err == nil {
		t.Fatal("port 70000 is not a valid port and must be rejected even though the raw spec covers it")
	}

	out := &nodeRecord{Name: "out", PortRange: "70000-80000"}
	if err := validateLocalNodePort(out, 60000); err == nil {
		t.Fatal("fully out-of-range spec must still reject a legal port (no valid port in range)")
	}

	unparseable := &nodeRecord{Name: "bad", PortRange: "abc"}
	if err := validateLocalNodePort(unparseable, 60000); err != nil {
		t.Fatalf("unparseable spec must stay permissive: %v", err)
	}
	empty := &nodeRecord{Name: "empty", PortRange: ""}
	if err := validateLocalNodePort(empty, 60000); err != nil {
		t.Fatalf("empty spec must stay permissive: %v", err)
	}
}

// E-01 审计复现（handler 层直测 pickTunnelPort）：节点范围
// "65530-99999" 时 20 次分配须全部落在 65530-65535；"1-99999" 时
// 100 次分配须全部 ≤65535；全段越界 "70000-99999" 时须报错而非
// 发出非法端口。
func TestPickTunnelPortNeverExceeds65535(t *testing.T) {
	seed := func(t *testing.T, name, portRange string) (*Handler, int64) {
		t.Helper()
		r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		t.Cleanup(func() { _ = r.Close() })
		h := &Handler{repo: r}
		now := time.Now().UnixMilli()
		if err := r.DB().Exec(`
			INSERT INTO node(name, secret, server_ip, port, created_time, status, tcp_listen_addr, udp_listen_addr, is_remote)
			VALUES(?, 'secret', '10.0.0.1', ?, ?, 1, '[::]', '[::]', 0)
		`, name, portRange, now).Error; err != nil {
			t.Fatalf("insert node: %v", err)
		}
		var nodeID int64
		if err := r.DB().Raw(`SELECT id FROM node WHERE name = ?`, name).Scan(&nodeID).Error; err != nil {
			t.Fatalf("load node id: %v", err)
		}
		if err := r.DB().Exec(`
			INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference)
			VALUES(?, 1, 1, 'tls', 1, ?, ?, 1, 1, '')
		`, name+"-tunnel", now, now).Error; err != nil {
			t.Fatalf("insert tunnel: %v", err)
		}
		var tunnelID int64
		if err := r.DB().Raw(`SELECT id FROM tunnel WHERE name = ?`, name+"-tunnel").Scan(&tunnelID).Error; err != nil {
			t.Fatalf("load tunnel id: %v", err)
		}
		if err := r.DB().Exec(`
			INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, inx, protocol)
			VALUES(?, '1', ?, 1, 'tls')
		`, tunnelID, nodeID).Error; err != nil {
			t.Fatalf("insert chain_tunnel: %v", err)
		}
		return h, tunnelID
	}

	h, tunnelID := seed(t, "e01-narrow", "65530-99999")
	for i := 0; i < 20; i++ {
		p, err := h.pickTunnelPort(tunnelID)
		if err != nil {
			t.Fatalf("pick %d on 65530-99999 must succeed, got %v", i, err)
		}
		if p < 65530 || p > 65535 {
			t.Fatalf("pick %d on 65530-99999 must be inside 65530-65535, got %d", i, p)
		}
	}

	h, tunnelID = seed(t, "e01-wide", "1-99999")
	for i := 0; i < 100; i++ {
		p, err := h.pickTunnelPort(tunnelID)
		if err != nil {
			t.Fatalf("pick %d on 1-99999 must succeed, got %v", i, err)
		}
		if p < 1 || p > 65535 {
			t.Fatalf("pick %d on 1-99999 must be inside 1-65535, got %d", i, p)
		}
	}

	h, tunnelID = seed(t, "e01-out", "70000-99999")
	if p, err := h.pickTunnelPort(tunnelID); err == nil {
		t.Fatalf("pick on 70000-99999 must fail, got port %d", p)
	}
}

// 端口族复扫：firstPortFromRange（control_plane.go）是第 5 个端口范围
// 解析器（探针/优选出口的回退拨号端口）。首段越界时不得发出非法端口，
// 应返回 0 由调用方回退 443（既有“无可用端口”语义）。
func TestFirstPortFromRangeRejectsOutOfRange(t *testing.T) {
	if p := firstPortFromRange("70000-99999"); p != 0 {
		t.Fatalf("firstPortFromRange(\"70000-99999\") must be 0, got %d", p)
	}
	if p := firstPortFromRange("70000"); p != 0 {
		t.Fatalf("firstPortFromRange(\"70000\") must be 0, got %d", p)
	}
	if p := firstPortFromRange("1-99999"); p != 1 {
		t.Fatalf("firstPortFromRange(\"1-99999\") must be 1, got %d", p)
	}
	if p := firstPortFromRange("20000-20010"); p != 20000 {
		t.Fatalf("firstPortFromRange(\"20000-20010\") must be 20000, got %d", p)
	}
	if p := firstPortFromRange(""); p != 0 {
		t.Fatalf("firstPortFromRange(\"\") must be 0, got %d", p)
	}
}

// 端口族复扫：handler 的 parsePortRangeMinMax（mutations.go）是 repo 版
// 的孪生实现但此前未钳制；目前零调用方（死代码），仍按同口径钳制，
// 防止后人接线后重蹈 E-01。
func TestHandlerParsePortRangeMinMaxClamps(t *testing.T) {
	lo, hi := parsePortRangeMinMax("1-99999")
	if lo != 1 || hi != 65535 {
		t.Fatalf("parsePortRangeMinMax(\"1-99999\") must be (1, 65535), got (%d, %d)", lo, hi)
	}
	lo, hi = parsePortRangeMinMax("70000-99999")
	if lo != 0 || hi != 0 {
		t.Fatalf("parsePortRangeMinMax(\"70000-99999\") must be (0, 0), got (%d, %d)", lo, hi)
	}
	lo, hi = parsePortRangeMinMax("10000-10010,20000-20010")
	if lo != 10000 || hi != 20010 {
		t.Fatalf("parsePortRangeMinMax multi-segment must be (10000, 20010), got (%d, %d)", lo, hi)
	}
}
