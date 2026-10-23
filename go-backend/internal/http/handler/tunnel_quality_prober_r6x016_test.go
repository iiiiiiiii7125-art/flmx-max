package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// R6X-016：隧道质量快照的候选跳逐跳错误串（probeCandidateGroupLinks 与
// probePublicTargetCandidates 两落点）必须过 latencyCommandError 统一
// 归类，与快照头 snap.ErrorMessage 同文案。防过修对照：解析失败串两
// 落点（快照主路径 :「解析节点 X 失败: …」与候选跳 resolveErr 原串）
// 维持不归类，不得套「探测失败：」前缀。

func r6x016CandidateHops(t *testing.T, p *tunnelQualityProber) []TunnelQualityCandidateHop {
	t.Helper()
	snaps := p.GetAll()
	if len(snaps) != 1 {
		t.Fatalf("expected one quality snapshot, got %+v", snaps)
	}
	var details tunnelQualityChainDetails
	if err := json.Unmarshal([]byte(snaps[0].ChainDetails), &details); err != nil {
		t.Fatalf("decode chain details: %v (%q)", err, snaps[0].ChainDetails)
	}
	return details.CandidateHops
}

func r6x016FindHop(t *testing.T, hops []TunnelQualityCandidateHop, fromID, toID int64) TunnelQualityCandidateHop {
	t.Helper()
	for _, hop := range hops {
		if hop.FromNodeID == fromID && hop.ToNodeID == toID {
			return hop
		}
	}
	t.Fatalf("candidate hop %d -> %d not found in %+v", fromID, toID, hops)
	return TunnelQualityCandidateHop{}
}

// 落点一（probeCandidateGroupLinks，type-2 入口→出口候选跳）：ping 原始
// 繁忙串必须归类，且与快照头逐字一致。
func TestR6X016CandidateGroupLinkErrorClassified(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedQualityForwardTunnel(t, h, 96, []int{1})
	raw := errors.New("TCP探测任务过多")
	p := newTunnelQualityProber(h)
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		if fmt.Sprintf("%d|%s|%d", nodeID, ip, port) == "10|10.0.0.30|30030" {
			return 0, 100, raw
		}
		return 10, 0, nil
	}
	p.probeTunnel(96)

	snaps := p.GetAll()
	if len(snaps) != 1 {
		t.Fatalf("expected one snapshot, got %+v", snaps)
	}
	if snaps[0].ErrorMessage != "节点繁忙，请稍后重试" {
		t.Fatalf("precondition: snapshot head must already be classified, got %q", snaps[0].ErrorMessage)
	}
	hop := r6x016FindHop(t, r6x016CandidateHops(t, p), 10, 30)
	if hop.ErrorMessage != "节点繁忙，请稍后重试" {
		t.Fatalf("R6X-016: candidate hop ErrorMessage = %q, want classified %q (raw %q)", hop.ErrorMessage, "节点繁忙，请稍后重试", raw.Error())
	}
	if hop.ErrorMessage != latencyCommandError(raw) || hop.ErrorMessage != snaps[0].ErrorMessage {
		t.Fatalf("R6X-016: candidate hop %q must match latencyCommandError and snapshot head %q verbatim", hop.ErrorMessage, snaps[0].ErrorMessage)
	}
}

// 落点二（probePublicTargetCandidates）：type-2 出口→公网候选跳与
// type-1 入口→公网候选跳同函数，两形态均须归类。
func TestR6X016PublicTargetCandidateErrorClassified(t *testing.T) {
	t.Run("type2 exit to public", func(t *testing.T) {
		h := setupProbeTargetTunnelHandler(t)
		seedQualityForwardTunnel(t, h, 97, []int{1})
		raw := errors.New("节点连接已断开")
		p := newTunnelQualityProber(h)
		p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
			if fmt.Sprintf("%d|%s|%d", nodeID, ip, port) == "30|www.bing.com|443" {
				return 0, 100, raw
			}
			return 10, 0, nil
		}
		p.probeTunnel(97)
		hop := r6x016FindHop(t, r6x016CandidateHops(t, p), 30, 0)
		if hop.ErrorMessage != "节点离线" {
			t.Fatalf("R6X-016: public candidate hop ErrorMessage = %q, want classified %q (raw %q)", hop.ErrorMessage, "节点离线", raw.Error())
		}
		if hop.ErrorMessage != latencyCommandError(raw) {
			t.Fatalf("R6X-016: public candidate hop %q must equal latencyCommandError %q", hop.ErrorMessage, latencyCommandError(raw))
		}
	})

	t.Run("type1 entry to public", func(t *testing.T) {
		h := setupProbeTargetTunnelHandler(t)
		seedProbeTargetTunnel(t, h, 98, "r6x016-type1", "speed.example.com", 8443)
		raw := errors.New("等待节点响应超时")
		p := newTunnelQualityProber(h)
		p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
			return 0, 100, raw
		}
		p.probeTunnel(98)
		hop := r6x016FindHop(t, r6x016CandidateHops(t, p), 10, 0)
		if hop.ErrorMessage != "节点响应超时" {
			t.Fatalf("R6X-016: type1 public candidate hop ErrorMessage = %q, want classified %q (raw %q)", hop.ErrorMessage, "节点响应超时", raw.Error())
		}
	})
}

// 防过修对照：解析失败串两落点维持不归类。出口节点地址为空 → 主路径
// 快照头为「解析节点 exit-1 失败: 目标节点地址为空」（描述性串原样），
// 候选跳为 resolveErr 原串「目标节点地址为空」，均不得被套上
// latencyCommandError 的「探测失败：」归类前缀。
func TestR6X016ResolveFailureStringsStayUnclassified(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	now := time.Now().UnixMilli()
	if err := h.repo.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference, probe_target_host, probe_target_port)
		VALUES(99, 'r6x016-resolve', 1, 2, 'tls', 1, ?, ?, 1, 99, '', '', 0)
	`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := h.repo.DB().Exec(`
		INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
		VALUES(99, '1', 10, 30001, 'fifo', 1, 'tls')
	`).Error; err != nil {
		t.Fatalf("insert entry chain: %v", err)
	}
	if err := h.repo.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(30, 'exit-1', 'exit-secret-1', '', '', '', '30000-30100', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)
	`, now, now).Error; err != nil {
		t.Fatalf("insert empty-ip exit node: %v", err)
	}
	if err := h.repo.DB().Exec(`
		INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
		VALUES(99, '3', 30, 30030, 'fifo', 1, 'tls')
	`).Error; err != nil {
		t.Fatalf("insert exit chain: %v", err)
	}

	p := newTunnelQualityProber(h)
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		return 10, 0, nil
	}
	p.probeTunnel(99)

	snaps := p.GetAll()
	if len(snaps) != 1 {
		t.Fatalf("expected one snapshot, got %+v", snaps)
	}
	wantSnap := "解析节点 exit-1 失败: 目标节点地址为空"
	if snaps[0].ErrorMessage != wantSnap {
		t.Fatalf("anti-overfix: snapshot resolve-failure string = %q, want unclassified %q", snaps[0].ErrorMessage, wantSnap)
	}
	if strings.HasPrefix(snaps[0].ErrorMessage, "探测失败") {
		t.Fatalf("anti-overfix: snapshot resolve-failure string must not carry classification prefix, got %q", snaps[0].ErrorMessage)
	}
	hop := r6x016FindHop(t, r6x016CandidateHops(t, p), 10, 30)
	if hop.ErrorMessage != "目标节点地址为空" {
		t.Fatalf("anti-overfix: candidate resolve-failure string = %q, want raw descriptive %q (no classification prefix)", hop.ErrorMessage, "目标节点地址为空")
	}
}
