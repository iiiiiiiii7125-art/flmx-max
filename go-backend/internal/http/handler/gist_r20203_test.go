package handler

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
)

// R2-02-03 回归：gistDo 的「连接 GitHub 失败」包装此前以 %v 直包
// *url.Error，完整请求 URL（含 userinfo）随错误落盘/回显。修复后
// 经同包 redactFetchError（R2X-028 的脱敏面）包装：错误文案不得
// 含 URL 的 userinfo，面向运维的前缀保留。

func TestGistDoErrorRedactsUserinfoR20203(t *testing.T) {
	// 取一个必然拒绝连接的回环端口：先占后放。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	const marker = "r20203-secret-marker"
	raw := "https://gistuser:" + marker + "@" + addr + "/gists"
	err = gistRawRequest(context.Background(), http.MethodGet, raw, nil, nil)
	if err == nil {
		t.Fatal("expected a dial error for a closed loopback port")
	}
	msg := err.Error()
	if strings.Contains(msg, marker) {
		t.Fatalf("gist error leaked the URL password marker: %q", msg)
	}
	if strings.Contains(msg, "gistuser") {
		t.Fatalf("gist error leaked the URL userinfo: %q", msg)
	}
	if !strings.Contains(msg, "连接 GitHub 失败") {
		t.Fatalf("gist error lost its operator-facing prefix: %q", msg)
	}
}
