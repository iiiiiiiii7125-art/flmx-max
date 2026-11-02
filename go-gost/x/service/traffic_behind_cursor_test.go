package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// C-C2-01 回归（agent 半边）：面板确认回执带游标位置（cursor）。
//
// 场景：本机 spool 被整机快照恢复/旧备份还原/整目录克隆回退，
// Next 落后于面板游标。面板对落后批次只确认不入账（其序号段已
// 入账过）；修复前 agent 无从知晓，Next 追平游标前的每个新批次
// 都被静默确认丢弃。修复后：回执 cursor 严格大于所发序号即判
// 落后——已确认批次照常清除（绝不重发、无双计），stream 当场
// 轮换，后续未入账字节改走新 stream 从序号 1 重新入账。

func v2AckResponseWithCursor(env trafficEnvelope, cursor int64) (*http.Response, error) {
	body, _ := json.Marshal(map[string]any{
		"stream": env.Stream, "sequence": env.Sequence, "cursor": cursor,
	})
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}, nil
}

func TestCollectAndReportBehindCursorRotatesStreamWithoutResendingAckedBytes(t *testing.T) {
	origReportDo := reportDo
	origReportURL := httpReportURL
	origAESCrypto := httpAESCrypto
	defer func() {
		reportDo = origReportDo
		httpReportURL = origReportURL
		httpAESCrypto = origAESCrypto
	}()

	requests := 0
	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		requests++
		if requests == 1 {
			// 面板游标已在 3：本批（seq=1）属落后段，只确认不入账，
			// 回执带 cursor=3。
			return v2AckResponseWithCursor(env, 3)
		}
		// 之后按新面板正常口径：回执游标＝所发序号。
		return v2AckResponseWithCursor(env, env.Sequence)
	})

	spool, err := openTrafficSpool("")
	if err != nil {
		t.Fatal(err)
	}
	oldStream := spool.state.Stream
	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"svc-behind": {ServiceName: "svc-behind", UpBytes: 100, DownBytes: 200},
		},
		spool: spool,
		ctx:   context.Background(),
	}

	// 第一批：被 behind 确认。批次清除、stream 必须轮换、Next 归 1。
	m.collectAndReport()
	if len(*captured) != 1 || (*captured)[0].env.Sequence != 1 || (*captured)[0].env.Stream != oldStream {
		t.Fatalf("首批应以旧 stream seq=1 发出: %+v", *captured)
	}
	if m.spool.state.Pending != nil {
		t.Fatalf("behind 确认后待确认批次应清除: %+v", m.spool.state.Pending)
	}
	if m.spool.state.Stream == oldStream {
		t.Fatal("behind 确认后 stream 必须轮换，否则后续批次仍被静默丢弃")
	}
	if m.spool.state.Next != 1 {
		t.Fatalf("轮换后 Next 应为 1, got %d", m.spool.state.Next)
	}
	newStream := m.spool.state.Stream

	// 第二批（轮换后新计的流量）：必须走新 stream、序号 1，且只含
	// 新流量——已确认的 100/200 绝不重发（防双计的钉死断言）。
	m.addServiceTraffic("svc-behind", 7, 8)
	m.collectAndReport()
	if len(*captured) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(*captured))
	}
	second := (*captured)[1]
	if second.env.Stream != newStream || second.env.Sequence != 1 {
		t.Fatalf("第二批应走新 stream seq=1, got stream=%s seq=%d", second.env.Stream, second.env.Sequence)
	}
	if len(second.env.Items) != 1 || second.env.Items[0].U != 7 || second.env.Items[0].D != 8 {
		t.Fatalf("第二批只能含新流量 7/8（已确认字节不得重发）: %+v", second.env.Items)
	}
	// 第二批是正常确认（cursor==seq）：不得再次轮换，Next 推进到 2。
	if m.spool.state.Stream != newStream || m.spool.state.Next != 2 {
		t.Fatalf("正常确认后应留在新 stream 且 Next=2: stream=%s next=%d", m.spool.state.Stream, m.spool.state.Next)
	}
	if m.spool.state.Pending != nil || len(m.spool.state.Buffered) != 0 {
		t.Fatalf("全部确认后 spool 应排空: %+v", m.spool.state)
	}
}

func TestCollectAndReportAckWithoutCursorKeepsStream(t *testing.T) {
	origReportDo := reportDo
	origReportURL := httpReportURL
	origAESCrypto := httpAESCrypto
	defer func() {
		reportDo = origReportDo
		httpReportURL = origReportURL
		httpAESCrypto = origAESCrypto
	}()

	// 旧面板回执无 cursor 字段：行为必须与此前完全一致
	// （不轮换、Next++），新旧混跑不劣化。
	captured := stubV2Panel(t, "test-secret", func(env trafficEnvelope) (*http.Response, error) {
		return v2AckResponse(env)
	})

	spool, err := openTrafficSpool("")
	if err != nil {
		t.Fatal(err)
	}
	oldStream := spool.state.Stream
	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"svc-oldpanel": {ServiceName: "svc-oldpanel", UpBytes: 11, DownBytes: 22},
		},
		spool: spool,
		ctx:   context.Background(),
	}

	m.collectAndReport()
	if len(*captured) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*captured))
	}
	if m.spool.state.Stream != oldStream {
		t.Fatal("旧面板回执（无 cursor）不得触发 stream 轮换")
	}
	if m.spool.state.Next != 2 || m.spool.state.Pending != nil {
		t.Fatalf("旧面板确认后应 Next=2 且清批: next=%d pending=%+v", m.spool.state.Next, m.spool.state.Pending)
	}
}
