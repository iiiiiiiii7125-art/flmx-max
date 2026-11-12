package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// LIVE-215 红 1（新建中转入口标记时序）：inboundRelayCreate 先调
// createRelayForward（其内 syncForwardServices 按当时 DB 现算
// connlogEntryForwardIDs），此时 relay 行尚未 CreateInboundRelay
// 落库，故入口服务以下发时无 connlogEntry 标记；落库后不再重推，
// 标记永不补发，新建中转的连接日志零行（agent 侧经诊断复核已证
// 全通：无改动 update 重推后 journal 即现标记）。
//
// 本测试端到端钉死：新建中转（relay 创建＋forward 落库）后，入口
// 节点收到的最后一次 UpdateService 必须带 connlogEntry 标记
// （kind=forward、id=服务名），无需任何手动重推/手动 update。
// 在 2dc8792a 上本测试为红：新建中转的全部 UpdateService 均无标记。

type r215Red1Scenario struct {
	h         *Handler
	repo      *repo.Repository
	frontRC   *r1b3CommandRecorder
	landingRC *r1b3CommandRecorder
	tunnelID  int64
	inboundID int64
}

func setupR215Red1(t *testing.T) *r215Red1Scenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r215-red1.db")
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

	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	s := &r215Red1Scenario{h: h, repo: r,
		frontRC: &r1b3CommandRecorder{}, landingRC: &r1b3CommandRecorder{}}
	frontID := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	exitAID := seedRelayTargetNode(t, r, "exit-a", "198.51.100.2")
	landingID := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	// 新门控：入口标记只下发给采集开关开着的入口节点——前置
	// 节点开采集，否则本测试的标记断言在新门控下恒红。
	if err := r.UpdateNodeConnLogEnabled(frontID, 1); err != nil {
		t.Fatalf("enable front connlog: %v", err)
	}
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatalf("activate nodes: %v", err)
	}
	s.tunnelID = seedRelayTargetTunnel(t, r, "r215-tunnel", frontID, 30000, exitAID)
	landingBox := seedChainKernel(t, r, landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	target.RelayWhitelist = 1
	if err := r.SaveNodeInbound(target); err != nil {
		t.Fatalf("save inbound whitelist: %v", err)
	}
	s.inboundID = target.ID

	connectR1B3Agent(t, ts, "front-secret", s.frontRC)
	connectR1B3Agent(t, ts, "landing-secret", s.landingRC)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(frontID) && server.IsNodeOnline(landingID)
	}, "mock agents online")
	waitForCondition(t, 5*time.Second, func() bool {
		return len(s.landingRC.kernelApplyAllows()) > 0
	}, "initial kernel apply on reconnect")
	return s
}

// markedServicesOfForward 从 front 侧 UpdateService 流里按出现顺序
// 取出属于该 forward 的服务（每条命令一组），每组服务标出是否带
// 入口标记。命令在 mock agent 回 ACK 前已记录，同步 handler 调用
// 返回后流即完整，无需 sleep。
func markedServicesOfForward(t *testing.T, rc *r1b3CommandRecorder, forwardID int64) [][]map[string]interface{} {
	t.Helper()
	prefix := fmt.Sprintf("%d_", forwardID)
	var groups [][]map[string]interface{}
	for _, c := range rc.snapshot() {
		if c.Type != "UpdateService" {
			continue
		}
		var services []map[string]interface{}
		if err := json.Unmarshal(c.Data, &services); err != nil {
			t.Fatalf("decode UpdateService payload: %v (%s)", err, string(c.Data))
		}
		var group []map[string]interface{}
		for _, svc := range services {
			name, _ := svc["name"].(string)
			if strings.HasPrefix(name, prefix) {
				group = append(group, svc)
			}
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

func serviceHasConnlogMark(svc map[string]interface{}) bool {
	md, _ := svc["metadata"].(map[string]interface{})
	if md == nil {
		return false
	}
	name, _ := svc["name"].(string)
	return md[connlogEntryKindMetadataKey] == connlogEntryKindForward &&
		md[connlogEntryIDMetadataKey] == name
}

func TestR215Red1_NewRelayEntryServicesCarryConnlogMark(t *testing.T) {
	s := setupR215Red1(t)
	body := r2b2AuthPost(t, s.h.inboundRelayCreate, "/api/v1/inbound/relay/create",
		map[string]interface{}{"inboundId": s.inboundID, "tunnelId": s.tunnelID, "inPort": 30007, "name": "r215-red1"}, "r215-red1-key")
	var env struct {
		response.R
		Data struct {
			ID        int64 `json:"id"`
			ForwardID int64 `json:"forwardId"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode create response: %v (%s)", err, body)
	}
	if env.Code != 0 {
		t.Fatalf("inboundRelayCreate must succeed, got %s", body)
	}
	if env.Data.ForwardID <= 0 {
		t.Fatalf("create response carries no forwardId: %s", body)
	}
	if n := mustQueryInt(t, s.repo, `SELECT COUNT(*) FROM node_inbound_relay WHERE forward_id = ?`, env.Data.ForwardID); n != 1 {
		t.Fatalf("relay row missing for forward %d", env.Data.ForwardID)
	}

	groups := markedServicesOfForward(t, s.frontRC, env.Data.ForwardID)
	if len(groups) == 0 {
		t.Fatalf("no UpdateService reached the entry node for new relay forward %d", env.Data.ForwardID)
	}
	// 证据：首推（落库前同步）无标记、末推（落库后重推）带标记。
	t.Logf("forward %d: %d UpdateService group(s); first marked=%v, last marked=%v",
		env.Data.ForwardID, len(groups), serviceHasConnlogMark(groups[0][0]), serviceHasConnlogMark(groups[len(groups)-1][0]))
	last := groups[len(groups)-1]
	if len(last) != 2 {
		t.Fatalf("last UpdateService for forward %d must carry tcp+udp services, got %d: %v", env.Data.ForwardID, len(last), last)
	}
	for _, svc := range last {
		if !serviceHasConnlogMark(svc) {
			t.Fatalf("LIVE-215 红 1: last UpdateService for new relay forward %d still unmarked "+
				"(create-then-mark timing: relay row landed after the only sync, no repush); "+
				"groups=%d last=%v", env.Data.ForwardID, len(groups), last)
		}
	}
}
