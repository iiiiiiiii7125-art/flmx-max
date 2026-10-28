package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// F-01 回归：flowUpload 对失败 intake 必须回非 ok（fail-closed），
// 使 agent 保留计数下次重试；修复前以下用例一律回 ok（红），
// 修复后回 error（绿）。合法批次仍回 ok 且入账。

const f01TestSecret = "f01-failclosed-node-secret"

func newF01Handler(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "f01.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.DB().Create(&model.Node{ID: 1, Name: "f01-node", Secret: f01TestSecret, ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	return &Handler{repo: r}
}

// seedF01Forward 建一条可入账的转发：隧道双向计费、ratio=1。
// 返回 forward ID；上报服务名形如 "<forwardID>_<userID>_0"。
func seedF01Forward(t *testing.T, h *Handler) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	tunnel := model.Tunnel{Name: "f01-line", Type: 1, Protocol: "tcp", Flow: 2, TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := h.repo.DB().Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: "f01-line", TunnelID: tunnel.ID, RemoteAddr: "203.0.113.1:443", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := h.repo.DB().Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	// buildNodeFlowUploadBatch 按 forward_port 归属过滤：转发须在
	// 上报节点上有端口行，否则整条丢弃（非本节点流量）。
	if err := h.repo.DB().Create(&model.ForwardPort{ForwardID: fwd.ID, NodeID: 1, Port: 45001}).Error; err != nil {
		t.Fatal(err)
	}
	return fwd.ID
}

func f01EncryptEnvelope(t *testing.T, secret string, plain []byte) []byte {
	t.Helper()
	crypto, err := security.NewAESCrypto(secret)
	if err != nil {
		t.Fatal(err)
	}
	data, err := crypto.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]interface{}{
		"encrypted": true,
		"data":      data,
		"timestamp": time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func f01PostFlowUpload(t *testing.T, h *Handler, secret string, body []byte, seq *int64) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/flow/upload?secret="+secret, bytes.NewReader(body))
	if seq != nil {
		req.Header.Set("X-Flmx-Seq", fmt.Sprintf("%d", *seq))
	}
	rec := httptest.NewRecorder()
	h.flowUpload(rec, req)
	return strings.TrimSpace(rec.Body.String())
}

func f01Seq(t *testing.T, h *Handler, nodeID int64) (int64, bool) {
	t.Helper()
	var row repo.NodeSeq
	if err := h.repo.DB().Where("node_id = ?", nodeID).First(&row).Error; err != nil {
		return 0, false
	}
	return row.LastSeq, true
}

func TestFlowUploadUnknownSecretReturnsError(t *testing.T) {
	h := newF01Handler(t)
	body := []byte(`[{"N":"1_1_0","U":10,"D":20}]`)
	if got := f01PostFlowUpload(t, h, "no-such-secret", body, nil); got == "ok" {
		t.Fatalf("unknown secret must not return ok, got %q", got)
	}
}

func TestFlowUploadDecryptFailureReturnsError(t *testing.T) {
	h := newF01Handler(t)
	// 用错误 secret 加密：面板侧 GCM 解密失败。
	body := f01EncryptEnvelope(t, "wrong-secret", []byte(`[{"N":"1_1_0","U":10,"D":20}]`))
	if got := f01PostFlowUpload(t, h, f01TestSecret, body, nil); got == "ok" {
		t.Fatalf("decrypt failure must not return ok, got %q", got)
	}
}

func TestFlowUploadUnparsableBodyReturnsError(t *testing.T) {
	h := newF01Handler(t)
	body := []byte(`this is not json {{{`)
	if got := f01PostFlowUpload(t, h, f01TestSecret, body, nil); got == "ok" {
		t.Fatalf("unparsable body must not return ok, got %q", got)
	}
	body = []byte(`{"foo":1}`)
	if got := f01PostFlowUpload(t, h, f01TestSecret, body, nil); got == "ok" {
		t.Fatalf("unparsable JSON object must not return ok, got %q", got)
	}
}

func TestFlowUploadEmptyBodyStillOk(t *testing.T) {
	h := newF01Handler(t)
	if got := f01PostFlowUpload(t, h, f01TestSecret, []byte(``), nil); got != "ok" {
		t.Fatalf("empty body must still return ok, got %q", got)
	}
}

func TestReadAndDecryptFlowBodyDecryptFailureReturnsError(t *testing.T) {
	body := f01EncryptEnvelope(t, "wrong-secret", []byte(`[{"N":"1_1_0","U":10,"D":20}]`))
	req := httptest.NewRequest(http.MethodPost, "/flow/upload", bytes.NewReader(body))
	if _, err := readAndDecryptFlowBody(req.Body, f01TestSecret); err == nil {
		t.Fatal("decrypt failure must return error, got nil")
	}
}

func TestFlowUploadValidBatchOkAndAccountedThenReplayOk(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	items := fmt.Sprintf(`[{"N":"%d_1_0","U":70,"D":50}]`, fwdID)
	seq := int64(7)
	body := f01EncryptEnvelope(t, f01TestSecret, []byte(items))
	if got := f01PostFlowUpload(t, h, f01TestSecret, body, &seq); got != "ok" {
		t.Fatalf("valid batch must return ok, got %q", got)
	}
	var fwd model.Forward
	if err := h.repo.DB().First(&fwd, fwdID).Error; err != nil {
		t.Fatal(err)
	}
	// 双向计费、ratio=1：InFlow=D=50、OutFlow=U=70。
	if fwd.InFlow != 50 || fwd.OutFlow != 70 {
		t.Fatalf("valid batch must be accounted: got in=%d out=%d, want in=50 out=70", fwd.InFlow, fwd.OutFlow)
	}
	if lastSeq, ok := f01Seq(t, h, 1); !ok || lastSeq != seq {
		t.Fatalf("seq must advance to %d, got %d ok=%v", seq, lastSeq, ok)
	}
	// 同 seq 重放：回 ok、不重复入账。
	if got := f01PostFlowUpload(t, h, f01TestSecret, body, &seq); got != "ok" {
		t.Fatalf("replay must return ok, got %q", got)
	}
	if err := h.repo.DB().First(&fwd, fwdID).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 50 || fwd.OutFlow != 70 {
		t.Fatalf("replay must not double-account: got in=%d out=%d", fwd.InFlow, fwd.OutFlow)
	}
}

func TestFlowUploadDecryptFailureDoesNotConsumeSeq(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	items := fmt.Sprintf(`[{"N":"%d_1_0","U":70,"D":50}]`, fwdID)
	seq := int64(11)
	// 先用错 secret 加密同 seq 上报：必须回 error、seq 未推进。
	badBody := f01EncryptEnvelope(t, "wrong-secret", []byte(items))
	if got := f01PostFlowUpload(t, h, f01TestSecret, badBody, &seq); got == "ok" {
		t.Fatalf("decrypt failure must not return ok, got %q", got)
	}
	if _, ok := f01Seq(t, h, 1); ok {
		t.Fatal("decrypt failure must not advance seq")
	}
	// agent 同 seq 重报（正确 secret）：必须回 ok 且入账一次。
	goodBody := f01EncryptEnvelope(t, f01TestSecret, []byte(items))
	if got := f01PostFlowUpload(t, h, f01TestSecret, goodBody, &seq); got != "ok" {
		t.Fatalf("retry with same seq must return ok, got %q", got)
	}
	var fwd model.Forward
	if err := h.repo.DB().First(&fwd, fwdID).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 50 || fwd.OutFlow != 70 {
		t.Fatalf("retry must be accounted once: got in=%d out=%d", fwd.InFlow, fwd.OutFlow)
	}
}
