package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/http/response"
	runtimenft "go-backend/internal/runtime/nftables"
)

// PORT-CLOSURE 集成扩展：隧道显式 in_ip 双端口收口（F-07 扩展）。
//
// 根因同族：tunnelCreate（:1443）与 tunnelUpdate（:2079-2080）的显式
// inIp 只经单次剥离 stripShareHostPortList，零校验——双端口值
// "198.51.100.77:8443:22001" 是 SplitHostPort 的 no-op，原值落库后
// 消费侧（gost/realm/nft 入口）畸形。与 PC-04（forward inIp）与
// PC-05（监听地址）同机制、同修法：intake 剥离 + 格式校验，双端口
// 直接拒绝、不落库；单端口剥离口径不变；存量不改写。
//
// 探针：HTTP 级（复用 R31 F-01 脚手架 setupR31FixF01；manager 用
// 直通 fake——Test 立即返回 nil，不用 runWithBlockedNftTest，因为
// 拒绝格根本走不到 capability Test，而该 helper 在 Test 未被调用
// 时 Fatal）。红：修复前双端口被接受、原值落库；绿：拒绝 + 无行
// 落库 + 单端口对照仍剥离落库。

// pcIntNftPassthrough is a non-blocking fake nftables manager: Test
// returns nil immediately (unlike blockingNftablesManager, which waits
// for testRelease); Reconcile mirrors the blocking fake's ApplyResult
// so the create/update flows behave exactly as in the R31 fixture.
type pcIntNftPassthrough struct{}

func (pcIntNftPassthrough) Test(_ context.Context, _ runtimenft.SSHConfig) error {
	return nil
}

func (pcIntNftPassthrough) Reconcile(_ context.Context, _ runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error) {
	return runtimenft.ApplyResult{NodeID: plan.NodeID, Script: "table inet flvx {}", Hashes: runtimenft.PlanHashes(plan)}, nil
}

func (pcIntNftPassthrough) Clear(_ context.Context, _ runtimenft.SSHConfig) error {
	return nil
}

func (pcIntNftPassthrough) CollectCounters(_ context.Context, _ runtimenft.SSHConfig) ([]runtimenft.CounterSample, error) {
	return nil, nil
}

func pcIntSetup(t *testing.T) *r31FixF01Fixture {
	t.Helper()
	fx := setupR31FixF01(t)
	fx.h.nftablesManager = newSerializedNftManager(pcIntNftPassthrough{})
	return fx
}

func pcIntDecode(t *testing.T, res *httptest.ResponseRecorder) response.R {
	t.Helper()
	var out response.R
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	return out
}

func pcIntStoredInIPByName(t *testing.T, fx *r31FixF01Fixture, name string) string {
	t.Helper()
	var stored string
	if err := fx.h.repo.DB().Raw(`SELECT COALESCE(in_ip, '') FROM tunnel WHERE name = ?`, name).Scan(&stored).Error; err != nil {
		t.Fatalf("query tunnel.in_ip: %v", err)
	}
	return stored
}

func pcIntCreateAttempt(t *testing.T, fx *r31FixF01Fixture, name, inIP string) response.R {
	t.Helper()
	body := bytes.NewReader([]byte(`{
		"name":"` + name + `",
		"type":1,
		"flow":1,
		"trafficRatio":1,
		"status":1,
		"inIp":"` + inIP + `",
		"inNodeId":[{"nodeId":` + int64JSON(fx.nodeID) + `,"protocol":"tls"}]
	}`))
	res := httptest.NewRecorder()
	fx.h.tunnelCreate(res, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/create", body))
	return pcIntDecode(t, res)
}

func pcIntUpdateAttempt(t *testing.T, fx *r31FixF01Fixture, tunnelID int64, name, inIP string) response.R {
	t.Helper()
	body := bytes.NewReader([]byte(`{
		"id":` + int64JSON(tunnelID) + `,
		"name":"` + name + `",
		"type":1,
		"flow":1,
		"trafficRatio":1,
		"status":1,
		"inIp":"` + inIP + `",
		"inNodeId":[{"nodeId":` + int64JSON(fx.nodeID) + `,"protocol":"tls"}]
	}`))
	res := httptest.NewRecorder()
	fx.h.tunnelUpdate(res, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/update", body))
	return pcIntDecode(t, res)
}

func pcIntTunnelCount(t *testing.T, fx *r31FixF01Fixture, name string) int {
	t.Helper()
	var n int
	if err := fx.h.repo.DB().Raw(`SELECT COUNT(*) FROM tunnel WHERE name = ?`, name).Scan(&n).Error; err != nil {
		t.Fatalf("count tunnel %q: %v", name, err)
	}
	return n
}

// TestPortClosureIntTunnelCreateRejectsDoublePortInIP — F-07 扩展 (P2)：
// tunnelCreate 显式 inIp 双端口须拒绝、不落库。
func TestPortClosureIntTunnelCreateRejectsDoublePortInIP(t *testing.T) {
	fx := pcIntSetup(t)
	out := pcIntCreateAttempt(t, fx, "pc-int-t1", "198.51.100.77:8443:22001")
	if out.Code == 0 {
		t.Errorf("CELL-T1 tunnelCreate accepted double-port inIp (code 0); want rejection")
	}
	if n := pcIntTunnelCount(t, fx, "pc-int-t1"); n != 0 {
		t.Errorf("CELL-T1 double-port tunnel row created (count=%d); want 0 (rejected before insert)", n)
	}
	// 多值串中一项双端口：整串拒绝。
	out = pcIntCreateAttempt(t, fx, "pc-int-t1m", "198.51.100.77:8443,203.0.113.9:9999:22001")
	if out.Code == 0 || pcIntTunnelCount(t, fx, "pc-int-t1m") != 0 {
		t.Errorf("CELL-T1M multi-value double-port inIp accepted (code=%d); want rejection", out.Code)
	}
	// 括号 IPv6 双端口同格。
	out = pcIntCreateAttempt(t, fx, "pc-int-t1v6", "[2001:db8::1]:8443:22001")
	if out.Code == 0 || pcIntTunnelCount(t, fx, "pc-int-t1v6") != 0 {
		t.Errorf("CELL-T1V6 bracketed-v6 double-port inIp accepted (code=%d); want rejection", out.Code)
	}
}

// TestPortClosureIntTunnelCreateKeepsSinglePortStrip — 对照：单端口
// inIp 仍接受、剥离后落库（R31 F-01 口径不变）。
func TestPortClosureIntTunnelCreateKeepsSinglePortStrip(t *testing.T) {
	fx := pcIntSetup(t)
	out := pcIntCreateAttempt(t, fx, "pc-int-c1", "198.51.100.77:8443")
	if out.Code != 0 {
		t.Fatalf("control tunnelCreate single-port inIp rejected (code=%d msg=%q); want accept", out.Code, out.Msg)
	}
	if got := pcIntStoredInIPByName(t, fx, "pc-int-c1"); got != "198.51.100.77" {
		t.Errorf("control tunnel.in_ip = %q, want %q", got, "198.51.100.77")
	}
	out = pcIntCreateAttempt(t, fx, "pc-int-c1m", "198.51.100.77:8443,2001:db8::1")
	if out.Code != 0 {
		t.Fatalf("control tunnelCreate multi-value single-port inIp rejected (code=%d); want accept", out.Code)
	}
	if got := pcIntStoredInIPByName(t, fx, "pc-int-c1m"); got != "198.51.100.77,2001:db8::1" {
		t.Errorf("control tunnel.in_ip = %q, want %q", got, "198.51.100.77,2001:db8::1")
	}
}

// TestPortClosureIntTunnelUpdateRejectsDoublePortInIP — F-07 扩展 (P2)：
// tunnelUpdate 显式 inIp 双端口须拒绝、存量 in_ip 不变（种子隧道
// in_ip 为 NULL，即 COALESCE 后 ""）。
func TestPortClosureIntTunnelUpdateRejectsDoublePortInIP(t *testing.T) {
	fx := pcIntSetup(t)
	tunnelID := seedTunnelForNftables(t, fx.h, "pc-int-t2", fx.nodeID)
	out := pcIntUpdateAttempt(t, fx, tunnelID, "pc-int-t2", "198.51.100.77:8443:22001")
	if out.Code == 0 {
		t.Errorf("CELL-T2 tunnelUpdate accepted double-port inIp (code 0); want rejection")
	}
	if got := fx.storedTunnelInIP(t, tunnelID); got != "" {
		t.Errorf("CELL-T2 tunnel.in_ip changed to %q after rejected update; want %q (unchanged)", got, "")
	}
}

// TestPortClosureIntStripAndValidateNodeAddressList — helper 单元格：
// 剥离 + 逐项校验的真值表（双端口拒、单端口剥离、裸值/裸 IPv6 原样、
// 空串与空项保留形态）。
func TestPortClosureIntStripAndValidateNodeAddressList(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"198.51.100.77", "198.51.100.77", false},
		{"198.51.100.77:8443", "198.51.100.77", false},
		{"198.51.100.77:8443:22001", "", true},
		{"198.51.100.77:8443,203.0.113.9:9999", "198.51.100.77,203.0.113.9", false},
		{"198.51.100.77:8443,203.0.113.9:9999:22001", "", true},
		{"2001:db8::1", "2001:db8::1", false},
		{"[2001:db8::1]:8443", "2001:db8::1", false},
		{"[2001:db8::1]:8443:22001", "", true},
		{"example.com", "example.com", false},
		{"example.com:8443", "example.com", false},
		{"198.51.100.77,", "198.51.100.77,", false},
	}
	for _, c := range cases {
		got, err := stripAndValidateNodeAddressList(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("stripAndValidateNodeAddressList(%q) = %q, nil error; want rejection", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("stripAndValidateNodeAddressList(%q) error %v; want %q", c.in, err, c.want)
			continue
		}
		if got != c.want {
			t.Errorf("stripAndValidateNodeAddressList(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPortClosureIntTunnelUpdateKeepsSinglePortStrip — 对照：单端口
// inIp 更新仍接受、剥离后落库。
func TestPortClosureIntTunnelUpdateKeepsSinglePortStrip(t *testing.T) {
	fx := pcIntSetup(t)
	tunnelID := seedTunnelForNftables(t, fx.h, "pc-int-c2", fx.nodeID)
	out := pcIntUpdateAttempt(t, fx, tunnelID, "pc-int-c2", "198.51.100.77:8443")
	if out.Code != 0 {
		t.Fatalf("control tunnelUpdate single-port inIp rejected (code=%d msg=%q); want accept", out.Code, out.Msg)
	}
	if got := fx.storedTunnelInIP(t, tunnelID); got != "198.51.100.77" {
		t.Errorf("control tunnel.in_ip = %q, want %q", got, "198.51.100.77")
	}
}
