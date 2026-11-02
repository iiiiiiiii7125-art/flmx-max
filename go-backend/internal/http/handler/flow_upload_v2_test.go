package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// 可靠上报 v2 回归：stream 游标三路语义——正常入账＋同序号同内容
// 重放只确认不重入账、同序号不同内容 identity_conflict、序号缺口
// reset_stream、非法批次 invalid_batch；面板备份恢复（游标丢失）
// 后 agent 换新 stream 原字节重报可正常入账。

const (
	v2StreamA = "00112233445566778899aabbccddeeff"
	v2StreamB = "ffeeddccbbaa99887766554433221100"
)

type v2Ack struct {
	Stream   string `json:"stream"`
	Sequence int64  `json:"sequence"`
	Cursor   int64  `json:"cursor"`
	Error    string `json:"error"`
}

func v2Envelope(stream string, seq int64, items string) []byte {
	return []byte(fmt.Sprintf(`{"stream":"%s","sequence":%d,"items":%s}`, stream, seq, items))
}

func v2Post(t *testing.T, h *Handler, secret string, plain []byte) (int, v2Ack, string) {
	t.Helper()
	body := f01EncryptEnvelope(t, secret, plain)
	req := httptest.NewRequest(http.MethodPost, "/flow/upload/v2?secret="+secret, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.flowUploadV2(rec, req)
	var ack v2Ack
	_ = json.Unmarshal(rec.Body.Bytes(), &ack)
	return rec.Code, ack, rec.Body.String()
}

func v2ForwardFlow(t *testing.T, h *Handler, fwdID int64) (int64, int64) {
	t.Helper()
	var fwd model.Forward
	if err := h.repo.DB().First(&fwd, fwdID).Error; err != nil {
		t.Fatal(err)
	}
	return fwd.InFlow, fwd.OutFlow
}

func v2Cursor(t *testing.T, h *Handler, nodeID int64, stream string) (repo.TrafficStreamCursor, bool) {
	t.Helper()
	var row repo.TrafficStreamCursor
	scope := fmt.Sprintf("flow:%d:%s", nodeID, stream)
	if err := h.repo.DB().Where("scope = ?", scope).First(&row).Error; err != nil {
		return row, false
	}
	return row, true
}

func TestFlowUploadV2AckAndAccountingThenReplayAck(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	items := fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID)

	code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, items))
	if code != http.StatusOK || ack.Stream != v2StreamA || ack.Sequence != 1 || ack.Error != "" {
		t.Fatalf("valid v2 batch must ack: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
		t.Fatalf("v2 batch must be accounted: in=%d out=%d, want 50/70", in, out)
	}
	cursor, ok := v2Cursor(t, h, 1, v2StreamA)
	if !ok || cursor.Sequence != 1 || len(cursor.Digest) != 64 {
		t.Fatalf("cursor must record seq=1 with digest: %+v ok=%v", cursor, ok)
	}

	// 同 stream 同序号同内容重放：回确认、不重复入账。
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, items))
	if code != http.StatusOK || ack.Error != "" {
		t.Fatalf("replay must ack: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
		t.Fatalf("replay must not double-account: in=%d out=%d", in, out)
	}

	// 下一序号正常入账。
	more := fmt.Sprintf(`[{"n":"%d_1_0","u":30,"d":10}]`, fwdID)
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 2, more))
	if code != http.StatusOK || ack.Sequence != 2 {
		t.Fatalf("next sequence must ack: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 60 || out != 100 {
		t.Fatalf("second batch must be accounted: in=%d out=%d, want 60/100", in, out)
	}
}

func TestFlowUploadV2IdentityConflict(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	items := fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID)
	if code, _, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, items)); code != http.StatusOK {
		t.Fatalf("setup batch must ack, code=%d", code)
	}
	// 同序号、不同内容：identity_conflict，且不入账。
	other := fmt.Sprintf(`[{"n":"%d_1_0","u":71,"d":50}]`, fwdID)
	code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, other))
	if code != http.StatusConflict || ack.Error != "identity_conflict" {
		t.Fatalf("same seq different content must conflict: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
		t.Fatalf("conflict batch must not be accounted: in=%d out=%d", in, out)
	}
}

func TestFlowUploadV2GapResetStreamThenNewStreamReplays(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	items := fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID)
	if code, _, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, items)); code != http.StatusOK {
		t.Fatalf("setup batch must ack, code=%d", code)
	}

	// 序号缺口（面板游标在 1，agent 发 3）：reset_stream、不入账。
	skipped := fmt.Sprintf(`[{"n":"%d_1_0","u":1,"d":1}]`, fwdID)
	code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 3, skipped))
	if code != http.StatusConflict || ack.Error != "reset_stream" {
		t.Fatalf("sequence gap must reset_stream: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
		t.Fatalf("gap batch must not be accounted: in=%d out=%d", in, out)
	}

	// 模拟面板备份恢复后游标整行丢失：删游标行，agent 旧 stream
	// 高序号批次到达 → 同样 reset_stream。
	if err := h.repo.DB().Where("scope = ?", fmt.Sprintf("flow:1:%s", v2StreamA)).
		Delete(&repo.TrafficStreamCursor{}).Error; err != nil {
		t.Fatal(err)
	}
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 5, items))
	if code != http.StatusConflict || ack.Error != "reset_stream" {
		t.Fatalf("lost cursor with high sequence must reset_stream: code=%d ack=%+v", code, ack)
	}

	// agent 按回执换新 stream、原字节以 sequence=1 重报：正常入账。
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamB, 1, items))
	if code != http.StatusOK || ack.Stream != v2StreamB || ack.Sequence != 1 {
		t.Fatalf("new stream replay must ack: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 100 || out != 140 {
		t.Fatalf("new stream replay must be accounted: in=%d out=%d, want 100/140", in, out)
	}
}

func TestFlowUploadV2InvalidBatch(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	// 负计数：整批 invalid_batch、不入账、游标不推进。
	bad := fmt.Sprintf(`[{"n":"%d_1_0","u":-5,"d":50}]`, fwdID)
	code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, bad))
	if code != http.StatusBadRequest || ack.Error != "invalid_batch" {
		t.Fatalf("negative counter must invalid_batch: code=%d ack=%+v", code, ack)
	}
	if _, ok := v2Cursor(t, h, 1, v2StreamA); ok {
		t.Fatal("invalid batch must not create/advance cursor")
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 0 || out != 0 {
		t.Fatalf("invalid batch must not be accounted: in=%d out=%d", in, out)
	}
}

func TestFlowUploadV2RejectsMalformedEnvelopeAndUnknownSecret(t *testing.T) {
	h := newF01Handler(t)
	// stream 非 16 字节 hex：400（纯文本，非三路回执）。
	code, _, _ := v2Post(t, h, f01TestSecret, v2Envelope("not-a-stream", 1, `[]`))
	if code != http.StatusBadRequest {
		t.Fatalf("malformed stream must be 400, got %d", code)
	}
	// sequence 非正：400。
	code, _, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 0, `[]`))
	if code != http.StatusBadRequest {
		t.Fatalf("non-positive sequence must be 400, got %d", code)
	}
	// 未知 secret：401。
	body := f01EncryptEnvelope(t, f01TestSecret, v2Envelope(v2StreamA, 1, `[]`))
	req := httptest.NewRequest(http.MethodPost, "/flow/upload/v2?secret=nope", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.flowUploadV2(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown secret must be 401, got %d", rec.Code)
	}
}
