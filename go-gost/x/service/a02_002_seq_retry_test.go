package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/crypto"
)

// A02-002 回归（可靠上报 spool 版）：同一批计数的重试必须原样重发
// （同 stream、同 sequence、同字节）。面板契约是「同身份重报」：
// 已入账批次的重放回确认不重复入账，未入账批次待原样重发。旧 v1
// 实现每次尝试都 nextSeq 换新号——面板已入账但响应丢失时，同批被
// 当新批次二次入账；且批次只活在内存，进程死亡即丢。现批次先落盘
// spool 才发送：重试读的是 spool 里的待确认批次，期间新计入的
// 流量只进 Buffered、不污染重发批次，确认后另组新批（sequence+1）。

type capturedEnvelope struct {
	raw string
	env trafficEnvelope
}

// stubV2Panel 安装一个脚本化 v2 面板桩：解密请求信封、记录明文，
// 再按 respond 决定回执（网络错误/确认/拒绝）。返回捕获列表指针。
func stubV2Panel(t *testing.T, secret string, respond func(env trafficEnvelope) (*http.Response, error)) *[]capturedEnvelope {
	t.Helper()
	c, err := crypto.NewAESCrypto(secret)
	if err != nil {
		t.Fatal(err)
	}
	httpAESCrypto = c
	httpReportURL = "http://panel.example.com/flow/upload?secret=" + secret

	captured := &[]capturedEnvelope{}
	reportDo = func(_ context.Context, req *http.Request, _ time.Duration) (*http.Response, error) {
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
		var env trafficEnvelope
		if jerr := json.Unmarshal(plain, &env); jerr != nil {
			t.Fatalf("decrypt 后必须是 v2 信封: %v", jerr)
		}
		*captured = append(*captured, capturedEnvelope{raw: string(plain), env: env})
		return respond(env)
	}
	return captured
}

func v2AckResponse(env trafficEnvelope) (*http.Response, error) {
	body, _ := json.Marshal(map[string]any{"stream": env.Stream, "sequence": env.Sequence})
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}, nil
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

	failNext := true
	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		if failNext {
			// 模拟「面板已处理但响应丢失」：agent 侧只看到失败。
			return nil, errors.New("response lost")
		}
		return v2AckResponse(env)
	})

	spool, err := openTrafficSpool("")
	if err != nil {
		t.Fatal(err)
	}
	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"svc-seq": {ServiceName: "svc-seq", UpBytes: 1111, DownBytes: 2222},
		},
		spool: spool,
		ctx:   context.Background(),
	}

	// 尝试 1：失败。批次已落盘冻结，计数快照已转入 spool（内存清零
	// 不等于丢失——数值在 spool 里）。
	m.collectAndReport()
	if len(*captured) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(*captured))
	}
	first := (*captured)[0]
	if first.env.Sequence != 1 {
		t.Fatalf("首批 sequence 应为 1, got %d", first.env.Sequence)
	}
	if m.spool.state.Pending == nil || len(m.spool.state.Pending.Items) != 1 ||
		m.spool.state.Pending.Items[0].U != 1111 || m.spool.state.Pending.Items[0].D != 2222 {
		t.Fatalf("失败后待确认批次应冻结在 spool: %+v", m.spool.state.Pending)
	}

	// 冻结期间新计入流量：不得并入重发批次。
	m.addServiceTraffic("svc-seq", 5, 5)

	// 尝试 2：仍失败。必须同 stream、同 sequence、同字节。
	m.collectAndReport()
	if len(*captured) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(*captured))
	}
	if (*captured)[1].raw != first.raw {
		t.Fatalf("重试字节与首发不一致:\n%s\n%s", first.raw, (*captured)[1].raw)
	}

	// 尝试 3：成功确认。待确认批次清除，序号推进到 2。
	failNext = false
	m.collectAndReport()
	if len(*captured) != 3 || (*captured)[2].raw != first.raw {
		t.Fatalf("成功尝试应仍为同一批次原样重发: %+v", *captured)
	}
	if m.spool.state.Pending != nil {
		t.Fatalf("确认后待确认批次应清除, got %+v", m.spool.state.Pending)
	}
	if m.spool.state.Next != 2 {
		t.Fatalf("确认后 Next 应为 2, got %d", m.spool.state.Next)
	}

	// 尝试 4：新批次、sequence=2、只含冻结期间新增的 5/5。
	m.collectAndReport()
	if len(*captured) != 4 {
		t.Fatalf("expected 4 attempts, got %d", len(*captured))
	}
	second := (*captured)[3]
	if second.env.Sequence != 2 || second.env.Stream != first.env.Stream {
		t.Fatalf("新批次应同 stream sequence=2, got stream=%s seq=%d", second.env.Stream, second.env.Sequence)
	}
	if len(second.env.Items) != 1 || second.env.Items[0].U != 5 || second.env.Items[0].D != 5 {
		t.Fatalf("新批次应只含 5/5: %+v", second.env.Items)
	}
	if m.spool.state.Pending != nil || len(m.spool.state.Buffered) != 0 {
		t.Fatalf("全部确认后 spool 应排空: %+v", m.spool.state)
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

	ack := false
	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		if !ack {
			// 面板未入账时回非确认响应（纯文本 error）：agent 不得
			// 当作确认清批次。
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("error")),
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
			"svc-seq2": {ServiceName: "svc-seq2", UpBytes: 7, DownBytes: 8},
		},
		spool: spool,
		ctx:   context.Background(),
	}

	m.collectAndReport()
	m.collectAndReport()
	if len(*captured) != 2 || (*captured)[0].raw != (*captured)[1].raw {
		t.Fatalf("非确认响应后重试必须原样重发, got %+v", *captured)
	}
	if m.spool.state.Pending == nil {
		t.Fatal("非确认响应后待确认批次被清除")
	}

	ack = true
	m.collectAndReport()
	if m.spool.state.Pending != nil {
		t.Fatal("收到确认后待确认批次应清除")
	}
}
