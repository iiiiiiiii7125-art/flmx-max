package handler

// DB-3/DB-4（移植自 kure29/flyx 3.3.33，按我方探测面改写）：重连重测
// 受影响集、worker 静默合并、定向探测不计 inbound_down streak、慢轮次
// 结果不覆写新轮次。上游同名测试依赖其自建入站 reach 探测面
// （probeInboundReach/reachDial），我方对应面是外部入站探测
// （probeExternalInbounds），故断言口径按我方结构重写。

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func TestProbeRoundsTake(t *testing.T) {
	var r probeRounds
	if !r.take(1, 100) {
		t.Fatal("first take must pass")
	}
	if !r.take(1, 100) {
		t.Fatal("same-round take must pass")
	}
	if r.take(1, 99) {
		t.Fatal("older round must be rejected")
	}
	if !r.take(1, 101) {
		t.Fatal("newer round must pass")
	}
	if !r.take(2, 1) {
		t.Fatal("rounds are per id")
	}
}

func TestProbeStoresOlderRoundCannotOverwriteNewer(t *testing.T) {
	lat := newNodeLatencyStore()
	lat.replaceRound(map[int64]externalProbe{1: {OK: true, CheckedAt: 200}}, false, 2000)
	lat.replaceRound(map[int64]externalProbe{1: {OK: false, CheckedAt: 100}}, false, 1000)
	if p, _ := lat.get(1); !p.OK || p.CheckedAt != 200 {
		t.Fatalf("latency store overwritten by older round: %+v", p)
	}
	lat.replaceRound(map[int64]externalProbe{}, true, 1500)
	if _, ok := lat.get(1); !ok {
		t.Fatal("stale full round deleted a fresher entry")
	}
	lat.replaceRound(map[int64]externalProbe{}, true, 3000)
	if _, ok := lat.get(1); ok {
		t.Fatal("newer full round must drop missing entries")
	}

	rel := newRelayLatencyStore()
	rel.replaceRound(map[int64]relayLatency{1: {externalProbe: externalProbe{OK: true, CheckedAt: 200}}}, false, 2000)
	rel.replaceRound(map[int64]relayLatency{1: {externalProbe: externalProbe{OK: false, CheckedAt: 100}}}, false, 1000)
	if p, _ := rel.get(1); !p.OK || p.CheckedAt != 200 {
		t.Fatalf("relay store overwritten by older round: %+v", p)
	}

	ext := newExternalProbeStore()
	ext.replaceRound(map[int64]externalProbe{1: {OK: true, CheckedAt: 200}}, false, 2000)
	ext.replaceRound(map[int64]externalProbe{1: {OK: false, CheckedAt: 100}}, false, 1000)
	if p, _ := ext.get(1); !p.OK || p.CheckedAt != 200 {
		t.Fatalf("external store overwritten by older round: %+v", p)
	}
}

// DB-4：定向探测（手动/重连，ids 非空）刷新缓存但不计 inbound_down
// streak；streak 只由定时全量轮（ids 为空）累积。目标用 loopback
// 关闭端口：netguard 拒拨或拨号被拒都产生失败结果，不依赖本机网卡
// 夹具（probeFixtureLocalAddr 在无 RFC1918 地址的环境会 Skip）。
func TestTargetedExternalProbeDoesNotCountStreak(t *testing.T) {
	h, r := setupLatencyHandler(t, "")
	ctx := context.Background()
	row := &model.NodeInbound{Name: "targeted", Protocol: "ss", Listen: "127.0.0.1", Port: 9,
		Enabled: 1, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if err := r.DB().Create(row).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	for i := 0; i < notifyInboundDownStreak+1; i++ {
		if _, err := h.probeExternalInbounds(ctx, []int64{row.ID}, probeRoundTargeted); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := h.externalProbes.get(row.ID); p.OK {
		t.Fatalf("targeted probe must still refresh the cache, got %+v", p)
	}
	h.externalProbes.mu.Lock()
	streak := h.externalProbes.failStreak[row.ID]
	down := h.externalProbes.down[row.ID]
	h.externalProbes.mu.Unlock()
	if streak != 0 || down {
		t.Fatalf("targeted probes touched alert streak: streak=%d down=%v", streak, down)
	}
	for i := 0; i < notifyInboundDownStreak; i++ {
		if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
			t.Fatal(err)
		}
	}
	h.externalProbes.mu.Lock()
	streak = h.externalProbes.failStreak[row.ID]
	h.externalProbes.mu.Unlock()
	if streak < notifyInboundDownStreak {
		t.Fatalf("full rounds must count the streak, got %d", streak)
	}
}

func TestReconnectProbeTargetsOurSurface(t *testing.T) {
	h, r := setupLatencyHandler(t, "")
	entry := mustCreateRelayNode(t, r, "entry", "198.51.100.1")
	hop := mustCreateRelayNode(t, r, "hop", "198.51.100.2")
	exit := mustCreateRelayNode(t, r, "exit", "198.51.100.3")
	landing := mustCreateRelayNode(t, r, "landing", "198.51.100.4")
	other := mustCreateRelayNode(t, r, "other", "198.51.100.5")
	id := mustCreateRelayLine(t, r, "affected", 2, "198.51.100.4:443", entry, []int64{hop}, exit)
	mustCreateRelayLine(t, r, "unrelated", 1, "203.0.113.1:443", other, nil, 0)
	// 落地入站是带内核的自建入站（kernel_id 非 0）；夹具缺省行
	// KernelID=0 会被 isExternalInbound 误判为外部入站，一并补上。
	if err := r.DB().Exec("UPDATE node_inbound SET node_id = ?, kernel_id = 999 WHERE id = (SELECT inbound_id FROM node_inbound_relay WHERE id = ?)", landing, id).Error; err != nil {
		t.Fatal(err)
	}
	ext := &model.NodeInbound{NodeID: entry, Name: "imported-on-entry", Protocol: "ss", Listen: "203.0.113.9", Port: 8388, Enabled: 1}
	if err := r.DB().Create(ext).Error; err != nil {
		t.Fatal(err)
	}
	relays, err := h.repo.ListInboundRelays(0)
	if err != nil {
		t.Fatal(err)
	}
	var tunnelID int64
	for _, rw := range relays {
		if rw.ID == id {
			tunnelID = rw.TunnelID
		}
	}
	if tunnelID <= 0 {
		t.Fatal("affected line has no tunnel")
	}
	for _, node := range []int64{entry, hop, exit, landing} {
		set, err := h.reconnectProbeTargets([]int64{node, node})
		if err != nil {
			t.Fatalf("reconnect %d: %v", node, err)
		}
		wantNodes := []int64{entry}
		if node != entry {
			wantNodes = append(wantNodes, node)
		}
		if !reflect.DeepEqual(set.relayIDs, []int64{id}) || !reflect.DeepEqual(set.latencyNodes, wantNodes) {
			t.Fatalf("reconnect %d: relays=%v nodes=%v", node, set.relayIDs, set.latencyNodes)
		}
		if !reflect.DeepEqual(set.tunnelIDs, []int64{tunnelID}) {
			t.Fatalf("reconnect %d: tunnels=%v", node, set.tunnelIDs)
		}
		if node == entry {
			if !reflect.DeepEqual(set.externalIDs, []int64{ext.ID}) {
				t.Fatalf("reconnect entry: external=%v", set.externalIDs)
			}
		} else if len(set.externalIDs) != 0 {
			t.Fatalf("reconnect %d: external=%v, want none", node, set.externalIDs)
		}
	}
}

func TestReconnectProbeReplacesStaleRelayAndLatency(t *testing.T) {
	h, r := setupLatencyHandler(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`)
	entry := mustCreateRelayNode(t, r, "entry", "198.51.100.1")
	other := mustCreateRelayNode(t, r, "other", "198.51.100.2")
	id := mustCreateRelayLine(t, r, "affected", 1, "203.0.113.1:443", entry, nil, 0)
	unrelated := mustCreateRelayLine(t, r, "unrelated", 1, "203.0.113.2:443", other, nil, 0)
	h.nodeLatency.replace(map[int64]externalProbe{entry: {CheckedAt: 1, Points: []latencyPoint{{Name: "电信", Error: "节点离线"}}}}, false)
	if err := r.UpdateNodeStatus(entry, 0); err != nil {
		t.Fatal(err)
	}
	stubLatencyPing(t, func(node int64, host string) (map[string]interface{}, error) {
		if node != entry {
			t.Errorf("unrelated node measured: %d", node)
		}
		if host == "ct.example.com" {
			return okPing(20), nil
		}
		return okPing(30), nil
	})
	initial, err := h.probeRelayLatency(context.Background(), []int64{id})
	if err != nil || initial[id].OK {
		t.Fatalf("expected offline line: %+v err=%v", initial[id], err)
	}
	h.relayLatency.replace(map[int64]relayLatency{unrelated: {externalProbe: externalProbe{CheckedAt: 123, OK: true}}}, false)
	if err := r.UpdateNodeStatus(entry, 1); err != nil {
		t.Fatal(err)
	}
	if err := h.probeReconnectedNodes(context.Background(), []int64{entry}); err != nil {
		t.Fatal(err)
	}
	got, _ := h.relayLatency.get(id)
	if !got.OK || len(got.Points) != 1 || !got.Points[0].OK || got.Points[0].LatencyMs != 50 || got.CheckedAt < initial[id].CheckedAt {
		t.Fatalf("line did not recover with fresh reference point: %+v", got)
	}
	if p, _ := h.relayLatency.get(unrelated); p.CheckedAt != 123 {
		t.Fatalf("unrelated line changed: %+v", p)
	}
	if p, _ := h.nodeLatency.get(entry); !p.OK || p.LatencyMs != 20 {
		t.Fatalf("node page would still show old latency: %+v", p)
	}
}

func TestReconnectNodeWithoutRelayOnlyRefreshesItsOwnLatency(t *testing.T) {
	h, r := setupLatencyHandler(t, `[{"name":"A","host":"a.example.com","port":80,"enabled":true}]`)
	id := mustCreateRelayNode(t, r, "no-relay", "198.51.100.1")
	other := mustCreateRelayNode(t, r, "other", "198.51.100.2")
	relay := mustCreateRelayLine(t, r, "unrelated", 1, "203.0.113.1:443", other, nil, 0)
	h.externalProbes.replace(map[int64]externalProbe{999: {CheckedAt: 123}}, false)
	calls := stubLatencyPing(t, func(node int64, host string) (map[string]interface{}, error) {
		if node != id || host != "a.example.com" {
			t.Errorf("unrelated measurement: node=%d host=%s", node, host)
		}
		return okPing(42), nil
	})
	if err := h.probeReconnectedNodes(context.Background(), []int64{id}); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.nodeLatency.get(id); !p.OK || p.LatencyMs != 42 || len(*calls) != 1 {
		t.Fatalf("node result=%+v calls=%v", p, *calls)
	}
	if _, ok := h.relayLatency.get(relay); ok {
		t.Fatal("empty affected-relay list triggered a full relay round")
	}
	if p, ok := h.externalProbes.get(999); !ok || p.CheckedAt != 123 {
		t.Fatal("empty affected-external list triggered a full port check")
	}
}

func TestReconnectWorkerCoalescesBursts(t *testing.T) {
	h, r := setupLatencyHandler(t, "")
	entry := mustCreateRelayNode(t, r, "entry", "198.51.100.1")
	id := mustCreateRelayLine(t, r, "line", 1, "203.0.113.1:443", entry, nil, 0)
	queue := newNodeReconnectProbeQueue()
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var count atomic.Int32
	stubLatencyPing(t, func(int64, string) (map[string]interface{}, error) {
		n := count.Add(1)
		started <- struct{}{}
		if n == 1 {
			<-release
		}
		return okPing(10), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.jobsWG.Add(1) // 生产侧由 StartBackgroundJobs 的 Add(16) 承担配对
	go func() {
		defer close(done)
		h.runNodeReconnectProbes(ctx, queue, 10*time.Millisecond)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("reconnect worker did not stop")
		}
	}()
	wait := func() {
		t.Helper()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("reconnect did not trigger detection")
		}
	}
	for i := 0; i < 100; i++ {
		queue.add(entry)
	}
	wait()
	for i := 0; i < 100; i++ {
		queue.add(entry)
	}
	close(release)
	wait()
	select {
	case <-started:
		t.Fatal("duplicate reconnect probes")
	case <-time.After(40 * time.Millisecond):
	}
	cancel()
	<-done
	if got, _ := h.relayLatency.get(id); !got.OK || count.Load() != 2 {
		t.Fatalf("result=%+v probes=%d", got, count.Load())
	}
}

func TestRelayProbeOlderRoundCannotOverwriteNewer(t *testing.T) {
	h, r := setupLatencyHandler(t, "")
	entry := mustCreateRelayNode(t, r, "entry", "198.51.100.1")
	id := mustCreateRelayLine(t, r, "line", 1, "203.0.113.1:443", entry, nil, 0)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	stubLatencyPing(t, func(int64, string) (map[string]interface{}, error) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-release
			return nil, errors.New("节点离线")
		}
		return okPing(10), nil
	})
	older := make(chan struct{})
	go func() {
		defer close(older)
		_, _ = h.probeRelayLatency(context.Background(), []int64{id})
	}()
	<-started
	if _, err := h.probeRelayLatency(context.Background(), []int64{id}); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.relayLatency.get(id); !got.OK {
		t.Fatalf("newer result not stored: %+v", got)
	}
	close(release)
	<-older
	if got, _ := h.relayLatency.get(id); !got.OK {
		t.Fatalf("older round overwrote the newer result: %+v", got)
	}
}
