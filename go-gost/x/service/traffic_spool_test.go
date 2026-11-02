package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 可靠上报 spool 回归：进程重启续报、停机不丢尾量、面板游标回退
// （reset_stream）原字节重报、三路自愈的隔离/二分。

func TestSpoolPendingSurvivesProcessRestart(t *testing.T) {
	origReportDo := reportDo
	origReportURL := httpReportURL
	origAESCrypto := httpAESCrypto
	defer func() {
		reportDo = origReportDo
		httpReportURL = origReportURL
		httpAESCrypto = origAESCrypto
	}()

	ack := false
	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		if !ack {
			return nil, errors.New("panel unreachable")
		}
		return v2AckResponse(env)
	})

	spoolPath := filepath.Join(t.TempDir(), "spool.json")

	// 第一代进程：落盘后发送失败，进程「死亡」。
	spool1, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	m1 := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"svc-a": {ServiceName: "svc-a", UpBytes: 100, DownBytes: 200},
		},
		spool: spool1,
		ctx:   context.Background(),
	}
	m1.collectAndReport()
	if len(*captured) != 1 {
		t.Fatalf("expected 1 attempt before restart, got %d", len(*captured))
	}
	first := (*captured)[0]

	// 第二代进程：从同一 spool 文件恢复，待确认批次原样续报。
	spool2, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	if spool2.state.Pending == nil {
		t.Fatal("重启后待确认批次丢失")
	}
	m2 := &GlobalTrafficManager{
		serviceTraffic: make(map[string]*ServiceTraffic),
		spool:          spool2,
		ctx:            context.Background(),
	}
	ack = true
	m2.collectAndReport()
	if len(*captured) != 2 {
		t.Fatalf("expected resume attempt after restart, got %d", len(*captured))
	}
	if (*captured)[1].raw != first.raw {
		t.Fatalf("重启续报字节与首发不一致:\n%s\n%s", first.raw, (*captured)[1].raw)
	}
	if spool2.state.Pending != nil || spool2.state.Next != 2 {
		t.Fatalf("续报确认后 spool 应排空且 Next=2: %+v", spool2.state)
	}
	// 落盘终态同样排空（再开一代进程无残留重报）。
	spool3, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	if spool3.state.Pending != nil || len(spool3.state.Buffered) != 0 {
		t.Fatalf("落盘终态应无残留: %+v", spool3.state)
	}
}

func TestManagerStopPersistsTailTraffic(t *testing.T) {
	spoolPath := filepath.Join(t.TempDir(), "spool.json")
	spool, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"svc-tail": {ServiceName: "svc-tail", UpBytes: 42, DownBytes: 24},
		},
		spool: spool,
		ctx:   context.Background(),
	}

	// Stop 不发送、只落盘：尾量必须出现在 spool 文件里待续报。
	m.Stop()

	reopened, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	pending := reopened.state.Pending
	if pending == nil || len(pending.Items) != 1 ||
		pending.Items[0].N != "svc-tail" || pending.Items[0].U != 42 || pending.Items[0].D != 24 {
		t.Fatalf("停机尾量应落盘进待确认批次: %+v", pending)
	}
	if up, down := m.GetServiceTraffic("svc-tail"); up != 0 || down != 0 {
		t.Fatalf("落盘后计数应清零: %d/%d", up, down)
	}
}

func TestCollectAndReportResetStreamReplaysSameItems(t *testing.T) {
	origReportDo := reportDo
	origReportURL := httpReportURL
	origAESCrypto := httpAESCrypto
	defer func() {
		reportDo = origReportDo
		httpReportURL = origReportURL
		httpAESCrypto = origAESCrypto
	}()

	reset := true
	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		if reset {
			// 模拟面板备份恢复：游标已回退，序号对不上 → reset_stream。
			body, _ := json.Marshal(map[string]any{
				"stream": env.Stream, "sequence": env.Sequence, "error": "reset_stream",
			})
			return &http.Response{
				StatusCode: http.StatusConflict,
				Body:       io.NopCloser(strings.NewReader(string(body))),
			}, nil
		}
		return v2AckResponse(env)
	})

	spool, err := openTrafficSpool("")
	if err != nil {
		t.Fatal(err)
	}
	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"svc-r": {ServiceName: "svc-r", UpBytes: 9, DownBytes: 9},
		},
		spool: spool,
		ctx:   context.Background(),
	}
	oldStream := spool.state.Stream

	m.collectAndReport()
	if m.spool.state.Stream == oldStream {
		t.Fatal("reset_stream 后应换新 stream 身份")
	}
	pending := m.spool.state.Pending
	if pending == nil || pending.Sequence != 1 || len(pending.Items) != 1 ||
		pending.Items[0].U != 9 || pending.Items[0].D != 9 {
		t.Fatalf("reset 后批次应原字节保留、新 stream seq=1: %+v", pending)
	}

	reset = false
	m.collectAndReport()
	if len(*captured) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(*captured))
	}
	if (*captured)[1].env.Stream == oldStream || (*captured)[1].env.Sequence != 1 {
		t.Fatalf("重报应走新 stream seq=1: %+v", (*captured)[1].env)
	}
	if m.spool.state.Pending != nil {
		t.Fatal("重报确认后待确认批次应排空")
	}
}

func TestRecoverRejectedIdentityConflictQuarantines(t *testing.T) {
	spoolPath := filepath.Join(t.TempDir(), "spool.json")
	spool, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	spool.state.Pending = &trafficEnvelope{
		Stream: spool.state.Stream, Sequence: spool.state.Next,
		Items: []TrafficReportItem{{N: "svc-x", U: 1, D: 2}},
	}
	oldStream := spool.state.Stream

	if err := spool.recoverRejected(trafficRejectionIdentityConflict); err != nil {
		t.Fatal(err)
	}
	if spool.state.Pending != nil {
		t.Fatal("冲突批次隔离后待确认应清空（绝不重放）")
	}
	if spool.state.Stream == oldStream || spool.state.Next != 1 {
		t.Fatal("隔离后应换新 stream、序号从 1 起")
	}
	matches, err := filepath.Glob(spoolPath + ".rejected-*.json")
	if err != nil || len(matches) != 1 {
		t.Fatalf("应留一份隔离存档: %v %v", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "svc-x") || !strings.Contains(string(data), "identity_conflict") {
		t.Fatalf("隔离存档应含原批次与原因: %s", data)
	}
	// 落盘状态一致：重开 spool 仍是新身份、无待确认。
	reopened, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.state.Pending != nil || reopened.state.Stream != spool.state.Stream {
		t.Fatalf("落盘状态与内存不一致: %+v", reopened.state)
	}
}

func TestRecoverRejectedInvalidBatchBisects(t *testing.T) {
	spoolPath := filepath.Join(t.TempDir(), "spool.json")
	spool, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	items := []TrafficReportItem{
		{N: "svc-1", U: 1, D: 1}, {N: "svc-2", U: 2, D: 2},
		{N: "svc-3", U: 3, D: 3}, {N: "svc-4", U: 4, D: 4},
	}
	spool.state.Pending = &trafficEnvelope{
		Stream: spool.state.Stream, Sequence: spool.state.Next, Items: items,
	}

	if err := spool.recoverRejected(trafficRejectionInvalidBatch); err != nil {
		t.Fatal(err)
	}
	pending := spool.state.Pending
	if pending == nil || len(pending.Items) != 2 || pending.Items[0].N != "svc-1" || pending.Items[1].N != "svc-2" {
		t.Fatalf("二分后待确认应为健康前半: %+v", pending)
	}
	if len(spool.state.Retry) != 1 || len(spool.state.Retry[0]) != 2 ||
		spool.state.Retry[0][0].N != "svc-3" || spool.state.Retry[0][1].N != "svc-4" {
		t.Fatalf("二分后重试队列应为后半: %+v", spool.state.Retry)
	}

	// 前半确认后，persist 应优先把 Retry 后半组为下一批。
	spool.state.Pending = nil
	spool.state.Next = 2
	m := &GlobalTrafficManager{
		serviceTraffic: make(map[string]*ServiceTraffic),
		spool:          spool,
		ctx:            context.Background(),
	}
	if err := m.persistTraffic(); err != nil {
		t.Fatal(err)
	}
	pending = spool.state.Pending
	if pending == nil || pending.Sequence != 2 || len(pending.Items) != 2 || pending.Items[0].N != "svc-3" {
		t.Fatalf("Retry 后半应组为 sequence=2 的下一批: %+v", pending)
	}
}
