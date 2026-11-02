package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R8-060 回归：/flow/config 与 /flow/upload 此前无鉴权失败节流，未知
// secret 每次尝试直达查库，与 /system-info WS 面（失败计数限流）
// 口径不一致。修复后：只计未知 secret 失败、同 IP 达 60 次/分钟后
// 两端点均短路（不再查库、回包与未知 secret 同形），有效上报永不
// 计数、异 IP 互不影响。/flow/upload/v2 的同族补齐（A-T-01）由
// TestFlowUploadV2ThrottledAfterRepeatedUnknownSecret 成对钉住。

func TestFlowAuthFailLimiterThresholdAndWindow(t *testing.T) {
	l := newFlowAuthFailLimiter()
	now := time.Now()
	ip := "203.0.113.7"
	if l.blocked(ip, now) {
		t.Fatal("fresh IP must not be blocked")
	}
	for i := 0; i < flowAuthFailMaxPerWindow-1; i++ {
		l.recordFailure(ip, now)
	}
	if l.blocked(ip, now) {
		t.Fatalf("must not block below %d failures", flowAuthFailMaxPerWindow)
	}
	l.recordFailure(ip, now)
	if !l.blocked(ip, now) {
		t.Fatalf("must block at %d failures", flowAuthFailMaxPerWindow)
	}
	// 窗口过期后计数重置。
	if l.blocked(ip, now.Add(flowAuthFailWindow)) {
		t.Fatal("must not stay blocked after the window expires")
	}
	// 异 IP 独立计量。
	if l.blocked("203.0.113.8", now) {
		t.Fatal("failures of one IP must not block another IP")
	}
}

func r8FlowRequest(t *testing.T, h *Handler, path, secret, remoteAddr string, body []byte) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path+"?secret="+secret, bytes.NewReader(body))
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	if path == "/flow/upload" {
		h.flowUpload(rec, req)
	} else {
		h.flowConfig(rec, req)
	}
	return strings.TrimSpace(rec.Body.String())
}

func TestFlowUploadThrottledAfterRepeatedUnknownSecret(t *testing.T) {
	h := newF01Handler(t)
	seedF01Forward(t, h)
	body := []byte(`[{"N":"1_1_0","U":10,"D":20}]`)
	attacker := "203.0.113.7:40001"
	// 喷洒达上限：未知 secret 一律回 error（与修复前同形）。
	for i := 0; i < flowAuthFailMaxPerWindow; i++ {
		if got := r8FlowRequest(t, h, "/flow/upload", "no-such-secret", attacker, body); got != "error" {
			t.Fatalf("unknown secret attempt %d: want error, got %q", i, got)
		}
	}
	// 达上限后：同 IP 即使持有效 secret 也被短路（不再查库/入账）。
	if got := r8FlowRequest(t, h, "/flow/upload", f01TestSecret, attacker, body); got != "error" {
		t.Fatalf("throttled valid upload: want error, got %q", got)
	}
	// 异 IP 持有效 secret 不受影响，照常入账回 ok。
	if got := r8FlowRequest(t, h, "/flow/upload", f01TestSecret, "203.0.113.8:40001", body); got != "ok" {
		t.Fatalf("valid upload from another IP: want ok, got %q", got)
	}
}

func r8FlowV2Request(t *testing.T, h *Handler, secret, remoteAddr string, plain []byte) (int, string) {
	t.Helper()
	body := f01EncryptEnvelope(t, secret, plain)
	req := httptest.NewRequest(http.MethodPost, "/flow/upload/v2?secret="+secret, bytes.NewReader(body))
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.flowUploadV2(rec, req)
	return rec.Code, rec.Body.String()
}

// A-T-01 回归（与 TestFlowUploadThrottledAfterRepeatedUnknownSecret
// 成对）：/flow/upload/v2 上线时漏接 R8-060 限流——未知 secret 无界
// 查库且不喂共享限流器。修复后 v2 与 v1 同口径：未知 secret 记账、
// 达上限后同 IP 短路（回 401 非确认形态、valid secret 也不查库不
// 入账，agent 保留批次）、异 IP 不受影响、有效上报永不计数。
func TestFlowUploadV2ThrottledAfterRepeatedUnknownSecret(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	plain := v2Envelope(v2StreamA, 1, fmt.Sprintf(`[{"n":"%d_1_0","u":10,"d":20}]`, fwdID))
	attacker := "203.0.113.17:40001"
	// 喷洒达上限：未知 secret 一律回 401（与修复前同形）。
	for i := 0; i < flowAuthFailMaxPerWindow; i++ {
		if code, _ := r8FlowV2Request(t, h, "no-such-secret", attacker, plain); code != http.StatusUnauthorized {
			t.Fatalf("unknown secret attempt %d: want 401, got %d", i, code)
		}
	}
	// 达上限后：同 IP 即使持有效 secret 也被短路——回 401 非确认
	// 形态、不入账、游标不建。
	code, body := r8FlowV2Request(t, h, f01TestSecret, attacker, plain)
	if code != http.StatusUnauthorized {
		t.Fatalf("throttled valid v2 upload: want 401, got %d (%s)", code, body)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 0 || out != 0 {
		t.Fatalf("throttled v2 upload must not be accounted: in=%d out=%d", in, out)
	}
	if _, ok := v2Cursor(t, h, 1, v2StreamA); ok {
		t.Fatal("throttled v2 upload must not create a cursor")
	}
	// 异 IP 持有效 secret 不受影响，照常确认入账。
	if code, _ := r8FlowV2Request(t, h, f01TestSecret, "203.0.113.18:40001", plain); code != http.StatusOK {
		t.Fatalf("valid v2 upload from another IP: want 200, got %d", code)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 20 || out != 10 {
		t.Fatalf("valid v2 upload must be accounted: in=%d out=%d, want 20/10", in, out)
	}
	// v1 面攒下的失败与 v2 共用同一限流器：换个 IP 经 v1 喷洒达
	// 上限后，v2 同样被短路。
	shared := "203.0.113.19:40001"
	v1Body := []byte(`[{"N":"1_1_0","U":10,"D":20}]`)
	for i := 0; i < flowAuthFailMaxPerWindow; i++ {
		if got := r8FlowRequest(t, h, "/flow/upload", "no-such-secret", shared, v1Body); got != "error" {
			t.Fatalf("v1 unknown secret attempt %d: want error, got %q", i, got)
		}
	}
	if code, _ := r8FlowV2Request(t, h, f01TestSecret, shared, plain); code != http.StatusUnauthorized {
		t.Fatalf("v2 upload from v1-throttled IP: want 401, got %d", code)
	}
}

func TestFlowValidUploadsNeverCounted(t *testing.T) {
	h := newF01Handler(t)
	seedF01Forward(t, h)
	body := []byte(`[{"N":"1_1_0","U":10,"D":20}]`)
	addr := "203.0.113.9:40001"
	// 超过失败上限次数的有效上报：只计失败，故永不触发节流。
	for i := 0; i < flowAuthFailMaxPerWindow+5; i++ {
		if got := r8FlowRequest(t, h, "/flow/upload", f01TestSecret, addr, body); got != "ok" {
			t.Fatalf("valid upload %d: want ok, got %q", i, got)
		}
	}
}

func TestFlowConfigSharesLimiterAndKeepsOkShape(t *testing.T) {
	h := newF01Handler(t)
	addr := "203.0.113.10:40001"
	// 经 /flow/config 喷洒达上限：回包恒 "ok"（与未知 secret 同形）。
	for i := 0; i < flowAuthFailMaxPerWindow; i++ {
		if got := r8FlowRequest(t, h, "/flow/config", "no-such-secret", addr, nil); got != "ok" {
			t.Fatalf("flow config unknown secret attempt %d: want ok, got %q", i, got)
		}
	}
	// 两端点共用同一限流器：config 面攒下的失败同样短路 upload 面。
	body := []byte(`[{"N":"1_1_0","U":10,"D":20}]`)
	if got := r8FlowRequest(t, h, "/flow/upload", f01TestSecret, addr, body); got != "error" {
		t.Fatalf("upload from config-throttled IP: want error, got %q", got)
	}
}
