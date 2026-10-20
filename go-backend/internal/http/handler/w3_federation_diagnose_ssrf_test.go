package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// W3 (发版前阻塞轮 / N-09 补)：F-086 只拦了硬保留段（回环/链路本地/
// 未指定/组播/0-0-0-0/8），RFC1918 私网与主机名仍放行——持 peer token
// 的对端可借提供方节点探测内网（SSRF 侧信道）。本批口径：诊断目标
// 判定复用 isPrivateIP（与 SecureFetch 的 validateFetchIP 同一份判定，
// kernel_external.go），RFC1918/ULA/CGNAT 一并拒绝；主机名解析后逐
// 地址判定、任一不合法整体拒绝（与 validateFetchURL 防 rebinding
// 口径一致）。
func TestValidateFederationDiagnoseTargetW3(t *testing.T) {
	// 字面 IP：内网/保留段一律拒绝。
	for _, ip := range []string{
		"10.0.0.1", "192.168.1.1", "172.16.0.1", // RFC1918
		"127.0.0.1",        // 回环
		"169.254.169.254",  // 链路本地/云元数据
		"fc00::1",          // ULA
		"::ffff:10.0.0.1",  // IPv4-mapped 内网
		"::ffff:192.168.0.1",
		"0.1.2.3",   // 0.0.0.0/8（F-023 回归）
		"fe80::1",   // 链路本地 v6
		"::1",       // 回环 v6
		"100.64.0.1", // CGNAT（isPrivateIP 口径内）
	} {
		if _, err := validateFederationDiagnoseTarget(ip, 4); err == nil {
			t.Fatalf("target %s must be rejected as private/reserved", ip)
		}
	}

	// 字面公网 IP 放行，且 count 钳制不变。
	if c, err := validateFederationDiagnoseTarget("8.8.8.8", 100000); err != nil || c != maxFederationDiagnoseCount {
		t.Fatalf("public target must pass with clamped count, got %d, err=%v", c, err)
	}

	// 主机名解析到回环 → 拒绝（localhost 必然解析到 127.0.0.1/::1）。
	if _, err := validateFederationDiagnoseTarget("localhost", 4); err == nil {
		t.Fatalf("hostname resolving to loopback must be rejected")
	}
}

// 主机名判定走注入的解析器：任一返回地址不合法整体拒绝（防
// rebinding 的混合记录），纯公网放行，不可解析 fail-closed。
func TestValidateFederationDiagnoseTargetHostnameW3(t *testing.T) {
	orig := federationDiagnoseLookupIP
	defer func() { federationDiagnoseLookupIP = orig }()

	federationDiagnoseLookupIP = func(host string) ([]net.IP, error) {
		switch host {
		case "internal.example.com":
			return []net.IP{net.ParseIP("10.0.0.1")}, nil
		case "mixed.example.com":
			return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("192.168.1.1")}, nil
		case "public.example.com":
			return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("2001:4860:4860::8888")}, nil
		default:
			return nil, errors.New("no such host")
		}
	}

	if _, err := validateFederationDiagnoseTarget("internal.example.com", 4); err == nil {
		t.Fatalf("hostname resolving to RFC1918 must be rejected")
	}
	if _, err := validateFederationDiagnoseTarget("mixed.example.com", 4); err == nil {
		t.Fatalf("hostname with any private resolution must be rejected")
	}
	if c, err := validateFederationDiagnoseTarget("public.example.com", 4); err != nil || c != 4 {
		t.Fatalf("hostname resolving only to public IPs must pass, got %d, err=%v", c, err)
	}
	if _, err := validateFederationDiagnoseTarget("missing.example.com", 4); err == nil {
		t.Fatalf("unresolvable hostname must be rejected (fail-closed)")
	}
}

func TestFederationRuntimeDiagnoseSSRFW3(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "w3-diag.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, "unit"+"-test-secret")
	now := time.Now().UnixMilli()
	if err := r.CreatePeerShare(&repo.PeerShare{
		Name: "w3-share", NodeID: 9, Token: "w3-token",
		PortRangeStart: 22000, PortRangeEnd: 22010, IsActive: 1,
		CreatedTime: now, UpdatedTime: now,
	}); err != nil {
		t.Fatal(err)
	}

	call := func(path, body string) response.R {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer w3-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if strings.HasSuffix(path, "/diagnose") {
			h.federationRuntimeDiagnose(rec, req)
		} else {
			h.federationRuntimeCommand(rec, req)
		}
		var out response.R
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// diagnose 入口：RFC1918 必须在校验层以保留地址拒绝。
	for _, ip := range []string{"10.0.0.1", "192.168.1.1", "172.16.0.1"} {
		out := call("/api/v1/federation/runtime/diagnose",
			fmt.Sprintf(`{"ip":%q,"port":80,"count":4}`, ip))
		if out.Code == 0 || !strings.Contains(out.Msg, "保留地址") {
			t.Fatalf("diagnose target %s must be rejected as reserved, got %+v", ip, out)
		}
	}

	// runtime/command 的 TcpPing 变体同口径拒绝 RFC1918。
	out := call("/api/v1/federation/runtime/command",
		`{"commandType":"TcpPing","data":{"ip":"192.168.1.1","port":80,"count":4}}`)
	if out.Code == 0 || !strings.Contains(out.Msg, "保留地址") {
		t.Fatalf("runtime command tcpping to RFC1918 must be rejected, got %+v", out)
	}

	// 公网目标过校验层，失败只能发生在节点命令层。
	out = call("/api/v1/federation/runtime/diagnose",
		`{"ip":"8.8.8.8","port":53,"count":4}`)
	if strings.Contains(out.Msg, "保留地址") || strings.Contains(out.Msg, "Invalid target") {
		t.Fatalf("public target must pass validation, got %+v", out)
	}
}
