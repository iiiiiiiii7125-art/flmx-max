package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/crypto"
)

// F-087 回归：stale（已注销）服务的最后一批流量，上报失败时必须保留
// 待下一轮重试，不得在发送前就从 map 删除——先删后发、发送失败即
// 静默丢失（违 D1-010「失败则保留」本意）。
func TestCollectAndReportKeepsStaleEntryWhenReportFails(t *testing.T) {
	origReportDo := reportDo
	origReportURL := httpReportURL
	origAESCrypto := httpAESCrypto
	defer func() {
		reportDo = origReportDo
		httpReportURL = origReportURL
		httpAESCrypto = origAESCrypto
	}()

	c, err := crypto.NewAESCrypto("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	httpAESCrypto = c
	httpReportURL = "http://panel.example.com/flow/upload?secret=abc"

	// 第一轮：上报通道失败。
	reportDo = func(_ context.Context, _ *http.Request, _ time.Duration) (*http.Response, error) {
		return nil, errors.New("panel unreachable")
	}

	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"stale-service": {ServiceName: "stale-service", UpBytes: 10, DownBytes: 20},
		},
		ctx: context.Background(),
	}

	m.collectAndReport()

	entry, ok := m.serviceTraffic["stale-service"]
	if !ok {
		t.Fatal("上报失败后 stale 条目被删除，该批流量永久丢失")
	}
	if entry.UpBytes != 10 || entry.DownBytes != 20 {
		t.Fatalf("上报失败后计数被改动: up=%d down=%d, want 10/20", entry.UpBytes, entry.DownBytes)
	}

	// 第二轮：上报成功，条目才应被清理。
	reportDo = func(_ context.Context, _ *http.Request, _ time.Duration) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}
	m.collectAndReport()

	if _, ok := m.serviceTraffic["stale-service"]; ok {
		t.Fatal("上报成功后 stale 条目应被清理")
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
