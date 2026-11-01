package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// LB-01：联邦出站 client 七个方法在对端返回非 200 时曾以 io.ReadAll(resp.Body)
// 无界读错误体拼错误信息——异常/恶意对端可用超大错误体线性放大本机内存。
// 本文件以行为测试锁定修复：错误体经 federationErrorBodyLimit 截断后，
// 错误信息仍含状态码与体首内容（可用性不丢），体尾内容不得进入错误串。

const lb01TailMarker = "TAIL-MARKER-AFTER-LIMIT"

// lb01ErrorServer 返回指定状态码，响应体 = fill 字符重复至上限 + 尾标记 + 大段填充。
func lb01ErrorServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(strings.Repeat("A", federationErrorBodyLimit) + lb01TailMarker + strings.Repeat("B", 64*1024)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func lb01Calls(srvURL string) map[string]func(c *FederationClient) error {
	return map[string]func(c *FederationClient) error{
		"Connect": func(c *FederationClient) error {
			_, err := c.Connect(srvURL, "tok", "")
			return err
		},
		"CreateTunnel": func(c *FederationClient) error {
			_, err := c.CreateTunnel(srvURL, "tok", "", "tcp", 12345, "127.0.0.1:80")
			return err
		},
		"ReservePort": func(c *FederationClient) error {
			_, err := c.ReservePort(srvURL, "tok", "", RuntimeReservePortRequest{ResourceKey: "k", Protocol: "tcp"})
			return err
		},
		"ApplyRole": func(c *FederationClient) error {
			_, err := c.ApplyRole(srvURL, "tok", "", RuntimeApplyRoleRequest{ResourceKey: "k", Role: "entry", Protocol: "tcp"})
			return err
		},
		"ReleaseRole": func(c *FederationClient) error {
			return c.ReleaseRole(srvURL, "tok", "", RuntimeReleaseRoleRequest{ResourceKey: "k"})
		},
		"Diagnose": func(c *FederationClient) error {
			_, err := c.Diagnose(srvURL, "tok", "", RuntimeDiagnoseRequest{IP: "127.0.0.1", Port: 80})
			return err
		},
		"Command": func(c *FederationClient) error {
			_, err := c.Command(srvURL, "tok", "", RuntimeNodeCommandRequest{CommandType: "ping"})
			return err
		},
	}
}

func TestLB01FederationErrorBodyTruncated(t *testing.T) {
	// 与 federation_fix2b_test 同口径：放行环回供 httptest 对端使用。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	srv := lb01ErrorServer(t, http.StatusInternalServerError)
	c := NewFederationClient()
	calls := lb01Calls(srv.URL)
	if len(calls) != 7 {
		t.Fatalf("expected 7 federation methods under test, got %d", len(calls))
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call(c)
			if err == nil {
				t.Fatal("expected error for non-200 response, got nil")
			}
			msg := err.Error()
			if !strings.Contains(msg, "remote error 500: ") {
				t.Fatalf("error missing status prefix: %q", msg)
			}
			if !strings.Contains(msg, strings.Repeat("A", federationErrorBodyLimit)) {
				t.Fatalf("error should retain the first %d body bytes, got %d chars total", federationErrorBodyLimit, len(msg))
			}
			if strings.Contains(msg, lb01TailMarker) {
				t.Fatalf("error body was not truncated at %d bytes: tail marker leaked into error", federationErrorBodyLimit)
			}
			if strings.Contains(msg, "B") {
				t.Fatalf("error contains bytes beyond the %d-byte limit", federationErrorBodyLimit)
			}
		})
	}
}

// 短错误体（小于上限）必须完整进入错误串，截断不得误伤正常错误信息。
func TestLB01FederationShortErrorBodyIntact(t *testing.T) {
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	const shortBody = `{"code":1,"msg":"peer boom"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(shortBody))
	}))
	t.Cleanup(srv.Close)

	c := NewFederationClient()
	_, err := c.Connect(srv.URL, "tok", "")
	if err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
	if !strings.Contains(err.Error(), "remote error 400: "+shortBody) {
		t.Fatalf("short error body should pass through intact, got: %q", err.Error())
	}
}
