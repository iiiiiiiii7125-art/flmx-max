package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// Round 8 延迟语义簇回归：未测三态（全未测/部分未测/零腿）在生产者
// 与聚合层的全集收口。每条测试对应 LEDGER 条目，形态按机制全集钉
// （参考点数量任意、双分支、混合腿），不只钉审计发现时的单一形态。

// R8-020：多参考点＋全腿未测时，点位级已正确呈未测，但顶层聚合
// 不得残留 latencyProbe 的默认误差「测试点都连不上」。RB3-01 的
// 回归测试只钉了 1 个参考点，本测试钉 ≥2 个参考点形态。
func TestRelayLatencyAllUntestedMultiReferencePointsClearsTopError(t *testing.T) {
	h, r := setupLatencyHandler(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true},{"name":"联通","host":"cu.example.com","port":80,"enabled":true}]`)
	front := mustCreateRelayNode(t, r, "front", "198.51.100.1")
	landing := mustCreateRelayNode(t, r, "landing", "203.0.113.10")

	// Exit on the landing itself: the single chain leg is the whole path.
	id := mustCreateRelayLine(t, r, "all-udp-ref2", 2, "127.0.0.1:443", front, nil, landing)
	setChainRowProtocol(t, r, landing, "udp")

	calls := stubLatencyPing(t, func(_ int64, host string) (map[string]interface{}, error) {
		if host != "ct.example.com" && host != "cu.example.com" {
			return nil, fmt.Errorf("unexpected ping %s", host)
		}
		return okPing(40), nil
	})
	if _, err := h.probeNodeLatency(context.Background(), []int64{front}, probeRoundTargeted); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	res, err := h.probeRelayLatency(context.Background(), []int64{id}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("untested legs were probed: %v", *calls)
	}
	got := res[id]
	if len(got.Points) != 2 {
		t.Fatalf("points = %+v, want two reference points", got.Points)
	}
	for _, p := range got.Points {
		if !p.Untested || p.OK || p.LatencyMs != 0 || p.Error != "" {
			t.Fatalf("point = %+v, want untested with no value/error", p)
		}
	}
	if got.OK || got.Error != "" {
		t.Fatalf("relay = ok %v err %q, want not-ok with the default error cleared at any point count", got.OK, got.Error)
	}
	if got.PathMs != 0 || got.PartialUntested {
		t.Fatalf("relay = pathMs %d partial %v, want 0/false (nothing measured)", got.PathMs, got.PartialUntested)
	}
}

// R8-024（中转延迟面）：部分未测——点位值与 PathMs 只含实测腿时，
// 点位与线路两级都必须带 PartialUntested 标记；双分支（有/无参考
// 点）各钉一形态，全实测形态作阴性对照（标记必须为假）。
func TestRelayLatencyPartialUntestedMarked(t *testing.T) {
	buildMixed := func(t *testing.T, targets string) (relayLatency, int64) {
		h, r := setupLatencyHandler(t, targets)
		front := mustCreateRelayNode(t, r, "front", "198.51.100.1")
		ix := mustCreateRelayNode(t, r, "ix", "198.51.100.2")
		landing := mustCreateRelayNode(t, r, "landing", "203.0.113.10")
		id := mustCreateRelayLine(t, r, "mixed-udp", 2, "203.0.113.10:443", front, []int64{ix}, landing)
		setChainRowProtocol(t, r, ix, "kcp")
		legs := map[string]float64{
			"ct.example.com:80":  40, // front → reference point
			"203.0.113.10:31000": 25, // ix → landing (tunnel port, TCP)
			"203.0.113.10:443":   30, // landing → target (TCP)
		}
		old := nodeLatencyPing
		t.Cleanup(func() { nodeLatencyPing = old })
		nodeLatencyPing = func(_ *Handler, _ int64, host string, port int) (map[string]interface{}, error) {
			ms, ok := legs[fmt.Sprintf("%s:%d", host, port)]
			if !ok {
				return nil, fmt.Errorf("unexpected ping %s:%d", host, port)
			}
			return okPing(ms), nil
		}
		if len(targets) > 0 {
			if _, err := h.probeNodeLatency(context.Background(), []int64{front}, probeRoundTargeted); err != nil {
				t.Fatal(err)
			}
		}
		res, err := h.probeRelayLatency(context.Background(), []int64{id}, false)
		if err != nil {
			t.Fatal(err)
		}
		return res[id], id
	}

	t.Run("reference point branch", func(t *testing.T) {
		got, _ := buildMixed(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`)
		if got.PathMs != 55 || !got.PartialUntested {
			t.Fatalf("relay = pathMs %d partial %v, want 55/true (measured legs only, one leg untested)", got.PathMs, got.PartialUntested)
		}
		if len(got.Points) != 1 || !got.Points[0].OK || got.Points[0].LatencyMs != 95 || !got.Points[0].PartialUntested {
			t.Fatalf("point = %+v, want OK 95ms (40 entry + 55 measured path) marked partial", got.Points)
		}
	})

	t.Run("path only branch", func(t *testing.T) {
		got, _ := buildMixed(t, "")
		if len(got.Points) != 1 || !got.Points[0].OK || got.Points[0].LatencyMs != 55 || !got.Points[0].PartialUntested {
			t.Fatalf("path point = %+v, want OK 55ms marked partial", got.Points)
		}
		if !got.PartialUntested || got.Error != "" {
			t.Fatalf("relay = partial %v err %q, want true and no error", got.PartialUntested, got.Error)
		}
	})

	t.Run("fully measured stays unmarked", func(t *testing.T) {
		h, r := setupLatencyHandler(t, "")
		front := mustCreateRelayNode(t, r, "front", "198.51.100.1")
		id := mustCreateRelayLine(t, r, "port", 1, "203.0.113.10:443", front, nil, 0)
		stubLatencyPing(t, func(int64, string) (map[string]interface{}, error) { return okPing(33), nil })
		res, err := h.probeRelayLatency(context.Background(), nil, true)
		if err != nil {
			t.Fatal(err)
		}
		got := res[id]
		if !got.Points[0].OK || got.Points[0].LatencyMs != 33 || got.Points[0].PartialUntested || got.PartialUntested {
			t.Fatalf("fully measured = %+v, want OK 33ms with no partial mark anywhere", got)
		}
	})
}

// R8-048：零腿路径（type-2 回环落地＋零链路段＋无参考点）不得呈
// 1ms/0% 完美样本——无任何一段被测量，点位须走显式未测态，与同
// 函数全未测形态同口径。
func TestRelayLatencyZeroLegPathPointIsUntested(t *testing.T) {
	h, r := setupLatencyHandler(t, "")
	front := mustCreateRelayNode(t, r, "front", "198.51.100.1")
	// Type-2 tunnel with only an entry row: no hop groups, no exit
	// row, and the target is loopback, so the plan has zero legs.
	id := mustCreateRelayLine(t, r, "zero-leg", 2, "127.0.0.1:443", front, nil, 0)

	calls := stubLatencyPing(t, func(int64, string) (map[string]interface{}, error) { return okPing(1), nil })
	res, err := h.probeRelayLatency(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("zero-leg path was probed: %v", *calls)
	}
	got := res[id]
	if len(got.Hops) != 0 {
		t.Fatalf("hops = %+v, want none", got.Hops)
	}
	if len(got.Points) != 1 || !got.Points[0].Untested || got.Points[0].OK || got.Points[0].LatencyMs != 0 || got.Points[0].Error != "" {
		t.Fatalf("path point = %+v, want untested with no value/error (not a fabricated 1ms sample)", got.Points)
	}
	if got.OK || got.Error != "" || got.PathMs != 0 || got.PartialUntested {
		t.Fatalf("relay = ok %v err %q pathMs %d partial %v, want not-ok, no error, 0, false", got.OK, got.Error, got.PathMs, got.PartialUntested)
	}
}

// R8-024（隧道质量面）：混合跳（实测＋未测）聚合时快照必须带
// EntryToExitPartialUntested 且总值只含实测跳；标记须落库
// （entry_to_exit_partial=1），监控快照回读面同口径。全实测隧道
// 作阴性对照。
func TestTunnelQualityPartialUntestedAggregate(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	now := time.Now().UnixMilli()
	insertNode := func(id int64, ip string) {
		if err := h.repo.DB().Exec(`
			INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
			VALUES(?, ?, ?, ?, ?, '', '30000-30100', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)
		`, id, fmt.Sprintf("node-%d", id), fmt.Sprintf("secret-%d", id), ip, ip, now, now).Error; err != nil {
			t.Fatalf("insert node %d: %v", id, err)
		}
	}
	insertNode(20, "10.0.0.20")
	insertNode(30, "10.0.0.30")
	seedTunnel := func(id int64, chain [][4]interface{}) {
		if err := h.repo.DB().Exec(`
			INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference, probe_target_host, probe_target_port)
			VALUES(?, ?, 1, 2, 'tls', 1, ?, ?, 1, ?, '', '', 0)
		`, id, fmt.Sprintf("quality-r8-%d", id), now, now, id).Error; err != nil {
			t.Fatalf("insert tunnel %d: %v", id, err)
		}
		for i, c := range chain {
			if err := h.repo.DB().Exec(`
				INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
				VALUES(?, ?, ?, ?, 'fifo', ?, ?)
			`, id, c[0], c[1], c[2], i+1, c[3]).Error; err != nil {
				t.Fatalf("insert chain for tunnel %d: %v", id, err)
			}
		}
	}
	// 93: entry →(kcp, untested) mid →(tls, measured) exit.
	seedTunnel(93, [][4]interface{}{
		{1, int64(10), 30001, "tls"},
		{2, int64(20), 30020, "kcp"},
		{3, int64(30), 30030, "tls"},
	})
	// 94: entry →(tls, measured) exit — fully measured control.
	// Chain ports differ from tunnel 93's: chain_tunnel(node_id, port)
	// is unique across tunnels (r5b4).
	seedTunnel(94, [][4]interface{}{
		{1, int64(10), 30002, "tls"},
		{3, int64(30), 30031, "tls"},
	})

	p := newTunnelQualityProber(h)
	p.probeNode = func(nodeID int64, _ string, _ int, _ diagnosisExecOptions) (float64, float64, error) {
		switch nodeID {
		case 20:
			return 25, 0, nil // mid → exit
		case 10:
			return 20, 0, nil // entry → exit (tunnel 94)
		case 30:
			return 15, 0, nil // exit → target
		}
		return 0, 100, errors.New("unexpected ping")
	}
	p.probeTunnel(93)
	p.probeTunnel(94)

	snaps := map[int64]tunnelQualitySnapshot{}
	for _, s := range p.GetAll() {
		snaps[s.TunnelID] = s
	}
	partial := snaps[93]
	if !partial.Success || partial.EntryToExitLatency != 25 || !partial.EntryToExitPartialUntested {
		t.Fatalf("partial snapshot = %+v, want success, 25ms (measured hop only), partial mark set", partial)
	}
	full := snaps[94]
	if !full.Success || full.EntryToExitLatency != 20 || full.EntryToExitPartialUntested {
		t.Fatalf("full snapshot = %+v, want success, 20ms, no partial mark", full)
	}

	// The mark must persist: latest-quality rows feed the monitor API.
	rows, err := h.repo.GetLatestTunnelQualities()
	if err != nil {
		t.Fatal(err)
	}
	byTunnel := map[int64]model.TunnelQuality{}
	for _, row := range rows {
		byTunnel[row.TunnelID] = row
	}
	if got := byTunnel[93].EntryToExitPartial; got != 1 {
		t.Fatalf("persisted entry_to_exit_partial for tunnel 93 = %d, want 1", got)
	}
	if got := byTunnel[94].EntryToExitPartial; got != 0 {
		t.Fatalf("persisted entry_to_exit_partial for tunnel 94 = %d, want 0", got)
	}
}

// R8-019（F3-01）：隧道更新后入口转发同步中 ReplaceForwardPorts
// 锁内复检失败时，失败必须以点名 warnings 返回（由 tunnelUpdate
// 并入成功回执），不得只留服务端日志。构造确定性撞口：新入口节点
// 端口池仅 30000 且已被他人转发占用，受害转发的参考口同为 30000、
// 随机选口无可用口、回退参考口后锁内复检必失败。
func TestSyncTunnelForwardsEntryPortsFailureReturnsWarning(t *testing.T) {
	h, r := setupLatencyHandler(t, "")
	db := r.DB()
	now := time.Now().UnixMilli()
	mkNode := func(name, ip, portRange string) int64 {
		n := model.Node{Name: name, Secret: name, ServerIP: ip, Port: portRange, Status: 1}
		if err := db.Create(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n.ID
	}
	nodeA := mkNode("entry-old", "10.0.0.1", "1000-65535")
	nodeB := mkNode("entry-new", "10.0.0.2", "30000-30000")
	mkTunnel := func(name string) int64 {
		if err := db.Exec(`
			INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx)
			VALUES(?, 1.0, 2, 'tcp', 99999, ?, ?, 1, NULL, 0)`, name, now, now).Error; err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := db.Raw(`SELECT id FROM tunnel WHERE name = ?`, name).Scan(&id).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	tunnelID := mkTunnel("sync-tunnel")
	otherTunnel := mkTunnel("other-tunnel")
	mkForward := func(name string, tid int64) int64 {
		fwd := model.Forward{UserID: 1, UserName: "admin", Name: name, TunnelID: tid, RemoteAddr: "203.0.113.10:443", CreatedTime: now, UpdatedTime: now, Status: 1}
		if err := db.Create(&fwd).Error; err != nil {
			t.Fatal(err)
		}
		return fwd.ID
	}
	blocker := mkForward("blocker", otherTunnel)
	victim := mkForward("victim", tunnelID)
	control := mkForward("control", tunnelID)
	mkPort := func(fid, nid int64, port int) {
		if err := db.Create(&model.ForwardPort{ForwardID: fid, NodeID: nid, Port: port}).Error; err != nil {
			t.Fatal(err)
		}
	}
	mkPort(blocker, nodeB, 30000) // occupies the only port in node B's pool
	mkPort(victim, nodeA, 30000)  // reference port collides on node B
	mkPort(control, nodeA, 30001) // syncs cleanly onto both entries

	warnings := h.syncTunnelForwardsEntryPorts(tunnelID, []int64{nodeA, nodeB})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "victim") {
		t.Fatalf("warnings = %v, want exactly one warning naming the victim forward", warnings)
	}

	portsOf := func(fid int64) map[int64]int {
		rows, err := h.listForwardPorts(fid)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]int{}
		for _, row := range rows {
			out[row.NodeID] = row.Port
		}
		return out
	}
	// Failed replace rolled back: the victim keeps only its old entry.
	if got := portsOf(victim); len(got) != 1 || got[nodeA] != 30000 {
		t.Fatalf("victim ports = %v, want unchanged {old entry: 30000}", got)
	}
	// The clean forward synced onto both entries and produced no warning.
	if got := portsOf(control); len(got) != 2 || got[nodeA] != 30001 || got[nodeB] != 30001 {
		t.Fatalf("control ports = %v, want both entries at 30001", got)
	}
}
