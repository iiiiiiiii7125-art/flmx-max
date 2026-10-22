package handler

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// R2 批1（R2X-044）：notify 出站 pinned-dial。修复前 notify client 无
// DialContext：validateNotifyURL 与 client.Do 两次独立解析，rebinding
// 可把已校验的公网目标在发送时翻到元数据。本文件的解析桩依赖修复后
// 的 notifyLookupIP 接缝；行为断言：二次解析翻到元数据时发送必须
// 失败且目标服务零命中，正常公网（含重定向逐跳）两通道均可达且
// Host 头保留原主机名。

func stubNotifyLookup(t *testing.T, fn func(host string) ([]net.IP, error)) {
	t.Helper()
	orig := notifyLookupIP
	notifyLookupIP = fn
	t.Cleanup(func() { notifyLookupIP = orig })
}

func portOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
}

func TestR2B1NotifyPinnedDialBlocksRebindingToMetadata(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	port := portOf(t, srv)

	calls := 0
	stubNotifyLookup(t, func(host string) ([]net.IP, error) {
		calls++
		if calls == 1 {
			// 首次（校验时）解析为本机测试服务（非硬拒，校验通过）。
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		// 其后（拨号时）被 rebinding 翻到元数据。
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	})

	n := newNotifier()
	err := n.sendWebhook(context.Background(), "http://hook.test:"+port+"/wh", notifyMessage{Title: "t", Message: "m"})
	if err == nil {
		t.Fatal("rebinding to metadata at dial time must fail the send")
	}
	if hits.Load() != 0 {
		t.Fatalf("webhook server must receive nothing, got %d hits", hits.Load())
	}
	if !strings.Contains(err.Error(), "通知地址") && !strings.Contains(err.Error(), "元数据") {
		t.Fatalf("error should surface the hard-deny validation, got: %v", err)
	}
}

func TestR2B1NotifyPinnedDialWebhookAndTelegram(t *testing.T) {
	var hookHost, tgPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/bot") {
			tgPath = r.URL.Path
		} else {
			hookHost = r.Host
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	port := portOf(t, srv)
	stubNotifyLookup(t, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})

	n := newNotifier()
	msg := notifyMessage{Title: "t", Message: "m"}
	if err := n.sendWebhook(context.Background(), "http://hook.test:"+port+"/wh", msg); err != nil {
		t.Fatalf("webhook via pinned dial must succeed: %v", err)
	}
	if hookHost != "hook.test:"+port {
		t.Fatalf("Host header must preserve original host, got %q", hookHost)
	}
	s := notifySettings{TelegramToken: "tok123", TelegramChatID: "42", TelegramAPIBase: "http://tg.test:" + port}
	if err := n.sendTelegram(context.Background(), s, msg); err != nil {
		t.Fatalf("telegram via pinned dial must succeed: %v", err)
	}
	if tgPath != "/bottok123/sendMessage" {
		t.Fatalf("telegram path wrong: %q", tgPath)
	}
}

func TestR2B1NotifyRedirectRevalidatedPerHop(t *testing.T) {
	port := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://hook.test:"+port+"/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/evil", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://meta.test:80/hook", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	port = portOf(t, srv)
	stubNotifyLookup(t, func(host string) ([]net.IP, error) {
		if host == "meta.test" {
			return []net.IP{net.ParseIP("169.254.169.254")}, nil
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})
	n := newNotifier()
	msg := notifyMessage{Title: "t", Message: "m"}
	if err := n.sendWebhook(context.Background(), "http://hook.test:"+port+"/old", msg); err != nil {
		t.Fatalf("same-host redirect via pinned dial must succeed: %v", err)
	}
	if err := n.sendWebhook(context.Background(), "http://hook.test:"+port+"/evil", msg); err == nil {
		t.Fatal("redirect to metadata host must be blocked")
	}
}
