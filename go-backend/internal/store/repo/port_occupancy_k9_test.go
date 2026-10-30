package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// Round 2 K9 / P01-001：统一四源谓词的行为枚举——每一种占用源
// （forward_port / node_inbound / chain_tunnel / peer_share_runtime
// 有效预留）都必须被每一个选口/判重点位看见。此前隧道选口、隧道
// 显式口谓词、入站 occupant、备份预检四面手写三源集合、漏联邦预留
// 源，可与分享运行时同节点同口双分配。本测试按源 × 点位逐格断言，
// 任一面再漏一源即红。
//
// 点位全集（与 port_occupancy_sentinel_test.go 的源码清单对应）：
//   - NodePortOccupantTx / NodePortOccupiedTx（统一谓词本体）
//   - HasOtherForwardOnNodePortTx（转发面）
//   - NodePortOccupiedForTunnelTx（隧道显式口谓词）
//   - UsedPortsOnNodeTx / GetUsedPortsOnNodeAsMap / ListUsedPortsOnNode（已用集三形）
//   - PickNodePortTx / PickRandomNodePortTx（隧道选口）
//   - NodePortRangeOccupant（入站 occupant 命名）
//   - FindOtherForwardOnNodePortTx（转发预检详情）
//   - precheckBackupPortConflicts（备份预检，经 Import 验）
//   - TunnelChainPortReusableTx（P01-002 更新沿用判定）

const (
	k9NodeID = int64(1)
	k9Port   = 36005
)

func seedK9Node(t *testing.T, r *Repository) {
	t.Helper()
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Node{ID: k9NodeID, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "36005-36006", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
}

func seedK9Forward(t *testing.T, r *Repository) {
	t.Helper()
	createRow(t, r, &model.ForwardPort{ForwardID: 900, NodeID: k9NodeID, Port: k9Port})
}

func seedK9Inbound(t *testing.T, r *Repository) {
	t.Helper()
	now := time.Now().UnixMilli()
	createRow(t, r, &model.NodeInbound{ID: 21, NodeID: k9NodeID, KernelID: 11, Name: "ib", Protocol: "vless", Port: k9Port, CreatedTime: now, UpdatedTime: now})
}

func seedK9Chain(t *testing.T, r *Repository) {
	t.Helper()
	createRow(t, r, &model.ChainTunnel{ID: 71, TunnelID: 700, ChainType: "3", NodeID: k9NodeID, Port: sql.NullInt64{Int64: k9Port, Valid: true}, Inx: sql.NullInt64{Int64: 1, Valid: true}})
}

func seedK9Peer(t *testing.T, r *Repository, status int) {
	t.Helper()
	now := time.Now().UnixMilli()
	createRow(t, r, &model.PeerShareRuntime{ID: 31, ShareID: 5, NodeID: k9NodeID, ReservationID: "resv-k9", ResourceKey: "rk-k9", Port: k9Port, Status: status, CreatedTime: now, UpdatedTime: now})
}

func TestK9UnifiedOccupancySeesAllFourSources(t *testing.T) {
	cases := []struct {
		name          string
		seed          func(t *testing.T, r *Repository)
		wantSource    string
		wantRangeName string
	}{
		{"forward", seedK9Forward, PortSourceForward, "转发"},
		{"inbound", seedK9Inbound, PortSourceInbound, "其他节点服务"},
		{"chain", seedK9Chain, PortSourceChain, "隧道"},
		{"peer", func(t *testing.T, r *Repository) { seedK9Peer(t, r, 1) }, PortSourcePeer, "联邦分享预留"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := openCascadeRepo(t, "k9-occ-"+tc.name+".db")
			seedK9Node(t, r)
			tc.seed(t, r)
			db := r.DB()

			source, err := r.NodePortOccupantTx(db, k9NodeID, k9Port, PortExclusions{})
			if err != nil {
				t.Fatalf("NodePortOccupantTx: %v", err)
			}
			if source != tc.wantSource {
				t.Fatalf("P01-001: occupant source = %q, want %q", source, tc.wantSource)
			}

			if occ, err := r.HasOtherForwardOnNodePortTx(db, k9NodeID, k9Port, 0); err != nil || !occ {
				t.Fatalf("P01-001: HasOtherForwardOnNodePortTx = %v, %v; want occupied", occ, err)
			}
			if occ, err := r.NodePortOccupiedForTunnelTx(db, k9NodeID, k9Port, 0); err != nil || !occ {
				t.Fatalf("P01-001: NodePortOccupiedForTunnelTx = %v, %v; want occupied", occ, err)
			}

			set, err := r.UsedPortsOnNodeTx(db, k9NodeID, PortExclusions{})
			if err != nil {
				t.Fatalf("UsedPortsOnNodeTx: %v", err)
			}
			if _, ok := set[k9Port]; !ok {
				t.Fatalf("P01-001: UsedPortsOnNodeTx must contain %d (%s source)", k9Port, tc.name)
			}
			asMap, err := r.GetUsedPortsOnNodeAsMap(k9NodeID)
			if err != nil {
				t.Fatalf("GetUsedPortsOnNodeAsMap: %v", err)
			}
			if !asMap[k9Port] {
				t.Fatalf("P01-001: GetUsedPortsOnNodeAsMap must contain %d (%s source)", k9Port, tc.name)
			}
			listed, err := r.ListUsedPortsOnNode(k9NodeID)
			if err != nil {
				t.Fatalf("ListUsedPortsOnNode: %v", err)
			}
			found := false
			for _, p := range listed {
				if p == k9Port {
					found = true
				}
			}
			if !found {
				t.Fatalf("P01-001: ListUsedPortsOnNode must contain %d (%s source)", k9Port, tc.name)
			}

			// 选口：节点规格只有 36005-36006，顺序选口必须避开 36005。
			picked, err := r.PickNodePortTx(db, k9NodeID, map[int64]int{}, 0)
			if err != nil {
				t.Fatalf("PickNodePortTx: %v", err)
			}
			if picked != 36006 {
				t.Fatalf("P01-001: PickNodePortTx = %d, want 36006 (must avoid %s-occupied %d)", picked, tc.name, k9Port)
			}
			for i := 0; i < 8; i++ {
				picked, err := r.PickRandomNodePortTx(db, k9NodeID, map[int64]int{}, 0)
				if err != nil {
					t.Fatalf("PickRandomNodePortTx: %v", err)
				}
				if picked == k9Port {
					t.Fatalf("P01-001: PickRandomNodePortTx handed out %s-occupied port %d", tc.name, k9Port)
				}
			}

			name, err := r.NodePortRangeOccupant(k9NodeID, k9Port, k9Port, 0)
			if err != nil {
				t.Fatalf("NodePortRangeOccupant: %v", err)
			}
			if name != tc.wantRangeName {
				t.Fatalf("P01-001: NodePortRangeOccupant = %q, want %q", name, tc.wantRangeName)
			}

			conflict, err := r.FindOtherForwardOnNodePort(k9NodeID, k9Port, 0)
			if err != nil {
				t.Fatalf("FindOtherForwardOnNodePort: %v", err)
			}
			if conflict == nil || conflict.Source != tc.wantSource {
				t.Fatalf("P01-001: FindOtherForwardOnNodePort = %+v, want source %q", conflict, tc.wantSource)
			}

			// P01-002 沿用判定：被他人占用的口对任意隧道都不可沿用。
			reusable, err := r.TunnelChainPortReusableTx(db, k9NodeID, k9Port, 424242)
			if err != nil {
				t.Fatalf("TunnelChainPortReusableTx: %v", err)
			}
			if reusable {
				t.Fatalf("P01-002: port occupied by %s must not be reusable by another tunnel", tc.name)
			}
		})
	}
}

func TestK9UnifiedOccupancyExclusions(t *testing.T) {
	now := time.Now().UnixMilli()
	_ = now

	t.Run("forward excludes itself only", func(t *testing.T) {
		r := openCascadeRepo(t, "k9-excl-fwd.db")
		seedK9Node(t, r)
		seedK9Forward(t, r)
		if occ, err := r.HasOtherForwardOnNodePortTx(r.DB(), k9NodeID, k9Port, 900); err != nil || occ {
			t.Fatalf("own forward must be excluded, got occ=%v err=%v", occ, err)
		}
		if occ, err := r.HasOtherForwardOnNodePortTx(r.DB(), k9NodeID, k9Port, 901); err != nil || !occ {
			t.Fatalf("another forward must see the occupation, got occ=%v err=%v", occ, err)
		}
	})

	t.Run("chain excludes own tunnel only", func(t *testing.T) {
		r := openCascadeRepo(t, "k9-excl-chain.db")
		seedK9Node(t, r)
		seedK9Chain(t, r)
		if occ, err := r.NodePortOccupiedForTunnelTx(r.DB(), k9NodeID, k9Port, 700); err != nil || occ {
			t.Fatalf("own chain row must be excluded, got occ=%v err=%v", occ, err)
		}
		if occ, err := r.NodePortOccupiedForTunnelTx(r.DB(), k9NodeID, k9Port, 701); err != nil || !occ {
			t.Fatalf("another tunnel must see the occupation, got occ=%v err=%v", occ, err)
		}
		// 沿用：口是本隧道自己的链路行且仍在规格内 → 可沿用。
		reusable, err := r.TunnelChainPortReusableTx(r.DB(), k9NodeID, k9Port, 700)
		if err != nil || !reusable {
			t.Fatalf("own chain port must be reusable, got %v err=%v", reusable, err)
		}
		// 选口排除自身后可重挑中自己的现口（更新重挑不被旧行挡住）。
		picked, err := r.PickNodePortTx(r.DB(), k9NodeID, map[int64]int{}, 700)
		if err != nil || picked != k9Port {
			t.Fatalf("pick excluding own tunnel = %d, %v; want own port %d available", picked, err, k9Port)
		}
	})

	t.Run("inbound excludes itself in range occupant", func(t *testing.T) {
		r := openCascadeRepo(t, "k9-excl-inb.db")
		seedK9Node(t, r)
		seedK9Inbound(t, r)
		name, err := r.NodePortRangeOccupant(k9NodeID, k9Port, k9Port, 21)
		if err != nil || name != "" {
			t.Fatalf("own inbound must be excluded, got %q err=%v", name, err)
		}
	})

	t.Run("inactive peer reservation does not occupy", func(t *testing.T) {
		r := openCascadeRepo(t, "k9-peer-inactive.db")
		seedK9Node(t, r)
		// status 列带 default:1，Create 传 0 会被 GORM 当未设置落成 1；
		// 按生产路径（lifecycle 以 map Updates 置 0）种入失活行。
		seedK9Peer(t, r, 1)
		if err := r.DB().Model(&model.PeerShareRuntime{}).Where("id = ?", 31).Update("status", 0).Error; err != nil {
			t.Fatalf("deactivate peer row: %v", err)
		}
		source, err := r.NodePortOccupantTx(r.DB(), k9NodeID, k9Port, PortExclusions{})
		if err != nil || source != "" {
			t.Fatalf("status=0 reservation must not occupy, got %q err=%v", source, err)
		}
		set, err := r.UsedPortsOnNodeTx(r.DB(), k9NodeID, PortExclusions{})
		if err != nil {
			t.Fatalf("UsedPortsOnNodeTx: %v", err)
		}
		if _, ok := set[k9Port]; ok {
			t.Fatal("status=0 reservation must not enter the used set")
		}
	})

	t.Run("reusable requires in-spec port", func(t *testing.T) {
		r := openCascadeRepo(t, "k9-reuse-spec.db")
		seedK9Node(t, r)
		// 36007 空闲但不在节点规格（36005-36006）内 → 不可沿用。
		reusable, err := r.TunnelChainPortReusableTx(r.DB(), k9NodeID, 36007, 700)
		if err != nil || reusable {
			t.Fatalf("out-of-spec port must not be reusable, got %v err=%v", reusable, err)
		}
		// 36006 空闲且在规格内 → 可沿用。
		reusable, err = r.TunnelChainPortReusableTx(r.DB(), k9NodeID, 36006, 700)
		if err != nil || !reusable {
			t.Fatalf("free in-spec port must be reusable, got %v err=%v", reusable, err)
		}
	})
}

func TestK9BackupPrecheckSeesPeerReservationAndChain(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("forward claim colliding with peer reservation aborts", func(t *testing.T) {
		dest := openCascadeRepo(t, "k9-precheck-peer.db")
		seedK9Node(t, dest)
		seedK9Peer(t, dest, 1)
		backup := &model.BackupData{Version: "1.0", Forwards: []model.ForwardBackup{forwardBackup(51, "clash", k9NodeID, k9Port)}}
		_, err := dest.Import(backup, []string{"forwards"})
		if err == nil || !strings.Contains(err.Error(), "端口冲突") || !strings.Contains(err.Error(), "联邦分享预留") {
			t.Fatalf("P01-001: import onto an active peer reservation must abort naming it, got %v", err)
		}
		if n := countWhere(t, dest, "forward", "id = ?", 51); n != 0 {
			t.Fatalf("aborted import must not write the forward, found %d rows", n)
		}
	})

	t.Run("tunnel chain claim colliding with peer reservation aborts", func(t *testing.T) {
		dest := openCascadeRepo(t, "k9-precheck-chain-peer.db")
		seedK9Node(t, dest)
		seedK9Peer(t, dest, 1)
		chainPort := k9Port
		backup := &model.BackupData{Version: "1.0", Tunnels: []model.TunnelBackup{{
			ID: 61, Name: "tun", Type: 2, Protocol: "tls", Flow: 1, Status: 1, CreatedTime: now, UpdatedTime: now,
			ChainTunnels: []model.ChainTunnelBackup{{ID: 71, TunnelID: 61, ChainType: "3", NodeID: k9NodeID, Port: &chainPort}},
		}}}
		_, err := dest.Import(backup, []string{"tunnels"})
		if err == nil || !strings.Contains(err.Error(), "端口冲突") {
			t.Fatalf("P01-001: tunnel restore onto an active peer reservation must abort, got %v", err)
		}
	})
}

// P01-003：隧道列表（详情同源组装）回吐链路段与出口段端口；
// 入口行 port 恒 NULL 不带字段，未预留的远端行（NULL）同样省略。
func TestK9ListTunnelsExposesSegmentPorts(t *testing.T) {
	r := openCascadeRepo(t, "k9-list-ports.db")
	seedK9Node(t, r)
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Tunnel{ID: 700, Name: "tun", TrafficRatio: 1, Type: 2, Protocol: "tls", Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1, Inx: 1})
	createRow(t, r, &model.ChainTunnel{ID: 71, TunnelID: 700, ChainType: "1", NodeID: k9NodeID, Inx: sql.NullInt64{Int64: 1, Valid: true}})
	createRow(t, r, &model.ChainTunnel{ID: 72, TunnelID: 700, ChainType: "2", NodeID: k9NodeID, Port: sql.NullInt64{Int64: 36007, Valid: true}, Inx: sql.NullInt64{Int64: 1, Valid: true}})
	createRow(t, r, &model.ChainTunnel{ID: 73, TunnelID: 700, ChainType: "3", NodeID: k9NodeID, Port: sql.NullInt64{Int64: 36008, Valid: true}, Inx: sql.NullInt64{Int64: 1, Valid: true}})

	items, err := r.ListTunnels()
	if err != nil {
		t.Fatalf("ListTunnels: %v", err)
	}
	var item map[string]interface{}
	for _, it := range items {
		if it["id"] == int64(700) {
			item = it
		}
	}
	if item == nil {
		t.Fatal("tunnel 700 missing from list")
	}
	out := item["outNodeId"].([]map[string]interface{})
	if len(out) != 1 || out[0]["port"] != 36008 {
		t.Fatalf("P01-003: outNodeId must expose port 36008, got %+v", out)
	}
	hops := item["chainNodes"].([][]map[string]interface{})
	if len(hops) != 1 || len(hops[0]) != 1 || hops[0][0]["port"] != 36007 {
		t.Fatalf("P01-003: chainNodes must expose port 36007, got %+v", hops)
	}
	ins := item["inNodeId"].([]map[string]interface{})
	if len(ins) != 1 {
		t.Fatalf("inNodeId = %+v", ins)
	}
	if _, ok := ins[0]["port"]; ok {
		t.Fatalf("P01-003: entry rows carry no port and must omit the field, got %+v", ins[0])
	}
}
