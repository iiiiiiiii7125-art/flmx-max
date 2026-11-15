package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 流量审计 A-中-1 回归：v2 信封的 dropped_bytes（agent 死信隔离
// 的未确认字节）必须入账到上报节点名下，只在批次 Applied 时记
// 一次——重放/落后批次不重复入账；旧 agent 不发本字段按 0 处理；
// 为负整批拒收回 invalid_batch。

func v2EnvelopeDropped(stream string, seq, dropped int64, items string) []byte {
	return []byte(fmt.Sprintf(`{"stream":"%s","sequence":%d,"dropped_bytes":%d,"items":%s}`, stream, seq, dropped, items))
}

func v2DroppedTotal(t *testing.T, h *Handler, nodeID int64) int64 {
	t.Helper()
	total, err := h.repo.SumTrafficNodeDroppedByNodes([]int64{nodeID})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func TestFlowUploadV2DroppedBytesBookedOnce(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	items := fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID)

	// 首批带死信 3333：入账并累计。
	code, ack, _ := v2Post(t, h, f01TestSecret, v2EnvelopeDropped(v2StreamA, 1, 3333, items))
	if code != http.StatusOK || ack.Error != "" {
		t.Fatalf("batch with dropped_bytes must ack: code=%d ack=%+v", code, ack)
	}
	if got := v2DroppedTotal(t, h, 1); got != 3333 {
		t.Fatalf("dropped total=%d, want 3333 after applied batch", got)
	}

	// 同序号同内容重放（含同一 dropped_bytes）：确认、不重复入账。
	code, ack, _ = v2Post(t, h, f01TestSecret, v2EnvelopeDropped(v2StreamA, 1, 3333, items))
	if code != http.StatusOK || ack.Error != "" {
		t.Fatalf("replay must ack: code=%d ack=%+v", code, ack)
	}
	if got := v2DroppedTotal(t, h, 1); got != 3333 {
		t.Fatalf("replay must not double-book dropped bytes: total=%d, want 3333", got)
	}

	// 下一序号、死信已结清（agent 侧确认后扣减为 0）：累计不变，
	// 流量本身照常入账。
	more := fmt.Sprintf(`[{"n":"%d_1_0","u":30,"d":10}]`, fwdID)
	code, ack, _ = v2Post(t, h, f01TestSecret, v2EnvelopeDropped(v2StreamA, 2, 0, more))
	if code != http.StatusOK || ack.Error != "" {
		t.Fatalf("next batch must ack: code=%d ack=%+v", code, ack)
	}
	if got := v2DroppedTotal(t, h, 1); got != 3333 {
		t.Fatalf("settled batch must not change dropped total: total=%d, want 3333", got)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 60 || out != 100 {
		t.Fatalf("traffic must still be accounted: in=%d out=%d, want 60/100", in, out)
	}

	// 第二次死信 100：累计到 3433。
	third := fmt.Sprintf(`[{"n":"%d_1_0","u":1,"d":1}]`, fwdID)
	code, ack, _ = v2Post(t, h, f01TestSecret, v2EnvelopeDropped(v2StreamA, 3, 100, third))
	if code != http.StatusOK || ack.Error != "" {
		t.Fatalf("third batch must ack: code=%d ack=%+v", code, ack)
	}
	if got := v2DroppedTotal(t, h, 1); got != 3433 {
		t.Fatalf("second dead letter must accumulate: total=%d, want 3433", got)
	}
}

func TestFlowUploadV2DroppedBytesCompatAndNegative(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	items := fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID)

	// 旧 agent：信封无 dropped_bytes 字段——照常确认、死信累计为 0。
	code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, items))
	if code != http.StatusOK || ack.Error != "" {
		t.Fatalf("legacy envelope without dropped_bytes must ack: code=%d ack=%+v", code, ack)
	}
	if got := v2DroppedTotal(t, h, 1); got != 0 {
		t.Fatalf("legacy batch must book no dropped bytes: total=%d, want 0", got)
	}

	// 为负：整批拒收 invalid_batch、不入账、不推进游标。
	code, ack, _ = v2Post(t, h, f01TestSecret, v2EnvelopeDropped(v2StreamA, 2, -5, items))
	if code != http.StatusBadRequest || ack.Error != "invalid_batch" {
		t.Fatalf("negative dropped_bytes must be invalid_batch: code=%d ack=%+v", code, ack)
	}
	if got := v2DroppedTotal(t, h, 1); got != 0 {
		t.Fatalf("rejected batch must book nothing: total=%d, want 0", got)
	}
}

func TestNodeTrafficDroppedEndpoint(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	items := fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID)
	if code, _, _ := v2Post(t, h, f01TestSecret, v2EnvelopeDropped(v2StreamA, 1, 3333, items)); code != http.StatusOK {
		t.Fatalf("setup batch must ack, code=%d", code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/traffic/dropped", nil)
	rec := httptest.NewRecorder()
	h.nodeTrafficDropped(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dropped endpoint must return 200: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int              `json:"code"`
		Data map[string]int64 `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("dropped endpoint must return JSON envelope: %v body=%s", err, rec.Body.String())
	}
	if resp.Code != 0 || resp.Data["1"] != 3333 {
		t.Fatalf("dropped endpoint data=%v, want {\"1\":3333}", resp.Data)
	}
}
