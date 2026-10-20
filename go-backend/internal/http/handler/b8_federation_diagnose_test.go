package handler

import (
	"bytes"
	"encoding/json"
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

// F-086 (audit FINAL §5-B8 / A7-013，疑似→实证): federationRuntimeDiagnose
// 对持 peer token 的对端只做 IP 非空/端口范围的形式校验——目标 IP 不设
// 限（回环/链路本地/云元数据 169.254.169.254 等均可），Count 无上限
// （<=0 才置 4），结果（连通性/延迟）直接回给对端，构成对提供方内网
// 的低带宽探测侧信道 + 以节点为跳板的 ping 放大面。runtime/command
// 的 TcpPing 分支同型（req.Data 直通、零校验）。
// 实证（修复前）：目标 169.254.169.254 不被拒绝、直达节点命令层
// （以"节点不在线"类错误收尾）。修复后应在校验层以保留地址拦截。
// 口径：只拦硬保留段（回环/链路本地/未指定/组播 + 0/8，与 F-023 修后
// SSRF 段对齐）；RFC1918 不拦——链路中间跳目标可能为内网地址，是否
// 全拒属产品决策（FINAL §十一方向），不在本批。Count 钳制上限 10
// （合法调用方恒传 4）。主机名目标面板侧无法预解析校验，原样放行
// 并在报告登记为残余。
//
// W3/N-09（发版前阻塞轮）口径变更：上面「RFC1918 不拦、主机名放行」
// 两点已被取代——判定改复用 isPrivateIP 全量拒绝内网/保留段（含
// RFC1918），主机名解析后逐地址判定。本文件相应断言已同步更新，
// 新增覆盖见 w3_federation_diagnose_ssrf_test.go。
func TestFederationRuntimeDiagnoseTargetValidationF086(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "b8-diag.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, "unit"+"-test-secret")
	now := time.Now().UnixMilli()
	if err := r.CreatePeerShare(&repo.PeerShare{
		Name: "f086-share", NodeID: 9, Token: "f086-token",
		PortRangeStart: 22000, PortRangeEnd: 22010, IsActive: 1,
		CreatedTime: now, UpdatedTime: now,
	}); err != nil {
		t.Fatal(err)
	}

	call := func(path, body string) response.R {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer f086-token")
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

	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "0.1.2.3", "::1", "fe80::1", "10.0.0.5"} {
		out := call("/api/v1/federation/runtime/diagnose",
			fmt.Sprintf(`{"ip":%q,"port":80,"count":4}`, ip))
		if out.Code == 0 || !strings.Contains(out.Msg, "保留地址") {
			t.Fatalf("diagnose target %s must be rejected as reserved, got %+v", ip, out)
		}
	}

	// runtime/command 的 TcpPing 变体同口径拦截。
	out := call("/api/v1/federation/runtime/command",
		`{"commandType":"TcpPing","data":{"ip":"169.254.169.254","port":80,"count":100000}}`)
	if out.Code == 0 || !strings.Contains(out.Msg, "保留地址") {
		t.Fatalf("runtime command tcpping to metadata address must be rejected, got %+v", out)
	}

	// 公网目标应过校验层，失败只能发生在节点命令层（本测试无在线
	// 节点）。RFC1918 自 W3/N-09 起在校验层拒绝（见上方拒绝表）。
	for _, ip := range []string{"203.0.113.5"} {
		out := call("/api/v1/federation/runtime/diagnose",
			fmt.Sprintf(`{"ip":%q,"port":80,"count":4}`, ip))
		if strings.Contains(out.Msg, "保留地址") || strings.Contains(out.Msg, "Invalid target") {
			t.Fatalf("target %s must pass validation, got %+v", ip, out)
		}
	}
}

func TestValidateFederationDiagnoseTargetF086(t *testing.T) {
	if c, err := validateFederationDiagnoseTarget("203.0.113.5", 100000); err != nil || c != maxFederationDiagnoseCount {
		t.Fatalf("count must clamp to %d, got %d, err=%v", maxFederationDiagnoseCount, c, err)
	}
	if c, err := validateFederationDiagnoseTarget("203.0.113.5", 0); err != nil || c != 4 {
		t.Fatalf("count<=0 must default to 4, got %d, err=%v", c, err)
	}
	// W3/N-09：主机名解析后逐地址判定（注入公网解析结果保持测试
	// 密封）；RFC1918 改为拒绝。
	origLookup := federationDiagnoseLookupIP
	federationDiagnoseLookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	defer func() { federationDiagnoseLookupIP = origLookup }()
	if c, err := validateFederationDiagnoseTarget("example.com", 4); err != nil || c != 4 {
		t.Fatalf("hostname resolving to public IP must pass, got %d, err=%v", c, err)
	}
	if _, err := validateFederationDiagnoseTarget("192.168.1.10", 4); err == nil {
		t.Fatalf("RFC1918 must be rejected since W3/N-09")
	}
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "0.0.0.0", "0.9.9.9", "224.0.0.1", "::1", "fe80::5"} {
		if _, err := validateFederationDiagnoseTarget(ip, 4); err == nil {
			t.Fatalf("reserved target %s must be rejected", ip)
		}
	}
}
