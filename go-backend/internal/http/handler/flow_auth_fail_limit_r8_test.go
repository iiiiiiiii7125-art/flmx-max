package handler

import (
	"bytes"
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
// 计数、异 IP 互不影响。

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
