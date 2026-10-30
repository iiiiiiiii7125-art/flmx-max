package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// B13 联邦三条回归（Round 1）：
//   B13-001 写面校验与传输层私网放行策略同口径；
//   B13-002 apply 失败回滚 + 从未成功下发的 runtime 释放/删 share 不被
//           离线节点永久卡死；
//   B13-003 纯预留 TTL 惰性回收 + 过期预留不得 apply。
// 提供方侧均以「wsServer 存在但无 agent 连接」模拟节点离线。

type b13Envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func b13Call(t *testing.T, fn http.HandlerFunc, path, token, body string) b13Envelope {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	fn(res, req)
	var env b13Envelope
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response for %s: %v (body=%s)", path, err, res.Body.String())
	}
	return env
}

// b13Provider 离线提供方夹具：节点行存在、share 已建，wsServer 无连接。
func b13Provider(t *testing.T, portStart, portEnd int) (*Handler, *repo.Repository, *repo.PeerShare) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "b13.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.DB().Create(&model.Node{ID: 1, Name: "b13-provider-node", Secret: "b13-node-secret", ServerIP: "203.0.113.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	h := &Handler{repo: r, wsServer: ws.NewServer(r, "test-jwt-secret")}
	now := time.Now().UnixMilli()
	share := &repo.PeerShare{Name: "b13-share", NodeID: 1, Token: "b13-share-token", PortRangeStart: portStart, PortRangeEnd: portEnd, IsActive: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.CreatePeerShare(share); err != nil {
		t.Fatalf("create share: %v", err)
	}
	return h, r, share
}

func b13Reserve(t *testing.T, h *Handler, resourceKey string) b13Envelope {
	t.Helper()
	return b13Call(t, h.federationRuntimeReservePort, "/api/v1/federation/runtime/reserve-port", "b13-share-token", `{"resourceKey":"`+resourceKey+`"}`)
}

func b13ReservationID(t *testing.T, env b13Envelope) string {
	t.Helper()
	var data struct {
		ReservationID string `json:"reservationId"`
		AllocatedPort int    `json:"allocatedPort"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode reserve data: %v", err)
	}
	return data.ReservationID
}

// ---- B13-001 ----

func TestB13RemoteNodeURLFollowsFetchPrivatePolicy(t *testing.T) {
	// 未开启放行：私网/CGNAT/回环与元数据一律拒，公网放行（与旧口径
	// 在该状态下一致）。
	clearFetchEnv(t)
	for _, bad := range []string{
		"http://127.0.0.1:6365",
		"http://192.168.1.10:6365",
		"http://10.0.0.5:6365",
		"http://100.64.1.5:6365", // Tailscale CGNAT 段
		"http://169.254.169.254:6365",
	} {
		if err := validateRemoteNodeURL(bad); err == nil {
			t.Fatalf("validateRemoteNodeURL(%q) must be rejected with private policy off", bad)
		}
	}
	if err := validateRemoteNodeURL("http://203.0.113.10:6365"); err != nil {
		t.Fatalf("public remote URL must pass: %v", err)
	}

	// 开启全局放行：内网/Tailscale 联邦写面放行；硬拒段（云元数据）
	// 恒拒，不受放行策略影响。
	allowFetchEnv(t)
	for _, ok := range []string{
		"http://127.0.0.1:6365",
		"http://192.168.1.10:6365",
		"http://100.64.1.5:6365",
		"http://203.0.113.10:6365",
	} {
		if err := validateRemoteNodeURL(ok); err != nil {
			t.Fatalf("validateRemoteNodeURL(%q) must pass with private policy on: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://169.254.169.254:6365",
		"http://169.254.0.23:6365",
		"http://[fd00:ec2::254]:6365",
	} {
		if err := validateRemoteNodeURL(bad); err == nil {
			t.Fatalf("validateRemoteNodeURL(%q) must stay hard-denied even with private policy on", bad)
		}
	}

	// allowlist 口径：仅命中 CIDR 的私网放行，未命中仍拒；allowlist
	// 不得放行硬拒段。
	clearFetchEnv(t)
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "10.0.0.0/8")
	if err := validateRemoteNodeURL("http://10.0.0.5:6365"); err != nil {
		t.Fatalf("allowlisted private URL must pass: %v", err)
	}
	if err := validateRemoteNodeURL("http://192.168.1.10:6365"); err == nil {
		t.Fatal("non-allowlisted private URL must be rejected")
	}
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "169.254.0.0/16")
	if err := validateRemoteNodeURL("http://169.254.169.254:6365"); err == nil {
		t.Fatal("allowlist must not rescue hard-denied metadata address")
	}
}

// nodeCreate 直填与 import 共用同一校验：开启放行后内网远端节点可建，
// 未开启时仍拒（与 federation_fix132 的 D3-02 用例互补）。
func TestB13NodeCreateRemoteURLWithPrivatePolicyOn(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "b13-create.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-jwt-secret")

	create := func(remoteURL string) b13Envelope {
		body, _ := json.Marshal(map[string]interface{}{
			"name": "b13-remote-node", "serverIp": "203.0.113.20",
			"isRemote": 1, "remoteUrl": remoteURL, "remoteToken": "peer-token",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/node/create", strings.NewReader(string(body)))
		res := httptest.NewRecorder()
		h.nodeCreate(res, req)
		var env b13Envelope
		if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
		return env
	}

	clearFetchEnv(t)
	if env := create("http://127.0.0.1:6399"); env.Code == 0 {
		t.Fatal("private remoteUrl must be rejected while policy off")
	}
	allowFetchEnv(t)
	if env := create("http://127.0.0.1:6399"); env.Code != 0 {
		t.Fatalf("private remoteUrl must be accepted once policy on, got code=%d msg=%q", env.Code, env.Msg)
	}
	if count := mustQueryInt(t, r, `SELECT COUNT(1) FROM node WHERE name = 'b13-remote-node'`); count != 1 {
		t.Fatalf("expected exactly one persisted remote node, found %d", count)
	}
}

// ---- B13-002 ----

// 离线节点 apply 失败：runtime 必须回滚为纯预留（可换角色重试、可
// 本地释放），不得停在 role 已占用的卡死终态。
func TestB13ApplyFailureRollsBackReservation(t *testing.T) {
	h, r, share := b13Provider(t, 31000, 31002)

	env := b13Reserve(t, h, "rkA")
	if env.Code != 0 {
		t.Fatalf("reserve failed: %+v", env)
	}
	reservationID := b13ReservationID(t, env)

	apply := func(role string) b13Envelope {
		return b13Call(t, h.federationRuntimeApplyRole, "/api/v1/federation/runtime/apply-role", "b13-share-token",
			`{"reservationId":"`+reservationID+`","role":"`+role+`","protocol":"tls"}`)
	}
	if env := apply("exit"); env.Code == 0 {
		t.Fatal("apply against offline node must fail")
	}
	stored, err := r.GetPeerShareRuntimeByReservationID(share.ID, reservationID)
	if err != nil || stored == nil {
		t.Fatalf("load runtime: %v", err)
	}
	if stored.Status != 1 || stored.Role != "" || stored.ServiceName != "" || stored.ChainName != "" || stored.Applied != 0 {
		t.Fatalf("failed apply must roll back to a plain reservation, got %+v", stored)
	}

	// 换角色重试不得被 role-cannot-change 挡住（仍会因离线失败，但
	// 失败原因必须回到节点不可达本身）。
	if env := apply("middle"); env.Code == 0 || strings.Contains(env.Msg, "cannot change") {
		t.Fatalf("role change after rollback must not hit role-cannot-change, got %+v", env)
	}

	// 本地释放不再依赖节点在线。
	env = b13Call(t, h.federationRuntimeReleaseRole, "/api/v1/federation/runtime/release-role", "b13-share-token", `{"resourceKey":"rkA"}`)
	if env.Code != 0 {
		t.Fatalf("release after rolled-back apply must complete locally, got %+v", env)
	}
	stored, err = r.GetPeerShareRuntimeByReservationID(share.ID, reservationID)
	if err != nil || stored == nil || stored.Status != 0 {
		t.Fatalf("runtime must be released, got %+v err=%v", stored, err)
	}
}

// LB-REQ-05：回滚快照须覆盖 apply 路径改写并落库的全部字段。
// protocol/strategy/binding_id 漏回滚时，失败尝试的请求参数会残留
// 在行里：重试不带 protocol 经 defaultString(req.Protocol,
// runtime.Protocol) 继承残留值并下发——一个从未在成功请求里出现
// 过的值生效。构造：纯预留（reserve 默认 protocol=tls、
// strategy=round、binding_id 空）→ 携带 udp/fifo 的 apply 离线失败
// → 三列必须恢复为申请前值 → 不带 protocol/strategy 重试（仍离线
// 失败）后三列仍为申请前值，证明重试继承的是恢复后的预留原值。
func TestLBReq05ApplyFailureRollsBackProtocolStrategyBinding(t *testing.T) {
	h, r, share := b13Provider(t, 31000, 31002)

	env := b13Reserve(t, h, "rkLB")
	if env.Code != 0 {
		t.Fatalf("reserve failed: %+v", env)
	}
	reservationID := b13ReservationID(t, env)

	load := func() *repo.PeerShareRuntime {
		stored, err := r.GetPeerShareRuntimeByReservationID(share.ID, reservationID)
		if err != nil || stored == nil {
			t.Fatalf("load runtime: %v", err)
		}
		return stored
	}
	before := load()
	if before.Protocol != "tls" || before.Strategy != "round" || before.BindingID != "" {
		t.Fatalf("plain reservation precondition: protocol=tls strategy=round binding empty, got %+v", before)
	}

	apply := func(body string) b13Envelope {
		return b13Call(t, h.federationRuntimeApplyRole, "/api/v1/federation/runtime/apply-role", "b13-share-token", body)
	}
	if env := apply(`{"reservationId":"` + reservationID + `","role":"exit","protocol":"udp","strategy":"fifo"}`); env.Code == 0 {
		t.Fatal("apply against offline node must fail")
	}
	stored := load()
	if stored.Role != "" || stored.ServiceName != "" || stored.ChainName != "" || stored.Applied != 0 {
		t.Fatalf("failed apply must roll back occupation fields, got %+v", stored)
	}
	if stored.Protocol != before.Protocol || stored.Strategy != before.Strategy || stored.BindingID != before.BindingID {
		t.Fatalf("failed apply must roll back protocol/strategy/binding_id to pre-apply values (tls/round/empty), got protocol=%q strategy=%q binding=%q",
			stored.Protocol, stored.Strategy, stored.BindingID)
	}

	// 不带 protocol/strategy 重试：继承的应是恢复后的预留原值。
	if env := apply(`{"reservationId":"` + reservationID + `","role":"exit"}`); env.Code == 0 {
		t.Fatal("retry against offline node must fail")
	}
	stored = load()
	if stored.Protocol != before.Protocol || stored.Strategy != before.Strategy || stored.BindingID != before.BindingID {
		t.Fatalf("retry without protocol must inherit the restored reservation values, got protocol=%q strategy=%q binding=%q",
			stored.Protocol, stored.Strategy, stored.BindingID)
	}
}

// share/delete 同原则：share 下只有「首次 apply 失败已回滚」的纯预留
// 时，离线删 share 必须能本地走完（回滚后清理不依赖节点在线）。
// 注：曾成功下发的 runtime 离线删 share 仍按既有契约失败保留
// （TestPeerShareDeleteOfflineRetainsDisabledShare 锁定），不在本条
// 范围；旧代码已落库的卡死行与「曾确认 runtime 的中途态」在库内
// 不可区分，同样保留该安全口径，见 fix-C07 报告 deferred。
func TestB13ShareDeleteAfterRolledBackApply(t *testing.T) {
	h, r, share := b13Provider(t, 31000, 31002)

	env := b13Reserve(t, h, "rkDel")
	if env.Code != 0 {
		t.Fatalf("reserve failed: %+v", env)
	}
	reservationID := b13ReservationID(t, env)
	env = b13Call(t, h.federationRuntimeApplyRole, "/api/v1/federation/runtime/apply-role", "b13-share-token",
		`{"reservationId":"`+reservationID+`","role":"exit","protocol":"tls"}`)
	if env.Code == 0 {
		t.Fatal("apply against offline node must fail")
	}
	stored, err := r.GetPeerShareRuntimeByReservationID(share.ID, reservationID)
	if err != nil || stored == nil || stored.Role != "" || stored.ServiceName != "" {
		t.Fatalf("apply must have rolled back before share delete, got %+v err=%v", stored, err)
	}

	body, _ := json.Marshal(map[string]interface{}{"id": share.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/share/delete", strings.NewReader(string(body)))
	res := httptest.NewRecorder()
	h.federationShareDelete(res, req)
	var delEnv b13Envelope
	if err := json.Unmarshal(res.Body.Bytes(), &delEnv); err != nil {
		t.Fatalf("decode share delete response: %v", err)
	}
	if delEnv.Code != 0 {
		t.Fatalf("share/delete must complete offline once the failed apply rolled back, got %+v", delEnv)
	}
	if ports, err := r.ListActivePeerShareRuntimePorts(share.ID, 1); err != nil || len(ports) != 0 {
		t.Fatalf("no active runtime ports may remain after share delete, got %v err=%v", ports, err)
	}
	if gone, err := r.GetPeerShare(share.ID); err != nil || gone != nil {
		t.Fatalf("share must be deleted, got %+v err=%v", gone, err)
	}
}

// ---- B13-003 ----

// 弃单预留占满端口段后，过期回收使端口可再分配；过期预留不得再被
// apply；未过期预留不受影响。
func TestB13StaleReservationsExpireAndPortsReclaim(t *testing.T) {
	h, r, share := b13Provider(t, 31000, 31002)

	var reservationIDs []string
	for _, key := range []string{"rk1", "rk2", "rk3"} {
		env := b13Reserve(t, h, key)
		if env.Code != 0 {
			t.Fatalf("reserve %s failed: %+v", key, env)
		}
		reservationIDs = append(reservationIDs, b13ReservationID(t, env))
	}
	if env := b13Reserve(t, h, "rk4"); env.Code == 0 {
		t.Fatal("fourth reserve must fail while the whole range is held")
	}

	// 把三条预留回拨到 TTL 之前（模拟对端弃单）。
	stale := time.Now().Add(-peerShareReservationTTL - time.Minute).UnixMilli()
	for _, key := range []string{"rk1", "rk2", "rk3"} {
		rt, err := r.GetPeerShareRuntimeByResourceKey(share.ID, key)
		if err != nil || rt == nil {
			t.Fatalf("load runtime %s: %v", key, err)
		}
		rt.UpdatedTime = stale
		if err := r.UpdatePeerShareRuntime(rt); err != nil {
			t.Fatalf("backdate runtime %s: %v", key, err)
		}
	}

	// 新 reserve 触发惰性回收，端口回到可用池。
	env := b13Reserve(t, h, "rk4")
	if env.Code != 0 {
		t.Fatalf("reserve after expiry must succeed, got %+v", env)
	}
	for _, key := range []string{"rk1", "rk2", "rk3"} {
		rt, err := r.GetPeerShareRuntimeByResourceKey(share.ID, key)
		if err != nil || rt == nil || rt.Status != 0 {
			t.Fatalf("stale reservation %s must be expired, got %+v err=%v", key, rt, err)
		}
	}

	// 过期预留（rk1 的旧 reservationId）不得再被 apply。
	env = b13Call(t, h.federationRuntimeApplyRole, "/api/v1/federation/runtime/apply-role", "b13-share-token",
		`{"reservationId":"`+reservationIDs[0]+`","role":"exit","protocol":"tls"}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "not found") {
		t.Fatalf("apply of expired reservation must be refused as not found, got %+v", env)
	}

	// 新预留未过期：直接回拨单条后 apply 必须走过期分支释放并拒绝；
	// 不回拨时 apply 失败只能是节点离线而非过期。
	fresh := b13Reserve(t, h, "rk5")
	if fresh.Code != 0 {
		t.Fatalf("reserve rk5 failed: %+v", fresh)
	}
	freshID := b13ReservationID(t, fresh)
	env = b13Call(t, h.federationRuntimeApplyRole, "/api/v1/federation/runtime/apply-role", "b13-share-token",
		`{"reservationId":"`+freshID+`","role":"exit","protocol":"tls"}`)
	if env.Code == 0 || strings.Contains(env.Msg, "not found") {
		t.Fatalf("fresh reservation apply must fail only because node is offline, got %+v", env)
	}
	rt, err := r.GetPeerShareRuntimeByReservationID(share.ID, freshID)
	if err != nil || rt == nil {
		t.Fatalf("load rk5 runtime: %v", err)
	}
	rt.UpdatedTime = stale
	if err := r.UpdatePeerShareRuntime(rt); err != nil {
		t.Fatalf("backdate rk5: %v", err)
	}
	env = b13Call(t, h.federationRuntimeApplyRole, "/api/v1/federation/runtime/apply-role", "b13-share-token",
		`{"reservationId":"`+freshID+`","role":"exit","protocol":"tls"}`)
	if env.Code == 0 || !strings.Contains(env.Msg, "not found") {
		t.Fatalf("apply of backdated reservation must hit the expiry branch, got %+v", env)
	}
	rt, err = r.GetPeerShareRuntimeByReservationID(share.ID, freshID)
	if err != nil || rt == nil || rt.Status != 0 {
		t.Fatalf("expired reservation must be released by apply attempt, got %+v err=%v", rt, err)
	}
}
