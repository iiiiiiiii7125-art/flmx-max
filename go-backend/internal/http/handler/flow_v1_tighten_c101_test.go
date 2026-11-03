package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/model"
)

// C1-01 钉格（1.9.5 发版后审计）：flow v1 收紧三件——
//  1. 加密信封 timestamp 新鲜度校验（修复前：只解析从不校验，
//     任意旧信封可原样重放，下列 stale 用例修复前回 ok＝红）；
//  2. 无 seq legacy 形态的落日（修复前无任何落日安排，落日后
//     用例修复前仍回 ok 入账＝红）；
//  3. v1 flowUpload/flowConfig 方法校验（修复前 GET 亦可达＝红）。
//
// 时钟偏差容忍实证：agent 每次发送现场封包（重试亦重新封包），
// ±10 分钟偏差的信封必须放行入账（下方 skew 用例），窗口 15 分钟
// 不误杀时钟偏差在此量级内的正常旧 agent。

func c101EnvelopeAt(t *testing.T, secret string, plain []byte, ts int64) []byte {
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
		"timestamp": ts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func c101ForwardFlow(t *testing.T, h *Handler, fwdID int64) (int64, int64) {
	t.Helper()
	var fwd model.Forward
	if err := h.repo.DB().First(&fwd, fwdID).Error; err != nil {
		t.Fatal(err)
	}
	return fwd.InFlow, fwd.OutFlow
}

func TestCheckFlowEnvelopeTimestampBoundaries(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name string
		ts   int64
		ok   bool
	}{
		{"absent timestamp stays legacy-compatible", 0, true},
		{"exact now", now.Unix(), true},
		{"10m old", now.Add(-10 * time.Minute).Unix(), true},
		{"10m future", now.Add(10 * time.Minute).Unix(), true},
		{"14m old inside window", now.Add(-14 * time.Minute).Unix(), true},
		{"16m old outside window", now.Add(-16 * time.Minute).Unix(), false},
		{"16m future outside window", now.Add(16 * time.Minute).Unix(), false},
		{"millis now normalized", now.UnixMilli(), true},
		{"millis stale rejected", now.Add(-time.Hour).UnixMilli(), false},
	}
	for _, tc := range cases {
		err := checkFlowEnvelopeTimestamp(tc.ts, now)
		if tc.ok && err != nil {
			t.Fatalf("%s: want accept, got %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: want reject, got accept", tc.name)
		}
	}
}

func TestFlowUploadStaleEnvelopeRejectedFreshAccepted(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	items := fmt.Sprintf(`[{"N":"%d_1_0","U":70,"D":50}]`, fwdID)

	// 一小时前的信封原样重放：必须拒收（非 ok）且不入账。
	stale := c101EnvelopeAt(t, f01TestSecret, []byte(items), time.Now().Add(-time.Hour).Unix())
	if got := f01PostFlowUpload(t, h, f01TestSecret, stale, nil); got == "ok" {
		t.Fatalf("stale envelope must not return ok, got %q", got)
	}
	if in, out := c101ForwardFlow(t, h, fwdID); in != 0 || out != 0 {
		t.Fatalf("stale envelope must not be accounted: in=%d out=%d", in, out)
	}

	// 同批次以新鲜信封重报：入账（被拒批次 agent 保留、以新封包
	// 重报即自愈的实证）。
	fresh := c101EnvelopeAt(t, f01TestSecret, []byte(items), time.Now().Unix())
	if got := f01PostFlowUpload(t, h, f01TestSecret, fresh, nil); got != "ok" {
		t.Fatalf("fresh envelope must return ok, got %q", got)
	}
	if in, out := c101ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
		t.Fatalf("fresh envelope must be accounted: in=%d out=%d", in, out)
	}
}

func TestFlowUploadClockSkewWithinWindowAccepted(t *testing.T) {
	for _, skew := range []time.Duration{-10 * time.Minute, 10 * time.Minute} {
		h := newF01Handler(t)
		fwdID := seedF01Forward(t, h)
		items := fmt.Sprintf(`[{"N":"%d_1_0","U":70,"D":50}]`, fwdID)
		body := c101EnvelopeAt(t, f01TestSecret, []byte(items), time.Now().Add(skew).Unix())
		if got := f01PostFlowUpload(t, h, f01TestSecret, body, nil); got != "ok" {
			t.Fatalf("skew %v: envelope inside freshness window must return ok, got %q", skew, got)
		}
		if in, out := c101ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
			t.Fatalf("skew %v: batch must be accounted: in=%d out=%d", skew, in, out)
		}
	}
}

func TestFlowUploadLegacySunset(t *testing.T) {
	legacyItems := func(fwdID int64) []byte {
		return []byte(fmt.Sprintf(`[{"N":"%d_1_0","U":70,"D":50}]`, fwdID))
	}

	// 落日前：无 seq legacy 照常入账，回执点名弃用。
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	req := httptest.NewRequest(http.MethodPost, "/flow/upload?secret="+f01TestSecret, bytes.NewReader(legacyItems(fwdID)))
	rec := httptest.NewRecorder()
	h.flowUpload(rec, req)
	if strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("legacy batch before sunset must return ok, got %q", rec.Body.String())
	}
	if rec.Header().Get("Sunset") != flowV1LegacySunsetHTTPDate {
		t.Fatalf("legacy receipt before sunset must carry the Sunset header, got %q", rec.Header().Get("Sunset"))
	}
	if in, out := c101ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
		t.Fatalf("legacy batch before sunset must be accounted: in=%d out=%d", in, out)
	}

	// 落日后（注入缝把落日拨到过去）：无 seq 整批拒收、不入账；
	// 带 seq 的同一节点上报不受落日影响。
	old := flowV1LegacySunset
	flowV1LegacySunset = time.Now().Add(-time.Hour)
	defer func() { flowV1LegacySunset = old }()

	h2 := newF01Handler(t)
	fwdID2 := seedF01Forward(t, h2)
	if got := f01PostFlowUpload(t, h2, f01TestSecret, legacyItems(fwdID2), nil); got == "ok" {
		t.Fatalf("legacy batch after sunset must not return ok, got %q", got)
	}
	if in, out := c101ForwardFlow(t, h2, fwdID2); in != 0 || out != 0 {
		t.Fatalf("legacy batch after sunset must not be accounted: in=%d out=%d", in, out)
	}
	seq := int64(3)
	if got := f01PostFlowUpload(t, h2, f01TestSecret, legacyItems(fwdID2), &seq); got != "ok" {
		t.Fatalf("seq-carrying batch must stay accepted after sunset, got %q", got)
	}
	if in, out := c101ForwardFlow(t, h2, fwdID2); in != 50 || out != 70 {
		t.Fatalf("seq-carrying batch after sunset must be accounted: in=%d out=%d", in, out)
	}
}

func TestFlowV1EndpointsRequirePost(t *testing.T) {
	h := newF01Handler(t)
	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		path string
	}{
		{"flowUpload", h.flowUpload, "/flow/upload?secret=" + f01TestSecret},
		{"flowConfig", h.flowConfig, "/flow/config?secret=" + f01TestSecret},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		tc.fn(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: GET must be rejected 405, got %d", tc.name, rec.Code)
		}
	}
}
