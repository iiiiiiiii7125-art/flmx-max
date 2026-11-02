package handler

import (
	"fmt"
	"net/http"
	"testing"
)

// R2-019 回归（B-traffic B-T-01）：入账批次的归属分类层
// buildNodeFlowUploadBatch 有三条归属查询（peer_share_runtime /
// peer_share_resource / forward_port）。修复前任一查询出错只记一行
// 日志即返回空批次，v1/v2 两端点照常推进 seq/游标并回确认，agent
// 随即清掉待确认批次——整批流量静默丢失且不可恢复。
//
// 本文件按「三条查询逐条注错 × 两端点」矩阵钉死 fail-closed 口径：
// 注错期间整批拒收（v1 回 error、v2 回 503）、不入账、不推进
// seq/游标；故障消除后同批重报必须恰好入账一次（metas 先行拒收的
// 既有安全路径口径不得退化）。

// hideFlowTable 把一张归属查询依赖的表改名移走，使对应 repo 查询
// 必然出错；返回的恢复函数把表名改回。
func hideFlowTable(t *testing.T, h *Handler, table string) (restore func()) {
	t.Helper()
	db := h.repo.DB()
	if err := db.Exec(fmt.Sprintf("ALTER TABLE %s RENAME TO %s__hidden", table, table)).Error; err != nil {
		t.Fatalf("hide table %s: %v", table, err)
	}
	return func() {
		if err := db.Exec(fmt.Sprintf("ALTER TABLE %s__hidden RENAME TO %s", table, table)).Error; err != nil {
			t.Fatalf("restore table %s: %v", table, err)
		}
	}
}

var flowOwnershipQueryTables = []string{
	"peer_share_runtime",  // 查询一：ListActiveForwardPeerShareRuntimesByNode
	"peer_share_resource", // 查询二：ListPeerShareResourcesByNode
	"forward_port",        // 查询三：ListForwardIDsByNode
}

func TestFlowUploadV1OwnershipLookupFailureRejectsBatch(t *testing.T) {
	for _, table := range flowOwnershipQueryTables {
		t.Run(table, func(t *testing.T) {
			h := newF01Handler(t)
			fwdID := seedF01Forward(t, h)
			items := fmt.Sprintf(`[{"N":"%d_1_0","U":70,"D":50}]`, fwdID)
			seq := int64(7)
			body := f01EncryptEnvelope(t, f01TestSecret, []byte(items))

			restore := hideFlowTable(t, h, table)
			if got := f01PostFlowUpload(t, h, f01TestSecret, body, &seq); got != "error" {
				t.Fatalf("ownership lookup failure (%s) must reject the batch, got %q", table, got)
			}
			restore()

			if in, out := v2ForwardFlow(t, h, fwdID); in != 0 || out != 0 {
				t.Fatalf("rejected batch must not be accounted: in=%d out=%d", in, out)
			}
			if _, ok := f01Seq(t, h, 1); ok {
				t.Fatal("rejected batch must not advance seq")
			}

			// 故障消除后同 seq 重报：回 ok 且恰好入账一次。
			if got := f01PostFlowUpload(t, h, f01TestSecret, body, &seq); got != "ok" {
				t.Fatalf("retry after recovery must return ok, got %q", got)
			}
			if in, out := v2ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
				t.Fatalf("retry must be accounted exactly once: in=%d out=%d, want 50/70", in, out)
			}
			if lastSeq, ok := f01Seq(t, h, 1); !ok || lastSeq != seq {
				t.Fatalf("retry must advance seq to %d, got %d ok=%v", seq, lastSeq, ok)
			}
		})
	}
}

// 包装层契约：三条查询逐条注错时 buildNodeFlowUploadBatch 必须
// 返回非 nil error 且批次为零值；健康库上返回 nil error。
func TestBuildNodeFlowUploadBatchPropagatesOwnershipErrors(t *testing.T) {
	for _, table := range flowOwnershipQueryTables {
		t.Run(table, func(t *testing.T) {
			h := newF01Handler(t)
			seedF01Forward(t, h)
			if _, err := h.buildNodeFlowUploadBatch(1, nil, nil); err != nil {
				t.Fatalf("healthy repo must not error: %v", err)
			}
			restore := hideFlowTable(t, h, table)
			batch, err := h.buildNodeFlowUploadBatch(1, nil, nil)
			restore()
			if err == nil {
				t.Fatalf("ownership lookup failure (%s) must propagate an error", table)
			}
			if len(batch.flowDeltas) != 0 || len(batch.quotaUsage) != 0 || len(batch.peerShareUsage) != 0 {
				t.Fatalf("failed lookup must return a zero batch, got %+v", batch)
			}
		})
	}
}

func TestFlowUploadV2OwnershipLookupFailureRejectsBatch(t *testing.T) {
	for _, table := range flowOwnershipQueryTables {
		t.Run(table, func(t *testing.T) {
			h := newF01Handler(t)
			fwdID := seedF01Forward(t, h)
			items := fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID)

			restore := hideFlowTable(t, h, table)
			code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, items))
			if code != http.StatusServiceUnavailable {
				t.Fatalf("ownership lookup failure (%s) must reject with 503, got code=%d ack=%+v", table, code, ack)
			}
			restore()

			if _, ok := v2Cursor(t, h, 1, v2StreamA); ok {
				t.Fatal("rejected batch must not create/advance the stream cursor")
			}
			if in, out := v2ForwardFlow(t, h, fwdID); in != 0 || out != 0 {
				t.Fatalf("rejected batch must not be accounted: in=%d out=%d", in, out)
			}

			// 故障消除后同 stream 同序号重报：回确认且恰好入账一次。
			code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, items))
			if code != http.StatusOK || ack.Stream != v2StreamA || ack.Sequence != 1 || ack.Error != "" {
				t.Fatalf("retry after recovery must ack: code=%d ack=%+v", code, ack)
			}
			if in, out := v2ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
				t.Fatalf("retry must be accounted exactly once: in=%d out=%d, want 50/70", in, out)
			}
			cursor, ok := v2Cursor(t, h, 1, v2StreamA)
			if !ok || cursor.Sequence != 1 {
				t.Fatalf("retry must advance cursor to seq=1: %+v ok=%v", cursor, ok)
			}
		})
	}
}
