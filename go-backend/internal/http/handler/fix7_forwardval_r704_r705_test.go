package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// 修复轮 7 路 forwardval：R7-04（G-F3-01，低）＋R7-05（G-F3-02，低）。
//
// R7-04：目标地址「零有效目标」真空通过——IsSafeRemoteAddr 经
// splitRemoteParts 丢弃空段后逐段校验，纯分隔符目标（如 ",,,"）段集
// 为空集、循环零次、返回 nil，普通用户（开关关）、管理员、update
// 三面均可落库零目标转发。裁定 3：在校验本体拒绝空段集，管理员面
// 同样拒绝（零目标属格式无效、不在 netguard 管理员豁免范围）。
//
// R7-05：forwardUpdate 对缺席 remoteAddr 回填存量值后无条件复检
// netguard——存量内网目标规则在开关关闭后被所有者冻结编辑（仅改名
// 也 403），与 forward.md「已保存的规则不受开关变化影响」冲突。
// 裁定 2：仅在目标实际变更时复检；变更判定以切分后的段集为准
// （顺序/空段/空白不构成变更），提交新目标照常全检。
//
// 基线（afdade1e）红点：成员开关关建 ",,," → code=0 落库；管理员
// 建 ",,," → code=0；B6a（关＋仅改名）→ 403；update 改 ",,," →
// code=0 且存量目标被改写。

type fix7ForwardValFixture struct {
	h        *Handler
	r        *repo.Repository
	nodeID   int64
	tunnelID int64
}

func setupFix7ForwardVal(t *testing.T) *fix7ForwardValFixture {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/fix7-forwardval.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	fx := &fix7ForwardValFixture{h: New(r, "test-secret"), r: r}
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES('f7v-node', 'f7v-secret', '198.51.100.21', '198.51.100.21', '', '10000-20000', '', 'v1', 0, 0, 0, ?, ?, 1, '[::]', '[::]', 0)`, now, now).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = 'f7v-node'`).Scan(&fx.nodeID).Error; err != nil {
		t.Fatalf("lookup node: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx)
		VALUES('f7v-tunnel', 1.0, 2, 'tcp', 99999, ?, ?, 1, NULL, 0)`, now, now).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := r.DB().Raw(`SELECT id FROM tunnel WHERE name = 'f7v-tunnel'`).Scan(&fx.tunnelID).Error; err != nil {
		t.Fatalf("lookup tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol) VALUES(?, 1, ?, NULL, 'round', 1, 'tcp')`, fx.tunnelID, fx.nodeID).Error; err != nil {
		t.Fatalf("seed chain_tunnel: %v", err)
	}
	// 普通用户 user2（role_id=1）。
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(2, 'member', 'pwd', 1, 2727251700000, 99999, 0, 0, 1, 99999, ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("seed member user: %v", err)
	}
	for _, uid := range []int64{1, 2} {
		if err := r.DB().Exec(`
			INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
			VALUES(?, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)`, uid, fx.tunnelID).Error; err != nil {
			t.Fatalf("seed user_tunnel user=%d: %v", uid, err)
		}
	}
	return fx
}

func (fx *fix7ForwardValFixture) setSwitch(t *testing.T, on bool) {
	t.Helper()
	v := "false"
	if on {
		v = "true"
	}
	if err := fx.r.UpsertConfig(allowLocalRemoteAddrConfigKey, v, time.Now().UnixMilli()); err != nil {
		t.Fatalf("set allow_local_remote_addr=%s: %v", v, err)
	}
}

func (fx *fix7ForwardValFixture) callCreate(t *testing.T, userID int64, userName string, roleID int, payload map[string]interface{}) (int, string) {
	t.Helper()
	res := httptest.NewRecorder()
	fx.h.forwardCreate(res, newAuthenticatedJSONRequestAs(t, userID, userName, roleID, payload))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode forwardCreate response: %v body=%s", err, res.Body.String())
	}
	return out.Code, out.Msg
}

func (fx *fix7ForwardValFixture) callUpdate(t *testing.T, userID int64, userName string, roleID int, payload map[string]interface{}) (int, string) {
	t.Helper()
	res := httptest.NewRecorder()
	fx.h.forwardUpdate(res, newAuthenticatedJSONRequestAs(t, userID, userName, roleID, payload))
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode forwardUpdate response: %v body=%s", err, res.Body.String())
	}
	return out.Code, out.Msg
}

func (fx *fix7ForwardValFixture) seedMemberForward(t *testing.T, name, remoteAddr string, port int) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	fwd := model.Forward{UserID: 2, UserName: "member", Name: name, TunnelID: fx.tunnelID, RemoteAddr: remoteAddr, CreatedTime: now, UpdatedTime: now, Status: 1}
	if err := fx.r.DB().Create(&fwd).Error; err != nil {
		t.Fatalf("seed forward %s: %v", name, err)
	}
	if err := fx.r.DB().Create(&model.ForwardPort{ForwardID: fwd.ID, NodeID: fx.nodeID, Port: port}).Error; err != nil {
		t.Fatalf("seed forward_port %s: %v", name, err)
	}
	return fwd.ID
}

func (fx *fix7ForwardValFixture) forwardCountNamed(t *testing.T, name string) int {
	t.Helper()
	return mustQueryInt(t, fx.r, `SELECT COUNT(*) FROM forward WHERE name = ?`, name)
}

func (fx *fix7ForwardValFixture) storedRemoteAddr(t *testing.T, id int64) string {
	t.Helper()
	fwd, err := fx.h.getForwardRecord(id)
	if err != nil {
		t.Fatalf("get forward %d: %v", id, err)
	}
	return fwd.RemoteAddr
}

// ---- R7-04 校验本体（单元级） ----

func TestFix7R704IsSafeRemoteAddrRejectsEmptyPartSet(t *testing.T) {
	old := DisableSafeRemoteAddrCheckForTesting
	DisableSafeRemoteAddrCheckForTesting = false
	defer func() { DisableSafeRemoteAddrCheckForTesting = old }()

	for _, addr := range []string{"", ",,,", " , , ", "\n,\r\n,", ",", "  "} {
		err := IsSafeRemoteAddr(addr)
		if err == nil {
			t.Errorf("IsSafeRemoteAddr(%q) must reject empty part set, got nil", addr)
		} else if !strings.Contains(err.Error(), "目标地址不能为空") {
			t.Errorf("IsSafeRemoteAddr(%q) error must be the empty-target family, got %v", addr, err)
		}
	}
	// 既有合法形态逐个回归（不得误伤）：单段、多段、空段混排、
	// 首尾逗号、换行分隔、段间空格。
	for _, addr := range []string{
		"203.0.113.5:443",
		"203.0.113.5:443,198.51.100.7:443",
		"203.0.113.5:443,,198.51.100.7:443",
		"203.0.113.5:443,",
		",203.0.113.5:443",
		"203.0.113.5:443\n198.51.100.7:443",
		" 203.0.113.5:443 , 198.51.100.7:443 ",
	} {
		if err := IsSafeRemoteAddr(addr); err != nil {
			t.Errorf("IsSafeRemoteAddr(%q) must pass, got %v", addr, err)
		}
	}
	// 内网段仍按原口径拒且点名解析结果（本体改动不改变段校验）。
	err := IsSafeRemoteAddr("10.0.0.5:443")
	if err == nil || !strings.Contains(err.Error(), "resolves to internal IP") {
		t.Errorf("IsSafeRemoteAddr(10.0.0.5:443) must keep internal-IP rejection, got %v", err)
	}
}

// ---- R7-04 创建面（三身份/开关形态） ----

func TestFix7R704ForwardCreateRejectsSeparatorOnlyTarget(t *testing.T) {
	fx := setupFix7ForwardVal(t)
	fx.setSwitch(t, false)

	// 普通用户、开关关：基线 code=0 落库，修复后必须拒且不落库。
	code, msg := fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f7v-sep-member", "remoteAddr": ",,,", "inPort": 15001,
	})
	if code == 0 {
		t.Fatalf("member create remoteAddr=\",,,\" must be rejected, got success")
	}
	if !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("rejection msg must be the empty-target family, got %q", msg)
	}
	if n := fx.forwardCountNamed(t, "f7v-sep-member"); n != 0 {
		t.Fatalf("rejected create leaked a forward row: %d", n)
	}

	// 管理员面同样拒绝（零目标属格式无效、不在管理员豁免范围）。
	code, msg = fx.callCreate(t, 1, "admin", 0, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f7v-sep-admin", "remoteAddr": ",,,", "inPort": 15002,
	})
	if code == 0 {
		t.Fatalf("admin create remoteAddr=\",,,\" must be rejected, got success")
	}
	if !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("admin rejection msg must be the empty-target family, got %q", msg)
	}
	if n := fx.forwardCountNamed(t, "f7v-sep-admin"); n != 0 {
		t.Fatalf("rejected admin create leaked a forward row: %d", n)
	}

	// 开关开启时同样拒绝（格式判与 netguard 开关无关）。
	fx.setSwitch(t, true)
	code, msg = fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f7v-sep-on", "remoteAddr": " , ,", "inPort": 15003,
	})
	if code == 0 {
		t.Fatalf("member create separator-only target with switch on must be rejected, got success")
	}
	if !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("switch-on rejection msg must be the empty-target family, got %q", msg)
	}
	fx.setSwitch(t, false)

	// 对照：合法公网单段与空段混排多段照常创建（无误伤）。
	code, msg = fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f7v-ok-single", "remoteAddr": "203.0.113.5:443", "inPort": 15004,
	})
	if code != 0 {
		t.Fatalf("member create valid public target must succeed, got code=%d msg=%s", code, msg)
	}
	code, msg = fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f7v-ok-multi", "remoteAddr": "203.0.113.5:443,,198.51.100.7:443", "inPort": 15005,
	})
	if code != 0 {
		t.Fatalf("member create multi-target with empty segments must succeed, got code=%d msg=%s", code, msg)
	}
	// 对照：内网目标仍走 netguard 403（本体改动不放宽段校验）。
	code, msg = fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f7v-internal", "remoteAddr": "10.0.0.5:443", "inPort": 15006,
	})
	if code != 403 || !strings.Contains(msg, "resolves to internal IP") {
		t.Fatalf("member create internal target must stay 403 internal-IP, got code=%d msg=%s", code, msg)
	}
}

// ---- R7-05 更新面 B6a–B6d 翻转＋补格 ----

func TestFix7R705ForwardUpdateRechecksOnlyOnTargetChange(t *testing.T) {
	fx := setupFix7ForwardVal(t)

	// B6a：开关关＋所有者仅改名（remoteAddr 缺席回填存量内网目标）。
	// 基线 403（存量复检冻结），修复后 code=0。
	fx.setSwitch(t, false)
	idA := fx.seedMemberForward(t, "f7v-b6a", "10.9.9.9:443", 15101)
	code, msg := fx.callUpdate(t, 2, "member", 1, map[string]interface{}{"id": idA, "name": "f7v-b6a-renamed"})
	if code != 0 {
		t.Fatalf("B6a rename-only update with switch off must succeed after fix, got code=%d msg=%s", code, msg)
	}
	if got := fx.storedRemoteAddr(t, idA); got != "10.9.9.9:443" {
		t.Fatalf("B6a stored target must stay 10.9.9.9:443, got %q", got)
	}

	// 补格：开关关＋提交与存量逐字相同的目标＋改名 → 按未变更处理。
	idSame := fx.seedMemberForward(t, "f7v-same", "10.9.9.9:443", 15102)
	code, msg = fx.callUpdate(t, 2, "member", 1, map[string]interface{}{"id": idSame, "name": "f7v-same-renamed", "remoteAddr": "10.9.9.9:443"})
	if code != 0 {
		t.Fatalf("verbatim-identical target resubmission must be treated as unchanged, got code=%d msg=%s", code, msg)
	}

	// 变更格：开关关＋所有者把目标改成另一内网地址 → 照常全检 403、
	// 不落库。
	idChg := fx.seedMemberForward(t, "f7v-chg", "10.9.9.9:443", 15103)
	code, msg = fx.callUpdate(t, 2, "member", 1, map[string]interface{}{"id": idChg, "remoteAddr": "10.9.9.10:443"})
	if code != 403 || !strings.Contains(msg, "resolves to internal IP") {
		t.Fatalf("changed-to-internal target must stay 403 internal-IP, got code=%d msg=%s", code, msg)
	}
	if got := fx.storedRemoteAddr(t, idChg); got != "10.9.9.9:443" {
		t.Fatalf("rejected target change must not persist, stored = %q", got)
	}

	// B6d：开关关＋所有者把目标改成公网 → code=0 且落库新目标。
	idPub := fx.seedMemberForward(t, "f7v-b6d", "10.9.9.9:443", 15104)
	code, msg = fx.callUpdate(t, 2, "member", 1, map[string]interface{}{"id": idPub, "remoteAddr": "203.0.113.5:443"})
	if code != 0 {
		t.Fatalf("B6d change-to-public update must succeed, got code=%d msg=%s", code, msg)
	}
	if got := fx.storedRemoteAddr(t, idPub); got != "203.0.113.5:443" {
		t.Fatalf("B6d stored target must be updated to public, got %q", got)
	}

	// B6b：开关开＋仅改名 → code=0（对照，基线即 0）。
	fx.setSwitch(t, true)
	idB := fx.seedMemberForward(t, "f7v-b6b", "10.9.9.9:443", 15105)
	code, msg = fx.callUpdate(t, 2, "member", 1, map[string]interface{}{"id": idB, "name": "f7v-b6b-renamed"})
	if code != 0 {
		t.Fatalf("B6b rename-only update with switch on must succeed, got code=%d msg=%s", code, msg)
	}

	// B6c：开关关＋管理员改名同一条 → code=0（管理员面不变）。
	fx.setSwitch(t, false)
	idC := fx.seedMemberForward(t, "f7v-b6c", "10.9.9.9:443", 15106)
	code, msg = fx.callUpdate(t, 1, "admin", 0, map[string]interface{}{"id": idC, "name": "f7v-b6c-renamed"})
	if code != 0 {
		t.Fatalf("B6c admin rename must succeed, got code=%d msg=%s", code, msg)
	}
}

// ---- R7-04 × R7-05 交互：update 改纯分隔符必须被拒 ----

func TestFix7R704ForwardUpdateRejectsSeparatorOnlyTarget(t *testing.T) {
	fx := setupFix7ForwardVal(t)
	fx.setSwitch(t, false)

	// 存量公网目标改成 ",,,"：段集为空与现值段集不同，属实际变更，
	// 且零目标格式无效——不得被「未变更不复检」误判放行。
	id := fx.seedMemberForward(t, "f7v-upd-sep", "203.0.113.5:443", 15201)
	code, msg := fx.callUpdate(t, 2, "member", 1, map[string]interface{}{"id": id, "remoteAddr": ",,,"})
	if code == 0 {
		t.Fatalf("update to separator-only target must be rejected, got success")
	}
	if !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("update rejection msg must be the empty-target family, got %q", msg)
	}
	if got := fx.storedRemoteAddr(t, id); got != "203.0.113.5:443" {
		t.Fatalf("rejected separator-only update must not persist, stored = %q", got)
	}

	// 管理员面同拒。
	code, msg = fx.callUpdate(t, 1, "admin", 0, map[string]interface{}{"id": id, "remoteAddr": " , ,"})
	if code == 0 {
		t.Fatalf("admin update to separator-only target must be rejected, got success")
	}
	if !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("admin update rejection msg must be the empty-target family, got %q", msg)
	}
	if got := fx.storedRemoteAddr(t, id); got != "203.0.113.5:443" {
		t.Fatalf("rejected admin update must not persist, stored = %q", got)
	}
}

// ---- 变更判定谓词（段集口径）单元 ----

func TestFix7R705RemoteAddrTargetsChangedPredicate(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"10.9.9.9:443", "10.9.9.9:443", false},   // 逐字相同
		{"10.9.9.9:443", " 10.9.9.9:443 ", false}, // 空白差不构成变更
		{"a:1,b:2", "b:2,a:1", false},             // 顺序不构成变更
		{"a:1,,b:2", "a:1,b:2", false},            // 空段不构成变更
		{"a:1", "a:2", true},
		{"a:1", "a:1,b:2", true},
		{",,,", "a:1", true}, // 空段集与非空段集必不同（R7-04 先行拒绝，不依赖本谓词兜底）
	}
	for _, c := range cases {
		if got := remoteAddrTargetsChanged(c.a, c.b); got != c.want {
			t.Errorf("remoteAddrTargetsChanged(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
