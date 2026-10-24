package handler

import (
	"bytes"
	"encoding/json"
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

// R13 批 R · R-a（R13X-001，P2）先红后绿测试：backupImport 成功后，
// 在线节点的转发数据面必须重推（复用 redeploy 路径），失败进回执。
// 场景复用 R1B3 的种子形态，但同时保留入口节点（front）的命令记录，
// 以便断言 UpdateService 真的发到了在线入口节点。

type r13brScenario struct {
	h         *Handler
	repo      *repo.Repository
	server    *ws.Server
	frontRC   *r1b3CommandRecorder
	landingRC *r1b3CommandRecorder
	tunnelID  int64
	forwardID int64
	frontID   int64
	exitAID   int64
	exitBID   int64
	landingID int64
}

func setupR13BR(t *testing.T) *r13brScenario {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r13br.db")
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
	s := &r13brScenario{h: h, repo: r, frontRC: &r1b3CommandRecorder{}, landingRC: &r1b3CommandRecorder{}}
	s.frontID = seedRelayTargetNode(t, r, "front", "198.51.100.1")
	s.exitAID = seedRelayTargetNode(t, r, "exit-a", "198.51.100.2")
	s.exitBID = seedRelayTargetNode(t, r, "exit-b", "198.51.100.3")
	s.landingID = seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	if err := r.DB().Exec(`UPDATE node SET status = 1`).Error; err != nil {
		t.Fatalf("activate nodes: %v", err)
	}
	s.tunnelID = seedRelayTargetTunnel(t, r, "wl-tunnel", s.frontID, 30000, s.exitAID)
	landingBox := seedChainKernel(t, r, s.landingID, kernel.TypeSingbox)
	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	target.RelayWhitelist = 1
	if err := r.SaveNodeInbound(target); err != nil {
		t.Fatalf("save inbound whitelist: %v", err)
	}
	forwardID, err := r.CreateForwardTx(1, "admin", "relay-fwd", s.tunnelID, "203.0.113.10:443", "fifo", now, 1,
		[]int64{s.frontID}, 20000, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	s.forwardID = forwardID
	if err := r.CreateInboundRelay(&model.NodeInboundRelay{InboundID: target.ID, ForwardID: forwardID, Name: "relay", Managed: 1, CreatedTime: now}); err != nil {
		t.Fatalf("create relay: %v", err)
	}
	connectR1B3Agent(t, ts, "front-secret", s.frontRC)
	connectR1B3Agent(t, ts, "landing-secret", s.landingRC)
	waitForCondition(t, 5*time.Second, func() bool {
		return server.IsNodeOnline(s.frontID) && server.IsNodeOnline(s.landingID)
	}, "mock agents online")
	waitForCondition(t, 5*time.Second, func() bool {
		return len(s.landingRC.kernelApplyAllows()) > 0
	}, "initial kernel apply on reconnect")
	return s
}

func (s *r13brScenario) postImport(t *testing.T, types []string, data *model.BackupData) string {
	t.Helper()
	body := struct {
		Types []string `json:"types"`
		*model.BackupData
	}{Types: types, BackupData: data}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal import body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	s.h.backupImport(rec, req)
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("backupImport types=%v: code=%d err=%v body=%s", types, env.Code, err, rec.Body.String())
	}
	return rec.Body.String()
}

func (s *r13brScenario) frontUpdateServices() []cleanupAgentCommand {
	var out []cleanupAgentCommand
	for _, c := range s.frontRC.snapshot() {
		if c.Type == "UpdateService" {
			out = append(out, c)
		}
	}
	return out
}

func (s *r13brScenario) waitFrontUpdateWith(t *testing.T, baseline int, want string) {
	t.Helper()
	waitForCondition(t, 10*time.Second, func() bool {
		cmds := s.frontUpdateServices()
		if len(cmds) <= baseline {
			return false
		}
		for _, c := range cmds[baseline:] {
			if strings.Contains(string(c.Data), want) {
				return true
			}
		}
		return false
	}, "front UpdateService containing "+want)
}

// ① forwards 导入改端口：在线入口节点必须收到带新端口的 UpdateService，
// 旧服务被替换（同服务名、新参数下发），回执失败节点清单为空。
func TestR13BRBackupImportForwardsRepushesOnlineNode(t *testing.T) {
	s := setupR13BR(t)
	baseline := len(s.frontUpdateServices())
	data, err := s.repo.ExportPartial([]string{"forwards"})
	if err != nil {
		t.Fatalf("export forwards: %v", err)
	}
	mutated := false
	for i := range data.Forwards {
		if data.Forwards[i].ID == s.forwardID && data.Forwards[i].ForwardPorts != nil {
			for j := range *data.Forwards[i].ForwardPorts {
				if (*data.Forwards[i].ForwardPorts)[j].NodeID == s.frontID {
					(*data.Forwards[i].ForwardPorts)[j].Port = 20001
					mutated = true
				}
			}
		}
	}
	if !mutated {
		t.Fatal("setup: forward port missing from forwards export")
	}
	body := s.postImport(t, []string{"forwards"}, data)
	if !strings.Contains(body, `"forwardsImported":1`) {
		t.Fatalf("forwards segment must report 1 imported row, body=%s", body)
	}
	s.waitFrontUpdateWith(t, baseline, "20001")
	// 回执披露：在线入口节点在重推清单内、失败清单为空（失败不静默）。
	var env struct {
		Data model.ImportResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode import receipt: %v", err)
	}
	found := false
	for _, id := range env.Data.RepushNodeIDs {
		if id == s.frontID {
			found = true
		}
	}
	if !found {
		t.Fatalf("R13X-001: receipt repushNodeIds must include online front node %d, got %v", s.frontID, env.Data.RepushNodeIDs)
	}
	if len(env.Data.RepushFailedNodeIDs) != 0 {
		t.Fatalf("R13X-001: receipt repushFailedNodeIds must be empty for the online node, got %v", env.Data.RepushFailedNodeIDs)
	}
}

// ①b 无 live 连接时重推全跳过：离线节点留给重连路径（onNodeOnline，
// 本批未改动），不得计入失败、不得尝试下发。
func TestR13BRRepushSkipsWhenNoLiveAgent(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r13br-offline.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(id, name, secret, server_ip, port, created_time, updated_time, status) VALUES(10, 'n1', 's', '203.0.113.1', 0, ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, status, created_time, updated_time) VALUES(50, 1, 'admin', 'fwd', 0, '203.0.113.1:443', 'fifo', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(50, 10, 20000)`).Error; err != nil {
		t.Fatalf("insert forward port: %v", err)
	}
	h := New(r, "test-secret") // wsServer 保持 nil = 无 live 连接
	out := h.repushForwardRuntimeAfterImport(&model.ImportResult{ForwardsImported: 1}, &model.BackupData{Version: "1.0"})
	if len(out.attempted) != 0 || len(out.failed) != 0 {
		t.Fatalf("R13X-001: offline/no-agent repush must attempt nothing and fail nothing, got attempted=%v failed=%v", out.attempted, out.failed)
	}
}

// ①c 触发条件：forwards / relays-only 触发，纯内核导入不触发。
func TestR13BRRepushTrigger(t *testing.T) {
	cases := []struct {
		name   string
		result *model.ImportResult
		want   bool
	}{
		{"nil", nil, false},
		{"forwards", &model.ImportResult{ForwardsImported: 1}, true},
		{"tunnels", &model.ImportResult{TunnelsImported: 1}, true},
		{"nodes", &model.ImportResult{NodesImported: 1}, true},
		{"inbounds-rows", &model.ImportResult{InboundsImported: 1}, true},
		{"relays-only", &model.ImportResult{InboundRelaysImported: 1}, true},
		{"kernels-only", &model.ImportResult{InboundKernelsImported: 2}, false},
		{"subscriptions-only", &model.ImportResult{InboundSubscriptionsImported: 2}, false},
	}
	for _, c := range cases {
		if got := backupImportNeedsForwardRepush(c.result); got != c.want {
			t.Errorf("R13X-001 trigger %s = %v, want %v", c.name, got, c.want)
		}
	}
}

// ② nodes 导入改落地节点地址：托管中转的已存 RemoteAddr 必须重算，
// 且重算后的新地址必须下发到在线入口节点。
func TestR13BRBackupImportNodesResyncsRelayRemoteAddr(t *testing.T) {
	s := setupR13BR(t)
	baseline := len(s.frontUpdateServices())
	data, err := s.repo.ExportPartial([]string{"nodes"})
	if err != nil {
		t.Fatalf("export nodes: %v", err)
	}
	mutated := false
	for i := range data.Nodes {
		if data.Nodes[i].ID == s.landingID {
			data.Nodes[i].ServerIP = "203.0.113.99"
			data.Nodes[i].ServerIPv4 = "203.0.113.99"
			mutated = true
		}
	}
	if !mutated {
		t.Fatal("setup: landing node missing from nodes export")
	}
	s.postImport(t, []string{"nodes"}, data)
	fr, err := s.repo.GetForwardRecord(s.forwardID)
	if err != nil || fr == nil {
		t.Fatalf("read forward: %v", err)
	}
	if fr.RemoteAddr != "203.0.113.99:443" {
		t.Fatalf("R13X-001: managed relay RemoteAddr = %q, want %q after nodes import", fr.RemoteAddr, "203.0.113.99:443")
	}
	s.waitFrontUpdateWith(t, baseline, "203.0.113.99")
}
