package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// PORT-CLOSURE Lane C 修复探针（基线 c6e90c7；FIX-PLAN F-04/F-05/F-07
// 入口侧 + B-NEW-01 + O-C-01 + ConnectIP）。
//
// 重构说明（须明言）：sweep 原始红探针
// （portclosure_c_probe_test.go，已按 N-37 细则④跑后删除，红日志
// probe-raw/portclosure-c-red.log）中 C01/C03/C04 的部分格是消费侧
// 直连断言——以手造双端口记录直接调 buildForwardServiceConfigs /
// realmListenAddr / pickNodeAddressV4 / nodeShareIP。FIX-PLAN 定夺的
// 修法是 (a) intake 侧收口（拒绝双端口值落库），消费侧一字不动；
// 手造记录绕过 intake，故那些直连格在修法 (a) 下恒红，不可作为
// 本修法的绿判据。本文件按 FIX-PLAN 实例重建探针时把直连格重构为
// 入口门断言：HTTP intake 拒绝双端口值 + 已落库值经下游重算仍合法
// （单端口对照语义不变）。消费侧对手造双端口记录的容忍/跳过行为
// （nodeShareIP 返回 ""、pickNodeAddressV4 原值返回）是 fail-safe
// 的既有行为，本批不改；存量双端口行不改写（与 R32 F-01 存量口径
// 一致），其消费面残留见 lane 报告。

// pcFixCreateNode posts a raw JSON node-create payload and returns the
// decoded envelope without failing on non-zero codes (reject cases).
func pcFixCreateNode(t *testing.T, h *Handler, payload string) response.R {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/create", bytes.NewReader([]byte(payload)))
	rec := httptest.NewRecorder()
	h.nodeCreate(rec, req)
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode nodeCreate response: %v body=%s", err, rec.Body.String())
	}
	return out
}

func pcFixNodeID(t *testing.T, r *repo.Repository, name string) int64 {
	t.Helper()
	var id int64
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = ?`, name).Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("lookup node %q: id=%d err=%v", name, id, err)
	}
	return id
}

func pcFixNodeAddrs(t *testing.T, r *repo.Repository, name string) (serverIP, v4, v6, tcp, udp, extra string) {
	t.Helper()
	row := r.DB().Raw(`SELECT server_ip, COALESCE(server_ip_v4,''), COALESCE(server_ip_v6,''), COALESCE(tcp_listen_addr,''), COALESCE(udp_listen_addr,''), COALESCE(extra_ips,'') FROM node WHERE name = ?`, name).Row()
	if err := row.Scan(&serverIP, &v4, &v6, &tcp, &udp, &extra); err != nil {
		t.Fatalf("query node %q: %v", name, err)
	}
	return serverIP, v4, v6, tcp, udp, extra
}

// pcFixAttemptForward posts forwardCreate and returns the decoded
// envelope without failing on non-zero codes (reject cases).
func pcFixAttemptForward(t *testing.T, fx *r30FixF01Fixture, name, inIP string, inPort int) response.R {
	t.Helper()
	res := httptest.NewRecorder()
	fx.h.forwardCreate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"tunnelId":   fx.tunnelID,
		"name":       name,
		"remoteAddr": "203.0.113.5:443",
		"inPort":     inPort,
		"inIp":       inIP,
	}))
	var out response.R
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode forwardCreate response: %v body=%s", err, res.Body.String())
	}
	return out
}

func pcFixForwardCount(t *testing.T, fx *r30FixF01Fixture, name string) int {
	t.Helper()
	var n int
	if err := fx.r.DB().Raw(`SELECT COUNT(*) FROM forward WHERE name = ?`, name).Scan(&n).Error; err != nil {
		t.Fatalf("count forward %q: %v", name, err)
	}
	return n
}

// TestPortClosureC01ForwardCreateDoublePortInIP — PC-04 (P2, F-04):
// forwardCreate / forwardUpdate 的 inIp 经单次剥离后须过格式校验，
// 双端口值直接拒绝、不落库；单端口值仍剥离后落库（D-01 端口胜出
// 语义不变），下游 gost/realm 地址合法。
func TestPortClosureC01ForwardCreateDoublePortInIP(t *testing.T) {
	fx := setupR30FixF01(t, "198.51.100.77")
	out := pcFixAttemptForward(t, fx, "pc-c01-double", "203.0.113.7:9999:22001", 30001)
	if out.Code == 0 {
		t.Errorf("CELL-B forwardCreate accepted double-port inIp (code 0); want rejection")
	}
	if n := pcFixForwardCount(t, fx, "pc-c01-double"); n != 0 {
		t.Errorf("CELL-B double-port forward row created (count=%d); want 0 (rejected before insert)", n)
	}
	// 括号 v6 双端口同格。
	out = pcFixAttemptForward(t, fx, "pc-c01-double-v6", "[2001:db8::1]:9999:22001", 30001)
	if out.Code == 0 || pcFixForwardCount(t, fx, "pc-c01-double-v6") != 0 {
		t.Errorf("CELL-B bracketed-v6 double-port inIp accepted (code=%d); want rejection", out.Code)
	}
	// 对照：单端口 inIp 仍接受、剥离后落库，下游地址合法。
	id := fx.createForward(t, "pc-c01-single", "203.0.113.7:9999", 30001)
	if got := fx.storedInIP(t, id); got != "203.0.113.7" {
		t.Errorf("control forward_port.in_ip = %q, want %q", got, "203.0.113.7")
	}
	fx.checkDownstreamAddrs(t, id, 30001, "203.0.113.7:30001")
	// forwardUpdate 同型：双端口 inIp 拒绝、存量 in_ip 不变。
	res := httptest.NewRecorder()
	fx.h.forwardUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id":       id,
		"tunnelId": fx.tunnelID,
		"inPort":   30002,
		"inIp":     "203.0.113.7:9999:22001",
	}))
	var upd response.R
	if err := json.Unmarshal(res.Body.Bytes(), &upd); err != nil {
		t.Fatalf("decode forwardUpdate response: %v body=%s", err, res.Body.String())
	}
	if upd.Code == 0 {
		t.Errorf("CELL-B forwardUpdate accepted double-port inIp (code 0); want rejection")
	}
	if got := fx.storedInIP(t, id); got != "203.0.113.7" {
		t.Errorf("CELL-B forwardUpdate double-port must not rewrite stored in_ip, got %q want %q", got, "203.0.113.7")
	}
}

// TestPortClosureC01GostBindIPDoublePortDirect — PC-04 gost 面：
// intake 门（IsValidNodeAddress）直接拒双端口 bindIP；被拒后隧道下
// 无 forward_port 行可供 gost 消费；单端口对照经 HTTP 落库后 gost
// 服务地址合法。
func TestPortClosureC01GostBindIPDoublePortDirect(t *testing.T) {
	if err := IsValidNodeAddress("203.0.113.7:9999:22001"); err == nil {
		t.Errorf("CELL-G intake gate IsValidNodeAddress must reject double-port bindIP, got nil")
	}
	if err := IsValidNodeAddress("203.0.113.7:9999"); err != nil {
		t.Errorf("CELL-G intake gate must still admit single-port bindIP, got %v", err)
	}
	fx := setupR30FixF01(t, "198.51.100.77")
	out := pcFixAttemptForward(t, fx, "pc-c01-direct", "203.0.113.7:9999:22001", 30001)
	if out.Code == 0 {
		t.Fatalf("CELL-G forwardCreate accepted double-port inIp; gost bind path must never receive it")
	}
	var n int
	if err := fx.r.DB().Raw(`SELECT COUNT(*) FROM forward_port WHERE forward_id IN (SELECT id FROM forward WHERE tunnel_id = ?)`, fx.tunnelID).Scan(&n).Error; err != nil {
		t.Fatalf("count forward_port rows: %v", err)
	}
	if n != 0 {
		t.Errorf("CELL-G %d forward_port row(s) under tunnel after rejected create; want 0 (no double-port bindIP reaches gost)", n)
	}
	// 对照：单端口 inIp 落库后 gost 服务地址合法。
	id := fx.createForward(t, "pc-c01-direct-ctl", "203.0.113.7:9999", 30001)
	fx.checkDownstreamAddrs(t, id, 30001, "203.0.113.7:30001")
}

// TestPortClosureC03ListenAddrDoublePortF01Gap — PC-05 (P2, F-05):
// nodeCreate / nodeUpdate 的 TCP/UDPListenAddr 剥离后须过格式校验，
// 双端口值直接拒绝；单端口值剥离后落库、"[::]" 默认不变、未提交
// 字段保持存量（R32 F-01 intake 语义）。
func TestPortClosureC03ListenAddrDoublePortF01Gap(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/pc-c03.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	out := pcFixCreateNode(t, h, `{"name":"pc-c03-double","serverIp":"198.51.100.77","tcpListenAddr":"203.0.113.7:9999:8888","udpListenAddr":"203.0.113.7:9998","forwardMode":"agent"}`)
	if out.Code == 0 {
		t.Errorf("CELL-G nodeCreate accepted double-port TCPListenAddr (code 0); want rejection")
	}
	var n int
	if err := r.DB().Raw(`SELECT COUNT(*) FROM node WHERE name = ?`, "pc-c03-double").Scan(&n).Error; err != nil {
		t.Fatalf("count node: %v", err)
	}
	if n != 0 {
		t.Errorf("CELL-G double-port node row created (count=%d); want 0", n)
	}

	// 对照：单端口监听地址剥离后落库（R32 F-01 intake 语义）。
	out = pcFixCreateNode(t, h, `{"name":"pc-c03-single","serverIp":"198.51.100.77","tcpListenAddr":"203.0.113.7:9999","udpListenAddr":"203.0.113.7:9998","forwardMode":"agent"}`)
	if out.Code != 0 {
		t.Fatalf("control nodeCreate single-port listen addr failed: %+v", out)
	}
	_, _, _, tcp, udp, _ := pcFixNodeAddrs(t, r, "pc-c03-single")
	if tcp != "203.0.113.7" || udp != "203.0.113.7" {
		t.Errorf("control stored listen addrs = %q/%q, want 203.0.113.7/203.0.113.7", tcp, udp)
	}
	// 对照：默认 "[::]" 不变。
	out = pcFixCreateNode(t, h, `{"name":"pc-c03-def","serverIp":"198.51.100.77","forwardMode":"agent"}`)
	if out.Code != 0 {
		t.Fatalf("control nodeCreate default listen addr failed: %+v", out)
	}
	_, _, _, tcp, udp, _ = pcFixNodeAddrs(t, r, "pc-c03-def")
	if tcp != "[::]" || udp != "[::]" {
		t.Errorf("control default listen addrs = %q/%q, want [::]/[::]", tcp, udp)
	}
	// 对照：括号 v6 单端口剥离为裸 host。
	out = pcFixCreateNode(t, h, `{"name":"pc-c03-v6","serverIp":"198.51.100.77","tcpListenAddr":"[2001:db8::9]:9999","udpListenAddr":"[2001:db8::9]:9999","forwardMode":"agent"}`)
	if out.Code != 0 {
		t.Fatalf("control nodeCreate bracketed-v6 listen addr failed: %+v", out)
	}
	_, _, _, tcp, udp, _ = pcFixNodeAddrs(t, r, "pc-c03-v6")
	if tcp != "2001:db8::9" || udp != "2001:db8::9" {
		t.Errorf("control stored bracketed-v6 listen addrs = %q/%q, want 2001:db8::9/2001:db8::9", tcp, udp)
	}

	// nodeUpdate：双端口拒绝、存量不变；单端口剥离后落库。
	id := pcFixNodeID(t, r, "pc-c03-single")
	payload, _ := json.Marshal(map[string]interface{}{"id": id, "tcpListenAddr": "203.0.113.9:9999:8888"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/update", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.nodeUpdate(rec, req)
	var upd response.R
	if err := json.NewDecoder(rec.Body).Decode(&upd); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, rec.Body.String())
	}
	if upd.Code == 0 {
		t.Errorf("CELL-G nodeUpdate accepted double-port TCPListenAddr (code 0); want rejection")
	}
	_, _, _, tcp, _, _ = pcFixNodeAddrs(t, r, "pc-c03-single")
	if tcp != "203.0.113.7" {
		t.Errorf("CELL-G nodeUpdate double-port must not rewrite stored listen addr, got %q want %q", tcp, "203.0.113.7")
	}
	payload, _ = json.Marshal(map[string]interface{}{"id": id, "tcpListenAddr": "203.0.113.9:9999", "udpListenAddr": "[2001:db8::9]:9999"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/node/update", bytes.NewReader(payload))
	rec = httptest.NewRecorder()
	h.nodeUpdate(rec, req)
	if err := json.NewDecoder(rec.Body).Decode(&upd); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, rec.Body.String())
	}
	if upd.Code != 0 {
		t.Fatalf("control nodeUpdate single-port listen addr failed: %+v", upd)
	}
	_, _, _, tcp, udp, _ = pcFixNodeAddrs(t, r, "pc-c03-single")
	if tcp != "203.0.113.9" || udp != "2001:db8::9" {
		t.Errorf("control updated listen addrs = %q/%q, want 203.0.113.9/2001:db8::9", tcp, udp)
	}
}

// TestPortClosureC04NodeV4DoublePortDial — PC-07(i) (P3, F-07):
// nodeCreate / nodeUpdate 的 serverIpV4/serverIpV6 复用
// IsValidNodeAddress，双端口即拒；单端口剥离后落库，拨号取址合法。
func TestPortClosureC04NodeV4DoublePortDial(t *testing.T) {
	if err := IsValidNodeAddress("203.0.113.7:9999:22001"); err == nil {
		t.Errorf("CELL-D gate IsValidNodeAddress must reject double-port v4, got nil")
	}
	r, err := repo.Open(t.TempDir() + "/pc-c04.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	out := pcFixCreateNode(t, h, `{"name":"pc-c04-double","serverIp":"198.51.100.77","serverIpV4":"203.0.113.7:9999:22001","forwardMode":"agent"}`)
	if out.Code == 0 {
		t.Errorf("CELL-D nodeCreate accepted double-port serverIpV4 (code 0); want rejection")
	}
	var n int
	if err := r.DB().Raw(`SELECT COUNT(*) FROM node WHERE name = ?`, "pc-c04-double").Scan(&n).Error; err != nil {
		t.Fatalf("count node: %v", err)
	}
	if n != 0 {
		t.Errorf("CELL-D double-port node row created (count=%d); want 0", n)
	}
	// 对照：单端口 V4 剥离后落库，拨号取址合法。
	out = pcFixCreateNode(t, h, `{"name":"pc-c04-single","serverIp":"198.51.100.77","serverIpV4":"203.0.113.7:9999","forwardMode":"agent"}`)
	if out.Code != 0 {
		t.Fatalf("control nodeCreate single-port serverIpV4 failed: %+v", out)
	}
	_, v4, _, _, _, _ := pcFixNodeAddrs(t, r, "pc-c04-single")
	if v4 != "203.0.113.7" {
		t.Fatalf("control stored serverIpV4 = %q, want %q", v4, "203.0.113.7")
	}
	host := pickNodeAddressV4(&nodeRecord{ServerIP: "198.51.100.77", ServerIPv4: v4})
	if host != "203.0.113.7" {
		t.Errorf("CELL-D pickNodeAddressV4(stored) = %q, want %q", host, "203.0.113.7")
	}
	if joined := net.JoinHostPort(host, "22001"); joined != "203.0.113.7:22001" {
		t.Errorf("CELL-D dial target = %q, want %q", joined, "203.0.113.7:22001")
	}
	// nodeUpdate：双端口 V4 拒绝、存量不变。
	id := pcFixNodeID(t, r, "pc-c04-single")
	payload, _ := json.Marshal(map[string]interface{}{"id": id, "serverIpV4": "203.0.113.7:9999:22001"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/update", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.nodeUpdate(rec, req)
	var upd response.R
	if err := json.NewDecoder(rec.Body).Decode(&upd); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, rec.Body.String())
	}
	if upd.Code == 0 {
		t.Errorf("CELL-D nodeUpdate accepted double-port serverIpV4 (code 0); want rejection")
	}
	_, v4, _, _, _, _ = pcFixNodeAddrs(t, r, "pc-c04-single")
	if v4 != "203.0.113.7" {
		t.Errorf("CELL-D nodeUpdate double-port must not rewrite stored v4, got %q want %q", v4, "203.0.113.7")
	}
}

// TestPortClosureC04NodeShareIPDoublePortSkipped — PC-07(i) V6 面：
// serverIpV6 双端口即拒；单端口剥离后落库，订阅直连端点不再跳过。
func TestPortClosureC04NodeShareIPDoublePortSkipped(t *testing.T) {
	if err := IsValidNodeAddress("2001:db8::1:9999:22001"); err == nil {
		t.Errorf("CELL-D gate IsValidNodeAddress must reject double-port v6, got nil")
	}
	r, err := repo.Open(t.TempDir() + "/pc-c04b.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	out := pcFixCreateNode(t, h, `{"name":"pc-c04b-double","serverIp":"198.51.100.77","serverIpV6":"2001:db8::1:9999:22001","forwardMode":"agent"}`)
	if out.Code == 0 {
		t.Errorf("CELL-D nodeCreate accepted double-port serverIpV6 (code 0); want rejection")
	}
	var n int
	if err := r.DB().Raw(`SELECT COUNT(*) FROM node WHERE name = ?`, "pc-c04b-double").Scan(&n).Error; err != nil {
		t.Fatalf("count node: %v", err)
	}
	if n != 0 {
		t.Errorf("CELL-D double-port node row created (count=%d); want 0", n)
	}
	// 对照：单端口 V6 剥离后落库，nodeShareIP 不再跳过。
	out = pcFixCreateNode(t, h, `{"name":"pc-c04b-single","serverIp":"198.51.100.77","serverIpV6":"[2001:db8::1]:9999","forwardMode":"agent"}`)
	if out.Code != 0 {
		t.Fatalf("control nodeCreate single-port serverIpV6 failed: %+v", out)
	}
	_, _, v6, _, _, _ := pcFixNodeAddrs(t, r, "pc-c04b-single")
	if v6 != "2001:db8::1" {
		t.Fatalf("control stored serverIpV6 = %q, want %q", v6, "2001:db8::1")
	}
	share := nodeShareIP(&model.Node{
		ServerIP: "198.51.100.77",
		ServerIPV6: sql.NullString{
			String: v6,
			Valid:  true,
		},
	}, true)
	if share != "2001:db8::1" {
		t.Errorf("CELL-D nodeShareIP(stored v6) = %q, want %q (must not be silently skipped)", share, "2001:db8::1")
	}
}

// TestPortClosureC05ExtraIPsStrippedOnCreateAndUpdate — B-NEW-01
// （观察级顺手收口）：nodeCreate / nodeUpdate 的 extraIPs 按逗号
// 分项剥离 :port 后落库。
func TestPortClosureC05ExtraIPsStrippedOnCreateAndUpdate(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/pc-c05.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	out := pcFixCreateNode(t, h, `{"name":"pc-c05","serverIp":"198.51.100.77","extraIPs":"198.51.100.77:8443,2001:db8::1","forwardMode":"agent"}`)
	if out.Code != 0 {
		t.Fatalf("nodeCreate extraIPs failed: %+v", out)
	}
	_, _, _, _, _, extra := pcFixNodeAddrs(t, r, "pc-c05")
	if extra != "198.51.100.77,2001:db8::1" {
		t.Errorf("CELL-E stored extraIPs = %q, want %q", extra, "198.51.100.77,2001:db8::1")
	}
	id := pcFixNodeID(t, r, "pc-c05")
	payload, _ := json.Marshal(map[string]interface{}{"id": id, "extraIPs": "203.0.113.9:9443"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/update", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.nodeUpdate(rec, req)
	var upd response.R
	if err := json.NewDecoder(rec.Body).Decode(&upd); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, rec.Body.String())
	}
	if upd.Code != 0 {
		t.Fatalf("nodeUpdate extraIPs failed: %+v", upd)
	}
	_, _, _, _, _, extra = pcFixNodeAddrs(t, r, "pc-c05")
	if extra != "203.0.113.9" {
		t.Errorf("CELL-E updated extraIPs = %q, want %q", extra, "203.0.113.9")
	}
}

// TestPortClosureC06PanelInstallAddrValidation — O-C-01（观察级顺手
// 收口）：面板地址（网站配置 ip）在 nodeInstall 生成安装命令前须
// 过格式校验——单 host:port 放行，双端口拒绝。normalize 侧无改动，
// 红态证据为 scratch（normalizeNodeInstallAddr 对双端口值原样产出
// 畸形 ws:// 地址），见 lane 报告。
func TestPortClosureC06PanelInstallAddrValidation(t *testing.T) {
	valid := []string{
		"16.146.199.68:6366",
		"16.146.199.68",
		"example.com:6366",
		"https://example.com:6366",
		"http://16.146.199.68:6366",
		"[2001:db8::1]:6366",
		"2001:db8::1",
	}
	for _, v := range valid {
		if err := validatePanelInstallAddr(v); err != nil {
			t.Errorf("CELL-O validatePanelInstallAddr(%q) = %v, want nil", v, err)
		}
	}
	invalid := []string{
		"16.146.199.68:6366:6366",
		"example.com:6366:6366",
		"[2001:db8::1]:9999:22001",
		"https://example.com:6366:6366",
	}
	for _, v := range invalid {
		if err := validatePanelInstallAddr(v); err == nil {
			t.Errorf("CELL-O validatePanelInstallAddr(%q) = nil, want error (double-port panel addr)", v)
		}
	}
}

// TestPortClosureC07ConnectIPDoublePortRejected — ConnectIP 顺手收口：
// validateTunnelConnectIPConstraints 对出/链节点的 connectIp 追加
// 格式校验，双端口即拒；单端口/裸值/空值放行（存储仍保持原值，
// R30 F-02 口径；此处只做校验、不剥离）。
func TestPortClosureC07ConnectIPDoublePortRejected(t *testing.T) {
	cases := []struct {
		name string
		req  map[string]interface{}
		want bool // wantErr
	}{
		{"out double-port", map[string]interface{}{"outNodeId": []interface{}{map[string]interface{}{"nodeId": 1, "connectIp": "198.51.100.77:8443:22001"}}}, true},
		{"chain double-port", map[string]interface{}{"chainNodes": []interface{}{[]interface{}{map[string]interface{}{"nodeId": 2, "connectIp": "198.51.100.77:8443:22001"}}}}, true},
		{"out single-port", map[string]interface{}{"outNodeId": []interface{}{map[string]interface{}{"nodeId": 1, "connectIp": "198.51.100.77:8443"}}}, false},
		{"out bare", map[string]interface{}{"outNodeId": []interface{}{map[string]interface{}{"nodeId": 1, "connectIp": "198.51.100.77"}}}, false},
		{"out bare v6", map[string]interface{}{"outNodeId": []interface{}{map[string]interface{}{"nodeId": 1, "connectIp": "2001:db8::1"}}}, false},
		{"out bracketed v6 single-port", map[string]interface{}{"outNodeId": []interface{}{map[string]interface{}{"nodeId": 1, "connectIp": "[2001:db8::1]:8443"}}}, false},
		{"out empty", map[string]interface{}{"outNodeId": []interface{}{map[string]interface{}{"nodeId": 1, "connectIp": ""}}}, false},
		{"no connectIp key", map[string]interface{}{"outNodeId": []interface{}{map[string]interface{}{"nodeId": 1}}}, false},
	}
	for _, tc := range cases {
		err := validateTunnelConnectIPConstraints(tc.req)
		if tc.want && err == nil {
			t.Errorf("CELL-T %s: want error, got nil", tc.name)
		}
		if !tc.want && err != nil {
			t.Errorf("CELL-T %s: want nil, got %v", tc.name, err)
		}
	}
}
