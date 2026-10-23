package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// R5 批 2 · R5X-004（P2）删除收尾族反向验证（仿 R4B6 夹具形态）。
//
// 口径：删除类操作「已实际删行即与成功无关地收尾」——部分完成中止
// 返回前必须以删前收集集按现行 DB 补刷白名单。断言面为落地节点收到
// 的末次 KernelApply 的 allow 集（与 R4B6/R1B3 同口径）。
//
// 子面 (a) tunnelDelete：同隧道两条 Managed relay 转发分别落地两个
// 入站，第二条转发的删行以 SQLite 触发器注入失败——第一条已实际
// 删除、清理中止、回执失败；修复前 landing1 收不到任何新 KernelApply
// （allow 仍含已删转发的拨号出口），修复后末次 allow 不含出口 A。
// 子面 (b) userDelete：受害用户的两条转发分属两条隧道（分组逐组清
// 理），第二组删行失败中止；断言面同 (a)。

type r5b2Scenario struct {
	h          *Handler
	repo       *repo.Repository
	server     *ws.Server
	frontID    int64
	exitAID    int64
	exitBID    int64
	landing1   int64
	landing2   int64
	tunnelA    int64
	tunnelB    int64
	inbound1   int64
	inbound2   int64
	forwardA   int64
	forwardB   int64
	ownerID    int64
	landingRC1 *r1b3CommandRecorder
	landingRC2 *r1b3CommandRecorder
}

// setupR5B2Scenario: front 入口、exit-a/exit-b 出口；landing1/landing2
// 各一个 RelayWhitelist=1 入站。forwardA 经 tunnelA 落 inbound1；
// forwardB 落 inbound2，其隧道由 sameTunnelForB 决定：(a) 同 tunnelA、
// (b) tunnelB。ownerID 为两条转发的属主（(a) 用 admin=1，(b) 用受害
// 用户=2）。
func setupR5B2Scenario(t *testing.T, sameTunnelForB bool, ownerID int64) *r5b2Scenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r5b2.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	server := ws.NewServer(r, "test-secret")
	server.SetNodeOnlineHook(h.onNodeOnline)
	server.SetNodeOfflineHook(h.notifyNodeOffline)
	h.wsServer = server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)

	s := &r5b2Scenario{h: h, repo: r, server: server, ownerID: ownerID,
		landingRC1: &r1b3CommandRecorder{}, landingRC2: &r1b3CommandRecorder{}}
	now := time.Now().UnixMilli()
	// repo.Open 已预置用户 ID 1；新建用户的真实 ID 以返回值准（勿硬编码）。
	adminID, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if ownerID != 1 {
		victimID, err := r.CreateUser("victim", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
		if err != nil {
			t.Fatalf("create victim: %v", err)
		}
		s.ownerID = victimID
	} else {
		s.ownerID = adminID
	}
	ownerID = s.ownerID
	s.frontID = seedRelayTargetNode(t, r, "front", "198.51.100.1")
	s.exitAID = seedRelayTargetNode(t, r, "exit-a", "198.51.100.2")
	s.exitBID = seedRelayTargetNode(t, r, "exit-b", "198.51.100.3")
	s.landing1 = seedRelayTargetNode(t, r, "landing1", "203.0.113.10")
	s.landing2 = seedRelayTargetNode(t, r, "landing2", "203.0.113.11")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE node SET port = '1000-65535'`).Error; err != nil {
		t.Fatal(err)
	}
	s.tunnelA = seedRelayTargetTunnel(t, r, "r5b2-a", s.frontID, 30000, s.exitAID)
	s.tunnelB = seedRelayTargetTunnel(t, r, "r5b2-b", s.frontID, 30000, s.exitBID)

	ownerName := "admin"
	if ownerID != 1 {
		ownerName = "victim"
	}
	mkLanding := func(nodeID int64, remote string, tunnelID int64, inx int, fwdName string) (int64, int64) {
		box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
		target := seedChainInbound(t, r, box, fwdName+"-inbound", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
			Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
		target.RelayWhitelist = 1
		if err := r.SaveNodeInbound(target); err != nil {
			t.Fatal(err)
		}
		fwdID, err := r.CreateForwardTx(ownerID, ownerName, fwdName, tunnelID, remote, "fifo", now, inx,
			[]int64{s.frontID}, 20000+inx, "", nil, 0, 0, nil, 0, 0, 0)
		if err != nil {
			t.Fatalf("create forward %s: %v", fwdName, err)
		}
		if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: fwdID, Name: "relay-" + fwdName, Managed: 1, CreatedTime: now}); err != nil {
			t.Fatal(err)
		}
		return target.ID, fwdID
	}
	s.inbound1, s.forwardA = mkLanding(s.landing1, "203.0.113.10:443", s.tunnelA, 1, "fwd-a")
	bTunnel := s.tunnelB
	if sameTunnelForB {
		bTunnel = s.tunnelA
	}
	s.inbound2, s.forwardB = mkLanding(s.landing2, "203.0.113.11:443", bTunnel, 2, "fwd-b")

	frontRC := &r1b3CommandRecorder{}
	connectR1B3Agent(t, ts, "front-secret", frontRC)
	connectR1B3Agent(t, ts, "landing1-secret", s.landingRC1)
	connectR1B3Agent(t, ts, "landing2-secret", s.landingRC2)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(s.frontID) && server.IsNodeOnline(s.landing1) && server.IsNodeOnline(s.landing2)
	}, "mock agents online")
	waitForCondition(t, 5*time.Second, func() bool {
		return len(s.landingRC1.kernelApplyAllows()) > 0 && len(s.landingRC2.kernelApplyAllows()) > 0
	}, "initial kernel applies on reconnect")
	// 等上线对账下发流静默，避免在途下发落进操作窗被误判。
	quiesce := func(rc *r1b3CommandRecorder) {
		waitForCondition(t, 5*time.Second, func() bool {
			n := len(rc.kernelApplyAllows())
			time.Sleep(400 * time.Millisecond)
			return len(rc.kernelApplyAllows()) == n
		}, "kernel apply stream quiescent")
	}
	quiesce(s.landingRC1)
	quiesce(s.landingRC2)
	initial := s.landingRC1.kernelApplyAllows()
	if !strings.Contains(initial[len(initial)-1], "198.51.100.2/32") {
		t.Fatalf("setup: landing1 initial allow must contain exit A, got %v", initial)
	}
	return s
}

// failForwardDelete installs a trigger aborting the DB delete of the
// given forward row (DeleteForwardCascade 的转发本体删除步），使清理
// 漏斗在该条转发处中止——第一条转发已在此之前实际删行。
func (s *r5b2Scenario) failForwardDelete(t *testing.T, forwardID int64) {
	t.Helper()
	trigger := fmt.Sprintf(`CREATE TRIGGER r5b2_fail_forward_delete BEFORE DELETE ON forward FOR EACH ROW WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, 'r5b2 injected forward delete failure'); END`, forwardID)
	if err := s.repo.DB().Exec(trigger).Error; err != nil {
		t.Fatalf("install delete-failure trigger: %v", err)
	}
}

func (s *r5b2Scenario) assertForwardGone(t *testing.T, forwardID int64, gone bool) {
	t.Helper()
	_, err := s.h.getForwardRecord(forwardID)
	if gone && err == nil {
		t.Fatalf("forward %d must be deleted", forwardID)
	}
	if !gone && err != nil {
		t.Fatalf("forward %d must survive, got err=%v", forwardID, err)
	}
}

func (s *r5b2Scenario) assertLanding1RefreshedWithoutExitA(t *testing.T, baseline int) {
	t.Helper()
	allows := s.landingRC1.kernelApplyAllows()
	if len(allows) <= baseline {
		t.Fatalf("R5X-004: no KernelApply repushed to landing1 after partial delete (whitelist not refreshed), baseline=%d total=%d", baseline, len(allows))
	}
	last := allows[len(allows)-1]
	if strings.Contains(last, "198.51.100.2/32") {
		t.Fatalf("R5X-004: repushed allow must drop the deleted forward's dialer (exit A), got %q (all: %v)", last, allows)
	}
}

// 子面 (a): tunnelDelete 部分完成——第一条转发已删、第二条删行失败
// 中止。回执必须失败、隧道与第二条转发尚存、landing1 收到按现行 DB
// 补刷的 allow（不含出口 A）。
func TestR5B2TunnelDeletePartialFailureRepushesWhitelist(t *testing.T) {
	s := setupR5B2Scenario(t, true, 1)
	s.failForwardDelete(t, s.forwardB)
	baseline := len(s.landingRC1.kernelApplyAllows())

	res := httptest.NewRecorder()
	s.h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": s.tunnelA}))
	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("tunnelDelete must fail when the second forward cleanup fails, got success body=%s", res.Body.String())
	}
	if tun, err := s.h.getTunnelRecord(s.tunnelA); err != nil || tun == nil {
		t.Fatalf("tunnel must survive a partially failed delete, got %+v err=%v", tun, err)
	}
	s.assertForwardGone(t, s.forwardA, true)
	s.assertForwardGone(t, s.forwardB, false)
	s.assertLanding1RefreshedWithoutExitA(t, baseline)
}

// 子面 (b): userDelete 部分完成——第一组（tunnelA）转发已全删、第二
// 组（tunnelB）删行失败中止（offlineTolerant=false）。回执必须失败、
// 用户与第二条转发尚存、landing1 收到补刷 allow（不含出口 A）。
func TestR5B2UserDeletePartialFailureRepushesWhitelist(t *testing.T) {
	s := setupR5B2Scenario(t, false, 2)
	s.failForwardDelete(t, s.forwardB)
	baseline := len(s.landingRC1.kernelApplyAllows())

	res := httptest.NewRecorder()
	s.h.userDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": s.ownerID}))
	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("userDelete must fail when the second tunnel group cleanup fails, got success body=%s", res.Body.String())
	}
	if _, err := s.repo.GetUserRoleID(s.ownerID); err != nil {
		t.Fatalf("user must survive a partially failed delete, got err=%v", err)
	}
	s.assertForwardGone(t, s.forwardA, true)
	s.assertForwardGone(t, s.forwardB, false)
	s.assertLanding1RefreshedWithoutExitA(t, baseline)
}
