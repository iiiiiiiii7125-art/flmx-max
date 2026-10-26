package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// R31-L1-E01 / R31-FIX-F01（产品 P2）：隧道显式 inIp intake 绕过
// buildTunnelInIP，含 :port 的值原值落库 tunnel.in_ip ——
// tunnelCreate（mutations.go:1309 取值 → :1355/:1462 两段复用点，
// 仅 TrimSpace）与 tunnelUpdate（:1944 A9-05 覆盖分支，显式值直接
// 覆盖 buildTunnelInIP 的已剥离结果）双面。前端隧道表单 inIp 为
// 自由文本域且 validateTunnelForm 对其零校验，UI 直填即可命中。
//
// 探针按 L1 报告 §四逐格重建（脚手架复用 A301 模式：
// setupNftablesHandler + seedNftablesSSHConfig + 预放行的阻塞型
// fake nftables manager）：CELL-T1 create 单值、CELL-T1M create
// 多值、CELL-T2 update 单值 + 裸值对照格。修复前 T1/T1M/T2 均
// FAIL（原值落库）、对照 PASS；修复后全部 PASS。另覆括号 IPv6
// 多值格（stripShareHostPort 逐项语义：括号剥离）。
//
// 修法：后端两处按 , 分项复用 stripShareHostPort（整串 strip 对含
// 逗号串无效；多值 "198.51.100.77:8443,2001:db8::1" 须逐项剥离）。
// buildTunnelInIP 已剥离，不动；存量不改写。

type r31FixF01Fixture struct {
	h      *Handler
	nodeID int64
}

func setupR31FixF01(t *testing.T) *r31FixF01Fixture {
	t.Helper()
	fx := setupNftablesHandler(t)
	seedNftablesSSHConfig(t, fx.handler, fx.nodeID)
	return &r31FixF01Fixture{h: fx.handler, nodeID: fx.nodeID}
}

// createTunnel posts a tunnelCreate with the given explicit inIp and
// returns the stored tunnel.in_ip (raw DB read, COALESCE NULL → "").
func (fx *r31FixF01Fixture) createTunnel(t *testing.T, name, inIP string) string {
	t.Helper()
	manager := newBlockingNftablesManager()
	fx.h.nftablesManager = newSerializedNftManager(manager)
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
	calls := runWithBlockedNftTest(t, fx.h, manager, func() {
		fx.h.tunnelCreate(res, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/create", body))
	})
	assertNftablesSuccessWithBody(t, res)
	if calls != 1 {
		t.Fatalf("expected capability Test exactly once per create, got %d", calls)
	}
	var stored string
	if err := fx.h.repo.DB().Raw(`SELECT COALESCE(in_ip, '') FROM tunnel WHERE name = ?`, name).Scan(&stored).Error; err != nil {
		t.Fatalf("query tunnel.in_ip: %v", err)
	}
	return stored
}

func (fx *r31FixF01Fixture) storedTunnelInIP(t *testing.T, tunnelID int64) string {
	t.Helper()
	var stored string
	if err := fx.h.repo.DB().Raw(`SELECT COALESCE(in_ip, '') FROM tunnel WHERE id = ?`, tunnelID).Scan(&stored).Error; err != nil {
		t.Fatalf("query tunnel.in_ip: %v", err)
	}
	return stored
}

func TestR31FixF01TunnelCreateStripsPortedInIP(t *testing.T) {
	fx := setupR31FixF01(t)
	if got := fx.createTunnel(t, "r31-f01-t1", "198.51.100.77:8443"); got != "198.51.100.77" {
		t.Errorf("CELL-T1 tunnel.in_ip = %q, want %q (ported inIp stored verbatim via tunnelCreate)", got, "198.51.100.77")
	}
}

func TestR31FixF01TunnelCreateStripsPortedMultiValueInIP(t *testing.T) {
	fx := setupR31FixF01(t)
	if got := fx.createTunnel(t, "r31-f01-t1m", "198.51.100.77:8443,2001:db8::1"); got != "198.51.100.77,2001:db8::1" {
		t.Errorf("CELL-T1M tunnel.in_ip = %q, want %q (multi-value ported inIp stored verbatim via tunnelCreate)", got, "198.51.100.77,2001:db8::1")
	}
}

func TestR31FixF01TunnelCreateStripsBracketedV6MultiValueInIP(t *testing.T) {
	fx := setupR31FixF01(t)
	if got := fx.createTunnel(t, "r31-f01-t1v6", "198.51.100.77:8443,[2001:db8::1]:8443"); got != "198.51.100.77,2001:db8::1" {
		t.Errorf("CELL-T1V6 tunnel.in_ip = %q, want %q (bracketed v6 multi-value ported inIp stored verbatim via tunnelCreate)", got, "198.51.100.77,2001:db8::1")
	}
}

func TestR31FixF01TunnelCreateBareInIPControl(t *testing.T) {
	fx := setupR31FixF01(t)
	if got := fx.createTunnel(t, "r31-f01-control", "198.51.100.77"); got != "198.51.100.77" {
		t.Errorf("control tunnel.in_ip = %q, want %q", got, "198.51.100.77")
	}
}

func TestR31FixF01TunnelUpdateStripsPortedInIP(t *testing.T) {
	fx := setupR31FixF01(t)
	tunnelID := seedTunnelForNftables(t, fx.h, "r31-f01-t2", fx.nodeID)
	manager := newBlockingNftablesManager()
	fx.h.nftablesManager = newSerializedNftManager(manager)
	body := bytes.NewReader([]byte(`{
		"id":` + int64JSON(tunnelID) + `,
		"name":"r31-f01-t2",
		"type":1,
		"flow":1,
		"trafficRatio":1,
		"status":1,
		"inIp":"198.51.100.77:8443",
		"inNodeId":[{"nodeId":` + int64JSON(fx.nodeID) + `,"protocol":"tls"}]
	}`))
	res := httptest.NewRecorder()
	calls := runWithBlockedNftTest(t, fx.h, manager, func() {
		fx.h.tunnelUpdate(res, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/update", body))
	})
	assertNftablesSuccessWithBody(t, res)
	if calls != 1 {
		t.Fatalf("expected capability Test exactly once per update, got %d", calls)
	}
	if got := fx.storedTunnelInIP(t, tunnelID); got != "198.51.100.77" {
		t.Errorf("CELL-T2 tunnel.in_ip = %q, want %q (ported inIp stored verbatim via tunnelUpdate, overriding stripped derived value)", got, "198.51.100.77")
	}
}
