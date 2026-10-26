package handler

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// R32-L4-E01 / R32-FIX-F01（产品 P2）：节点监听地址
// TCPListenAddr/UDPListenAddr 含 :port 时三处直拼产出畸形地址——
// intake nodeCreate（mutations.go:632-633）与 nodeUpdate（:773/:780）
// 原值入库、零校验；消费 realmListenAddr（realm.go:100）、gost 默认
// 监听（control_plane.go:2057，bindIP 为空分支）、链式服务监听
// （mutations.go:6540）三处 fmt.Sprintf("%s:%d", listenAddr, port)
// 直拼；processServerAddress（control_plane.go:2144）对
// "host:port:port" 原样返回。探针按 L4 报告逐格重建（L4 原始探针
// 文件已按 N-37 跑后即删，结论以 R32-L4-probe-listenaddr.log 为准）：
// CELL-R realm、CELL-A gost 服务双格、裸值对照格。修复前 CELL-R /
// CELL-A 均 FAIL（"203.0.113.7:9999:443" / "203.0.113.7:9999:22001"，
// too many colons）、对照 PASS；修复后全部 PASS。
//
// 修法（FIX-PLAN 定夺）：(a) intake 侧 nodeCreate/nodeUpdate 按
// ServerIP intake 前例剥离 :port（新 helper stripListenAddrPort——
// 不复用 stripShareHostPort：后者会把默认 "[::]" 修剪成 "::"，改写
// 存量默认形态）；(b) 消费侧三处直拼调用点先剥离；另同形第四处
// （peer_share_runtime_lifecycle.go:76 联邦 peer-share 服务地址）
// 一并剥离。processServerAddress 的 IPv6 加括号语义不动；D-01
// 「端口胜出」bindIP 分支不动；存量监听地址不改写（未提交字段保持
// 原值，见末格）。

func r32f01ListenAddrs(t *testing.T, r *repo.Repository, name string) (string, string) {
	t.Helper()
	var tcp, udp string
	row := r.DB().Raw(`SELECT tcp_listen_addr, udp_listen_addr FROM node WHERE name = ?`, name).Row()
	if err := row.Scan(&tcp, &udp); err != nil {
		t.Fatalf("query node %q listen addrs: %v", name, err)
	}
	return tcp, udp
}

func TestR32L4PortedTCPListenAddrRealm(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "203.0.113.7:9999"}
	got := realmListenAddr(node, 443, "")
	if got != "203.0.113.7:443" {
		t.Errorf("CELL-R realmListenAddr with ported TCPListenAddr = %q, not a valid host:port: %v (want e.g. 203.0.113.7:443 or rejection at intake)", got, mustSplitHostPortErr(got))
	}
}

func TestR32L4PortedListenAddrGostServices(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "203.0.113.7:9999", UDPListenAddr: "203.0.113.7:9999"}
	forward := &forwardRecord{RemoteAddr: "127.0.0.1:8080"}
	services := buildForwardServiceConfigs("r32f01", forward, &tunnelRecord{}, node, 22001, "", forwardRuntimeLimiters{})
	if len(services) != 2 {
		t.Fatalf("CELL-A expected 2 services, got %d", len(services))
	}
	for i, svc := range services {
		got, _ := svc["addr"].(string)
		if got != "203.0.113.7:22001" {
			t.Errorf("CELL-A service[%d] addr = %q, not a valid host:port: %v (ported listen addr spliced as %%s:%%d)", i, got, mustSplitHostPortErr(got))
		}
	}
}

func TestR32L4BareListenAddrControl(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "203.0.113.7", UDPListenAddr: "203.0.113.7"}
	if got := realmListenAddr(node, 443, ""); got != "203.0.113.7:443" {
		t.Errorf("control realmListenAddr bare = %q, want %q", got, "203.0.113.7:443")
	}
	forward := &forwardRecord{RemoteAddr: "127.0.0.1:8080"}
	services := buildForwardServiceConfigs("r32f01", forward, &tunnelRecord{}, node, 22001, "", forwardRuntimeLimiters{})
	if len(services) != 2 {
		t.Fatalf("control expected 2 services, got %d", len(services))
	}
	for i, svc := range services {
		if got, _ := svc["addr"].(string); got != "203.0.113.7:22001" {
			t.Errorf("control service[%d] addr = %q, want %q", i, got, "203.0.113.7:22001")
		}
	}
}

func TestR32FixF01ChainServiceStripsPortedListenAddr(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "203.0.113.7:9999"}
	services := buildTunnelChainServiceConfig(1, tunnelRuntimeNode{Port: 22001, Protocol: "tls"}, node, 1)
	if len(services) != 1 {
		t.Fatalf("CELL-C expected 1 chain service, got %d", len(services))
	}
	got, _ := services[0]["addr"].(string)
	if got != "203.0.113.7:22001" {
		t.Errorf("CELL-C chain service addr = %q, not a valid host:port: %v (ported listen addr spliced as %%s:%%d)", got, mustSplitHostPortErr(got))
	}
}

func TestR32FixF01BracketedV6ListenAddrStripped(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "[2001:db8::1]:9999"}
	if got := realmListenAddr(node, 443, ""); got != "[2001:db8::1]:443" {
		t.Errorf("CELL-V6 realmListenAddr bracketed-v6 ported = %q, want %q", got, "[2001:db8::1]:443")
	}
	// 同值走 gost 面：裸 IPv6 拼端口须加括号，不得误读成另一地址。
	nodeUDP := &nodeRecord{TCPListenAddr: "[2001:db8::1]:9999", UDPListenAddr: "[2001:db8::1]:9999"}
	forward := &forwardRecord{RemoteAddr: "127.0.0.1:8080"}
	services := buildForwardServiceConfigs("r32f01", forward, &tunnelRecord{}, nodeUDP, 22001, "", forwardRuntimeLimiters{})
	if len(services) != 2 {
		t.Fatalf("CELL-V6 expected 2 services, got %d", len(services))
	}
	for i, svc := range services {
		if got, _ := svc["addr"].(string); got != "[2001:db8::1]:22001" {
			t.Errorf("CELL-V6 service[%d] addr = %q, want %q", i, got, "[2001:db8::1]:22001")
		}
	}
}

func TestR32FixF01WildcardListenAddrControl(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	if got := realmListenAddr(node, 443, ""); got != "[::]:443" {
		t.Errorf("control realmListenAddr wildcard = %q, want %q", got, "[::]:443")
	}
	forward := &forwardRecord{RemoteAddr: "127.0.0.1:8080"}
	services := buildForwardServiceConfigs("r32f01", forward, &tunnelRecord{}, node, 22001, "", forwardRuntimeLimiters{})
	if len(services) != 2 {
		t.Fatalf("control expected 2 services, got %d", len(services))
	}
	for i, svc := range services {
		if got, _ := svc["addr"].(string); got != "[::]:22001" {
			t.Errorf("control service[%d] addr = %q, want %q", i, got, "[::]:22001")
		}
	}
}

func TestR32FixF01NodeCreateStripsPortedListenAddr(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r32-f01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	r30f04CreateNode(t, h, `{"name":"r32f01-c","serverIp":"198.51.100.77","tcpListenAddr":"203.0.113.7:9999","udpListenAddr":"203.0.113.7:9998","forwardMode":"agent"}`)
	gotTCP, gotUDP := r32f01ListenAddrs(t, r, "r32f01-c")
	if gotTCP != "203.0.113.7" {
		t.Errorf("CELL-I1 tcpListenAddr stored %q, want %q (ported value stored verbatim via nodeCreate)", gotTCP, "203.0.113.7")
	}
	if gotUDP != "203.0.113.7" {
		t.Errorf("CELL-I1 udpListenAddr stored %q, want %q (ported value stored verbatim via nodeCreate)", gotUDP, "203.0.113.7")
	}
}

func TestR32FixF01NodeCreateWildcardDefaultKept(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r32-f01b.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	r30f04CreateNode(t, h, `{"name":"r32f01-def","serverIp":"198.51.100.77","forwardMode":"agent"}`)
	gotTCP, gotUDP := r32f01ListenAddrs(t, r, "r32f01-def")
	if gotTCP != "[::]" || gotUDP != "[::]" {
		t.Errorf("control default listen addrs stored %q/%q, want [::]/[::]", gotTCP, gotUDP)
	}
}

func TestR32FixF01NodeUpdateStripsPortedListenAddr(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r32-f01c.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	r30f04CreateNode(t, h, `{"name":"r32f01-u","serverIp":"198.51.100.77","forwardMode":"agent"}`)
	var id int64
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = ?`, "r32f01-u").Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("lookup node id: %d %v", id, err)
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"id": id, "tcpListenAddr": "203.0.113.9:9999", "udpListenAddr": "[2001:db8::9]:9999",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/update", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.nodeUpdate(rec, req)
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, rec.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("nodeUpdate failed: %+v body=%s", out, rec.Body.String())
	}
	gotTCP, gotUDP := r32f01ListenAddrs(t, r, "r32f01-u")
	if gotTCP != "203.0.113.9" {
		t.Errorf("CELL-I2 tcpListenAddr stored %q, want %q (ported value stored verbatim via nodeUpdate)", gotTCP, "203.0.113.9")
	}
	if gotUDP != "2001:db8::9" {
		t.Errorf("CELL-I2 udpListenAddr stored %q, want %q (bracketed v6 with :port)", gotUDP, "2001:db8::9")
	}
}

func TestR32FixF01NodeUpdateUntouchedListenAddrKeepsStock(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r32-f01d.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	// 存量带端口值（经直接写库模拟旧数据）：只改 name 的更新不得
	// 顺手改写未提交字段——存量不改写（与 R30-F04 存量口径同形）。
	now := int64(1793000000000)
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, '30000-30010', '', 'v1', 0, 0, 0, ?, ?, 0, '203.0.113.7:9999', '203.0.113.7:9998', 0)`,
		"r32f01-stock", "r32f01-stock-secret", "198.51.100.77", now, now).Error; err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = ?`, "r32f01-stock").Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"id": id, "name": "r32f01-stock-renamed"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/update", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.nodeUpdate(rec, req)
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, rec.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("nodeUpdate failed: %+v body=%s", out, rec.Body.String())
	}
	gotTCP, gotUDP := r32f01ListenAddrs(t, r, "r32f01-stock-renamed")
	if gotTCP != "203.0.113.7:9999" || gotUDP != "203.0.113.7:9998" {
		t.Errorf("untouched stock listen addrs must stay verbatim, got %q/%q", gotTCP, gotUDP)
	}
}

// mustSplitHostPortErr reports the SplitHostPort error for a value that
// should be a valid host:port (nil when valid, so %v prints <nil>).
func mustSplitHostPortErr(addr string) error {
	_, _, err := net.SplitHostPort(addr)
	return err
}
