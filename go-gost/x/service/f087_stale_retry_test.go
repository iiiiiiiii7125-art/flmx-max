package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// F-087 回归（可靠上报 spool 版）：stale（已注销）服务的最后一批
// 流量不得静默丢失。旧实现是内存冻结批次：条目先删后发、发送失败
// 即永久丢失。现保证由 spool 落盘接管——计数快照先落盘进待确认
// 批次（先落盘后清内存），发送失败时字节仍在 spool 里待下一轮
// 重发，内存条目删与不删不再决定生死。
func TestCollectAndReportKeepsStaleEntryWhenReportFails(t *testing.T) {
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
	spool, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"stale-service": {ServiceName: "stale-service", UpBytes: 10, DownBytes: 20},
		},
		spool: spool,
		ctx:   context.Background(),
	}

	// 第一轮：上报通道失败。字节必须已落盘在 spool 待确认批次里。
	m.collectAndReport()
	if len(*captured) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(*captured))
	}
	reopened, err := openTrafficSpool(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	pending := reopened.state.Pending
	if pending == nil || len(pending.Items) != 1 ||
		pending.Items[0].N != "stale-service" || pending.Items[0].U != 10 || pending.Items[0].D != 20 {
		t.Fatalf("上报失败后 stale 流量应落盘在 spool 待确认批次: %+v", pending)
	}

	// 第二轮：上报成功，待确认批次排空。
	ack = true
	m.collectAndReport()
	if m.spool.state.Pending != nil {
		t.Fatal("上报成功后待确认批次应排空")
	}
}

// F-089 回归（service 侧）：加密器缺失（secret 为空/无效）时上报必须
// 报错且不得发出任何明文请求，而不是静默回退明文发送。
func TestBatchReportRefusesPlaintextWhenCryptoMissing(t *testing.T) {
	origReportDo := reportDo
	origReportURL := httpReportURL
	origAESCrypto := httpAESCrypto
	defer func() {
		reportDo = origReportDo
		httpReportURL = origReportURL
		httpAESCrypto = origAESCrypto
	}()

	httpAESCrypto = nil
	httpReportURL = "http://panel.example.com/flow/upload?secret=abc"

	called := false
	reportDo = func(_ context.Context, _ *http.Request, _ time.Duration) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}

	_, err := sendBatchTrafficReportWithSeq(context.Background(),
		[]TrafficReportItem{{N: "svc", U: 1, D: 2}}, 7)
	if err == nil {
		t.Fatal("加密器缺失时上报必须报错，不得明文发送")
	}
	if called {
		t.Fatal("加密器缺失时不得发出任何 HTTP 请求（明文回退）")
	}
}
