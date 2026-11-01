package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// R4B03-003 回归：readAndDecryptFlowBody 是 agent 接入面 /flow/config 与
// /flow/upload 的唯一读体入口，修复前无上限 io.ReadAll；现与 decodeJSON/
// WS 同口径 MaxBytesReader 10MB。超限必须返回 error（两调用方整批拒收、
// flowUpload 返非 ok 且不消费 seq，agent 可同 seq 重报），上限内照常通过。

const flowBodyLimit = 10 << 20

func TestReadAndDecryptFlowBodyOverLimitReturnsError(t *testing.T) {
	body := bytes.Repeat([]byte("a"), flowBodyLimit+1)
	req := httptest.NewRequest(http.MethodPost, "/flow/upload", bytes.NewReader(body))
	got, err := readAndDecryptFlowBody(req.Body, f01TestSecret)
	if err == nil {
		t.Fatal("over-limit body must return error, got nil")
	}
	if got != "" {
		t.Fatalf("over-limit body must return empty payload, got %d bytes", len(got))
	}
}

func TestReadAndDecryptFlowBodyAtLimitAccepted(t *testing.T) {
	body := bytes.Repeat([]byte("a"), flowBodyLimit)
	req := httptest.NewRequest(http.MethodPost, "/flow/upload", bytes.NewReader(body))
	got, err := readAndDecryptFlowBody(req.Body, f01TestSecret)
	if err != nil {
		t.Fatalf("body at exactly the 10MB limit must be accepted, got err %v", err)
	}
	if len(got) != flowBodyLimit {
		t.Fatalf("at-limit body must round-trip in full: got %d bytes, want %d", len(got), flowBodyLimit)
	}
}

func TestReadAndDecryptFlowBodyNormalPlainAccepted(t *testing.T) {
	payload := `[{"N":"1_1_0","U":10,"D":20}]`
	req := httptest.NewRequest(http.MethodPost, "/flow/upload", strings.NewReader(payload))
	got, err := readAndDecryptFlowBody(req.Body, f01TestSecret)
	if err != nil {
		t.Fatalf("normal-size body must be accepted, got err %v", err)
	}
	if got != payload {
		t.Fatalf("normal-size plaintext body must return as-is: got %q", got)
	}
}

func TestFlowUploadOverLimitRejectedThenRetryWithSameSeqOk(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	seq := int64(21)

	// 超限上报：构造「内容合法、仅体积超限」的批次——合法 items 数组
	// 后缀 JSON 允许的空白填充至 10MB 以上。修复前该 body 会被整表读入
	// 并成功入账回 ok（红）；修复后读体阶段即 error 整批拒收。
	items := fmt.Sprintf(`[{"N":"%d_1_0","U":70,"D":50}]`, fwdID)
	over := append([]byte(items), bytes.Repeat([]byte(" "), flowBodyLimit)...)
	if len(over) <= flowBodyLimit {
		t.Fatalf("test body must exceed the limit: %d", len(over))
	}
	if got := f01PostFlowUpload(t, h, f01TestSecret, over, &seq); got == "ok" {
		t.Fatalf("over-limit upload must not return ok, got %q", got)
	}
	if _, ok := f01Seq(t, h, 1); ok {
		t.Fatal("over-limit upload must not consume seq")
	}
	var fwd model.Forward
	if err := h.repo.DB().First(&fwd, fwdID).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 0 || fwd.OutFlow != 0 {
		t.Fatalf("over-limit upload must not be accounted: got in=%d out=%d", fwd.InFlow, fwd.OutFlow)
	}

	// agent 不清零、同 seq 以正常大小重报：必须回 ok 且只入账一次。
	good := f01EncryptEnvelope(t, f01TestSecret, []byte(items))
	if got := f01PostFlowUpload(t, h, f01TestSecret, good, &seq); got != "ok" {
		t.Fatalf("normal-size retry with same seq must return ok, got %q", got)
	}
	if err := h.repo.DB().First(&fwd, fwdID).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 50 || fwd.OutFlow != 70 {
		t.Fatalf("retry must be accounted once: got in=%d out=%d, want in=50 out=70", fwd.InFlow, fwd.OutFlow)
	}
	if lastSeq, ok := f01Seq(t, h, 1); !ok || lastSeq != seq {
		t.Fatalf("seq must advance to %d after retry, got %d ok=%v", seq, lastSeq, ok)
	}
}
