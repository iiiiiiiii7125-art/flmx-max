package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A01-001 回归：上报客户端不得跟随跨主机重定向。此前裸
// http.Client 默认跟随任意 307/308 并把加密上报体原样带到异机，
// 异机回的 "ok" 还被当作面板确认。

func TestReportDoRejectsCrossHostRedirect(t *testing.T) {
	// 异机 B：收到什么都回 ok，并记录是否被击中。
	var bHits int
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bHits++
		_, _ = io.WriteString(w, "ok")
	}))
	defer serverB.Close()

	// 假面板 A：对上报只回 307 到异机 B。
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", serverB.URL+"/steal")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer serverA.Close()

	req, err := http.NewRequest("POST", serverA.URL+"/flow/upload?secret=x", strings.NewReader(`{"encrypted":true,"data":"AA=="}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := reportDo(context.Background(), req, 5*time.Second)
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("跨主机 307 必须被拒绝，不得跟随")
	}
	if bHits != 0 {
		t.Fatalf("上报体被投递到重定向异机 %d 次", bHits)
	}
}

func TestReportDoAllowsSameHostRedirect(t *testing.T) {
	// 同主机跳转（面板前置代理 http→https 升级的同形态）放行。
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/flow/upload")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/flow/upload", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	req, err := http.NewRequest("POST", server.URL+"/old", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := reportDo(context.Background(), req, 5*time.Second)
	if err != nil {
		t.Fatalf("同主机重定向应放行: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("同主机重定向后应拿到面板 ok, got %d %q", resp.StatusCode, body)
	}
}

func TestReportRedirectPolicyRejectsDowngradeAndLoops(t *testing.T) {
	client := reportHTTPClient(5 * time.Second)
	if client.CheckRedirect == nil {
		t.Fatal("上报客户端必须设置 CheckRedirect")
	}

	// https -> http 同主机降级拒绝。
	httpsReq, _ := http.NewRequest("GET", "http://panel.example.com/flow/upload", nil)
	via := []*http.Request{{URL: mustParseURL(t, "https://panel.example.com/flow/upload")}}
	if err := client.CheckRedirect(httpsReq, via); err == nil {
		t.Fatal("https->http 降级重定向必须拒绝")
	}

	// 跨主机拒绝（即使同 scheme）。
	crossReq, _ := http.NewRequest("GET", "https://evil.example.com/steal", nil)
	if err := client.CheckRedirect(crossReq, via); err == nil {
		t.Fatal("跨主机重定向必须拒绝")
	}

	// 跳数上限。
	loopReq, _ := http.NewRequest("GET", "https://panel.example.com/flow/upload", nil)
	many := make([]*http.Request, 5)
	for i := range many {
		many[i] = &http.Request{URL: mustParseURL(t, "https://panel.example.com/flow/upload")}
	}
	if err := client.CheckRedirect(loopReq, many); err == nil {
		t.Fatal("重定向跳数超限必须拒绝")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
