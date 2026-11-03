package handler

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

// F2-F1-01 钉格：转发目标段校验须先按消费侧归一（剥 scheme/路径）
// 再 SplitHostPort，scheme 形态 URL 目标与无 scheme 同语义同结果，
// 且归一不得绕过 netguard。

func TestFixACheckSingleNormalizesSchemeTargets(t *testing.T) {
	origLookup := safeRemoteLookupIP
	t.Cleanup(func() { safeRemoteLookupIP = origLookup })
	safeRemoteLookupIP = func(host string) ([]net.IP, error) {
		switch host {
		case "example.com":
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		case "internal.example":
			return []net.IP{net.ParseIP("192.168.0.8")}, nil
		}
		return nil, fmt.Errorf("no such host: %s", host)
	}

	// scheme 形态与无 scheme 同语义：均应通过段校验（解析到公网）。
	for _, addr := range []string{
		"example.com:8080",
		"example.com:8080/path",
		"http://example.com:8080",
		"http://example.com:8080/path",
		"https://example.com:8443/a/b?x=1",
		"http://example.com/path",
	} {
		if err := checkSingleRemoteAddr(addr); err != nil {
			t.Fatalf("target %q must pass segment validation: %v", addr, err)
		}
	}
	// 归一不得绕过 netguard：scheme 形态解析到内网同样拒绝。
	if err := checkSingleRemoteAddr("http://internal.example:8080/x"); err == nil ||
		!strings.Contains(err.Error(), "internal IP") {
		t.Fatalf("scheme target resolving to internal IP must be rejected, got %v", err)
	}
	// 解析失败文案仍点名用户提交的原始段。
	if err := checkSingleRemoteAddr("http://missing.example:8080/"); err == nil ||
		!strings.Contains(err.Error(), "could not resolve address \"http://missing.example:8080/\"") {
		t.Fatalf("unresolvable scheme target must name the submitted segment, got %v", err)
	}
}

