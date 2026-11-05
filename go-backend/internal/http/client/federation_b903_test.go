package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// B9-03：联邦 client 七个方法的 200 成功体此前无上限解码（同文件错误
// 体已有 federationErrorBodyLimit=512 的 LB-01 口径）——被攻陷/异常
// 对端可用超大成功体线性放大消费方内存，Diagnose 的 map 解码尤甚。
// 本文件钉修复：成功体经 federationSuccessBodyLimit 截断后解码必须
// 报错（按远端协议错误处理），七个方法无一例外；正常大小的成功体
// 保持可用（transport 测试的 connectOKServer 正向用例同包覆盖）。

func b903OversizedOKServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// msg 是七个方法响应结构体的公共字段，填充至上限之外可保证
		// 每个方法的 Decode 都必须读过截断点。
		_, _ = w.Write([]byte(`{"code":0,"msg":"` + strings.Repeat("A", federationSuccessBodyLimit+64*1024) + `","data":{"shareId":7}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestB903FederationSuccessBodyCapped(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := b903OversizedOKServer(t)
	c := NewFederationClient()
	calls := lb01Calls(srv.URL)
	if len(calls) != 7 {
		t.Fatalf("expected 7 federation methods under test, got %d", len(calls))
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(c); err == nil {
				t.Fatalf("oversized 200 body (>%d bytes) was decoded without error", federationSuccessBodyLimit)
			}
		})
	}
}
