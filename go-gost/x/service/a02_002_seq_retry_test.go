package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/crypto"
)

// A02-002 回归：同一批计数的重试必须原样重发（同条目、同 seq）。
// 面板契约是「同 seq 重报」：已入账批次的重放回 ok 不重复入账，
// 未入账批次返非 ok 待同 seq 重发。旧实现每次尝试都 nextSeq 换
// 新号——面板已入账但响应丢失时，同批被当新批次二次入账。

type capturedReport struct {
	seq  int64
	body string
}

func TestCollectAndReportRetriesFrozenBatchWithSameSeq(t *testing.T) {
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
	httpReportURL = "http://panel.example.com/flow/upload?secret=<redacted>"

	var captured []capturedReport
	failNext := true
	reportDo = func(_ context.Context, req *http.Request, _ time.Duration) (*http.Response, error) {
		seq, _ := strconv.ParseInt(req.Header.Get("X-Flmx-Seq"), 10, 64)
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var wrapper struct {
			Encrypted bool   `json:"encrypted"`
			Data      string `json:"data"`
		}
		if jerr := json.Unmarshal(raw, &wrapper); jerr != nil || !wrapper.Encrypted {
			t.Fatalf("上报必须是加密信封: %v", jerr)
		}
		plain, derr := c.Decrypt(wrapper.Data)
		if derr != nil {
			t.Fatalf("decrypt: %v", derr)
		}
		captured = append(captured, capturedReport{seq: seq, body: string(plain)})
		if failNext {
			// 模拟「面板已处理但响应丢失」：agent 侧只看到失败。
			return nil, errors.New("response lost")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}

	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"svc-seq": {ServiceName: "svc-seq", UpBytes: 1111, DownBytes: 2222},
		},
		ctx: context.Background(),
	}

	// 尝试 1：失败。计数保留、批次冻结。
	m.collectAndReport()
	if len(captured) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(captured))
	}
	seq1 := captured[0].seq
	if up, down := m.GetServiceTraffic("svc-seq"); up != 1111 || down != 2222 {
		t.Fatalf("失败后计数被改动: %d/%d", up, down)
	}

	// 冻结期间新计入流量：不得并入重发批次。
	entry := m.serviceTraffic["svc-seq"]
	entry.mu.Lock()
	entry.UpBytes += 5
	entry.DownBytes += 5
	entry.mu.Unlock()

	// 尝试 2：仍失败。必须同 seq、同 body（1111/2222，不含 +5）。
	m.collectAndReport()
	if len(captured) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(captured))
	}
	if captured[1].seq != seq1 {
		t.Fatalf("重试换了 seq: %d -> %d，同批将被面板二次入账", seq1, captured[1].seq)
	}
	if captured[1].body != captured[0].body {
		t.Fatalf("重试 body 与首发不一致:\n%s\n%s", captured[0].body, captured[1].body)
	}
	if !strings.Contains(captured[1].body, "1111") || strings.Contains(captured[1].body, "1116") {
		t.Fatalf("冻结批次被新流量污染: %s", captured[1].body)
	}

	// 尝试 3：成功（面板或判重放回 ok、或首次入账）。只扣冻结量，
	// 冻结期间新计入的 5/5 保留待下一批。
	failNext = false
	m.collectAndReport()
	if len(captured) != 3 || captured[2].seq != seq1 {
		t.Fatalf("成功尝试应仍为同 seq: %+v", captured)
	}
	if up, down := m.GetServiceTraffic("svc-seq"); up != 5 || down != 5 {
		t.Fatalf("成功后应只剩冻结期新增的 5/5, got %d/%d", up, down)
	}

	// 尝试 4：新批次、新 seq（恰好 +1）、body 只含 5/5。
	m.collectAndReport()
	if len(captured) != 4 {
		t.Fatalf("expected 4 attempts, got %d", len(captured))
	}
	if captured[3].seq != seq1+1 {
		t.Fatalf("新批次 seq 应为 %d, got %d", seq1+1, captured[3].seq)
	}
	if !strings.Contains(captured[3].body, `"u":5`) || !strings.Contains(captured[3].body, `"d":5`) {
		t.Fatalf("新批次 body 应为 5/5: %s", captured[3].body)
	}
	if up, down := m.GetServiceTraffic("svc-seq"); up != 0 || down != 0 {
		t.Fatalf("全部上报后计数应清零: %d/%d", up, down)
	}
}

func TestCollectAndReportNonOKResponseKeepsBatchFrozen(t *testing.T) {
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
	httpReportURL = "http://panel.example.com/flow/upload?secret=<redacted>"

	var seqs []int64
	reportDo = func(_ context.Context, req *http.Request, _ time.Duration) (*http.Response, error) {
		seq, _ := strconv.ParseInt(req.Header.Get("X-Flmx-Seq"), 10, 64)
		seqs = append(seqs, seq)
		_, _ = io.ReadAll(req.Body)
		// 面板未入账时返非 ok（body 非 "ok"）。
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("error")),
		}, nil
	}

	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"svc-seq2": {ServiceName: "svc-seq2", UpBytes: 7, DownBytes: 8},
		},
		ctx: context.Background(),
	}

	m.collectAndReport()
	m.collectAndReport()
	if len(seqs) != 2 || seqs[0] != seqs[1] {
		t.Fatalf("非 ok 响应后重试必须复用同 seq, got %v", seqs)
	}
	if up, down := m.GetServiceTraffic("svc-seq2"); up != 7 || down != 8 {
		t.Fatalf("非 ok 后计数被改动: %d/%d", up, down)
	}
}
