package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R18-X-01（F-01）：删隧道 gost 分支逐条清理中途硬失败时，回执必须披露
// 已删转发数与标识，不得只称「隧道未删除」。
//
// 回归策略：远端节点以 httptest 伪装——按转发 ID 决定 DeleteService
// 成功/真实失败；删库腿以「处理第 N 条转发的 DeleteService 时删表」
// 模拟循环中途的数据库硬失败。先红后绿：修复前回执不含已删披露。

// r18F01RemoteNode 伪装远端 gost 节点：解析 federation 命令体中的
// services 名（"<forwardID>_<userID>_<userTunnelID>[_tcp|_udp]"），
// 按转发 ID 决定 DeleteService 成败。
type r18F01RemoteNode struct {
	mu sync.Mutex
	// failForwardIDs 命中的转发 ID 一律返回在线真实失败。
	failForwardIDs map[int64]bool
	// dropTableOnForwardID 命中的转发 ID 在返回成功前先删
	// nft_counter_state 表（模拟删库腿中途硬失败）。
	dropTableOnForwardID int64
	h                    *Handler
	// seenForwardIDs 记录收到的 DeleteService 涉及的转发 ID（按序）。
	seenForwardIDs []int64
}

func (s *r18F01RemoteNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CommandType string `json:"commandType"`
		Data        struct {
			Services []string `json:"services"`
		} `json:"data"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	forwardIDs := map[int64]bool{}
	for _, name := range req.Data.Services {
		parts := strings.SplitN(name, "_", 2)
		if len(parts) == 0 {
			continue
		}
		if id, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
			forwardIDs[id] = true
		}
	}

	s.mu.Lock()
	for id := range forwardIDs {
		s.seenForwardIDs = append(s.seenForwardIDs, id)
	}
	fail := false
	for id := range forwardIDs {
		if s.failForwardIDs[id] {
			fail = true
		}
	}
	dropTable := false
	for id := range forwardIDs {
		if s.dropTableOnForwardID != 0 && id == s.dropTableOnForwardID {
			dropTable = true
		}
	}
	s.mu.Unlock()

	if dropTable && s.h != nil {
		// 删表发生在该条转发数据面清理成功之后、删库之前——与真实
		// 删库硬失败同一时序（前一条转发已完整删除）。
		_ = s.h.repo.DB().Exec("DROP TABLE IF EXISTS nft_counter_state").Error
	}

	w.Header().Set("Content-Type", "application/json")
	if fail {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"type":    "DeleteService",
				"success": false,
				"message": "simulated real failure: permission denied",
			},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"data": map[string]any{
			"type":    "DeleteService",
			"success": true,
			"message": "ok",
		},
	})
}

// seedR18F01RemoteTunnel 在远端节点上建 gost 隧道 + N 条转发（名/端口各异）。
// 返回 tunnelID 与按 ID 升序的转发记录（与 ListForwardsByTunnel 同序）。
func seedR18F01RemoteTunnel(t *testing.T, h *Handler, serverURL string, names []string) (int64, []*forwardRecord) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := h.repo.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	remoteNode := &model.Node{
		Name: "r18f01-remote", Secret: "remote-secret", ServerIP: "198.51.100.30", Port: "1000-65535",
		Status: 1, IsRemote: 1,
		RemoteURL:   sql.NullString{String: serverURL, Valid: true},
		RemoteToken: sql.NullString{String: "token", Valid: true},
	}
	if err := h.repo.DB().Create(remoteNode).Error; err != nil {
		t.Fatalf("create remote node: %v", err)
	}
	tunnelID := seedTunnelForNftables(t, h, "r18f01-tunnel", remoteNode.ID)
	forwards := make([]*forwardRecord, 0, len(names))
	for i, name := range names {
		forwardID, err := h.repo.CreateForwardTx(
			1, "admin", name, tunnelID, "203.0.113.9:8080", "fifo", now, 1,
			[]int64{remoteNode.ID}, 20000+i, "", nil, 0, 0, nil, 0, 0, 0,
		)
		if err != nil {
			t.Fatalf("create forward %s: %v", name, err)
		}
		forward, err := h.getForwardRecord(forwardID)
		if err != nil {
			t.Fatalf("get forward %s: %v", name, err)
		}
		forwards = append(forwards, forward)
	}
	return tunnelID, forwards
}

func r18F01ForwardLabel(fwd *forwardRecord) string {
	return fmt.Sprintf("%s(#%d)", fwd.Name, fwd.ID)
}

func assertR18F01ForwardRow(t *testing.T, h *Handler, id int64, wantGone bool) {
	t.Helper()
	fr, _ := h.repo.GetForwardRecord(id)
	if wantGone && fr != nil {
		t.Fatalf("forward #%d row must be deleted", id)
	}
	if !wantGone && fr == nil {
		t.Fatalf("forward #%d row must remain", id)
	}
}

func assertR18F01TunnelRow(t *testing.T, h *Handler, tunnelID int64, wantGone bool) {
	t.Helper()
	tun, _ := h.repo.GetTunnelRecord(tunnelID)
	if wantGone && tun != nil {
		t.Fatalf("tunnel #%d row must be deleted", tunnelID)
	}
	if !wantGone && tun == nil {
		t.Fatalf("tunnel #%d row must remain", tunnelID)
	}
}

// TestR18F01DeletedForwardsSummaryFormat：披露汇总恒含已删数；>0 附
// 「名(#ID)」标识；无名转发只标 #ID。
func TestR18F01DeletedForwardsSummaryFormat(t *testing.T) {
	if got := formatDeletedForwardsSummary(nil); got != "已删除 0 条转发" {
		t.Fatalf("empty summary = %q", got)
	}
	got := formatDeletedForwardsSummary([]model.ForwardRecord{
		{ID: 7, Name: "r18f01-a"},
		{ID: 9, Name: ""},
	})
	if want := "已删除 2 条转发：r18f01-a(#7)、#9"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

// TestR18F01MidLoopDataPlaneFailureDisclosesDeleted：第 2 条（N=2/3）数据面
// 真实失败——回执须含「已删除 1 条转发」与第 1 条标识，隧道与后两条保留。
func TestR18F01MidLoopDataPlaneFailureDisclosesDeleted(t *testing.T) {
	// 远端节点是 httptest 回环地址，federation 传输默认拦截私网目标。
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	remote := &r18F01RemoteNode{failForwardIDs: map[int64]bool{}}
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)

	h := openR1B3Handler(t, "r18f01-mid-dp-fail.db")
	remote.h = h
	tunnelID, forwards := seedR18F01RemoteTunnel(t, h, server.URL, []string{"r18f01-a", "r18f01-b", "r18f01-c"})
	remote.failForwardIDs[forwards[1].ID] = true

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("mid-loop data-plane failure must abort tunnel delete, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "已删除 1 条转发") || !strings.Contains(msg, r18F01ForwardLabel(forwards[0])) {
		t.Fatalf("receipt must disclose deleted count and identity, got msg=%q body=%s", msg, res.Body.String())
	}
	if !strings.Contains(msg, "r18f01-b") {
		t.Fatalf("receipt must name the failing forward, got msg=%q", msg)
	}
	assertR18F01ForwardRow(t, h, forwards[0].ID, true)
	assertR18F01ForwardRow(t, h, forwards[1].ID, false)
	assertR18F01ForwardRow(t, h, forwards[2].ID, false)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR18F01LastForwardDataPlaneFailureDisclosesDeleted：最后一条（N=3/3）
// 数据面真实失败——回执须含「已删除 2 条转发」与前两条标识。
func TestR18F01LastForwardDataPlaneFailureDisclosesDeleted(t *testing.T) {
	// 远端节点是 httptest 回环地址，federation 传输默认拦截私网目标。
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	remote := &r18F01RemoteNode{failForwardIDs: map[int64]bool{}}
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)

	h := openR1B3Handler(t, "r18f01-last-dp-fail.db")
	remote.h = h
	tunnelID, forwards := seedR18F01RemoteTunnel(t, h, server.URL, []string{"r18f01-a", "r18f01-b", "r18f01-c"})
	remote.failForwardIDs[forwards[2].ID] = true

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("last-forward data-plane failure must abort tunnel delete, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "已删除 2 条转发") ||
		!strings.Contains(msg, r18F01ForwardLabel(forwards[0])) ||
		!strings.Contains(msg, r18F01ForwardLabel(forwards[1])) {
		t.Fatalf("receipt must disclose 2 deleted forwards with identities, got msg=%q body=%s", msg, res.Body.String())
	}
	assertR18F01ForwardRow(t, h, forwards[0].ID, true)
	assertR18F01ForwardRow(t, h, forwards[1].ID, true)
	assertR18F01ForwardRow(t, h, forwards[2].ID, false)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR18F01FirstForwardDataPlaneFailureDisclosesZeroDeleted：第 1 条
// （N=1/3）即失败——回执须含「已删除 0 条转发」，全部转发与隧道保留。
func TestR18F01FirstForwardDataPlaneFailureDisclosesZeroDeleted(t *testing.T) {
	// 远端节点是 httptest 回环地址，federation 传输默认拦截私网目标。
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	remote := &r18F01RemoteNode{failForwardIDs: map[int64]bool{}}
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)

	h := openR1B3Handler(t, "r18f01-first-dp-fail.db")
	remote.h = h
	tunnelID, forwards := seedR18F01RemoteTunnel(t, h, server.URL, []string{"r18f01-a", "r18f01-b", "r18f01-c"})
	remote.failForwardIDs[forwards[0].ID] = true

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("first-forward data-plane failure must abort tunnel delete, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "已删除 0 条转发") {
		t.Fatalf("receipt must disclose zero deleted forwards, got msg=%q body=%s", msg, res.Body.String())
	}
	for _, fwd := range forwards {
		assertR18F01ForwardRow(t, h, fwd.ID, false)
	}
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR18F01MidLoopDbDeleteFailureDisclosesDeleted：第 2 条数据面已清、
// 删库硬失败——回执须含「已删除 1 条转发」与第 1 条标识，并说明本条
// 数据面已清、库记录仍保留；第 1 条已删、第 2 条库行保留、隧道保留。
func TestR18F01MidLoopDbDeleteFailureDisclosesDeleted(t *testing.T) {
	// 远端节点是 httptest 回环地址，federation 传输默认拦截私网目标。
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	remote := &r18F01RemoteNode{failForwardIDs: map[int64]bool{}}
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)

	h := openR1B3Handler(t, "r18f01-mid-db-fail.db")
	remote.h = h
	tunnelID, forwards := seedR18F01RemoteTunnel(t, h, server.URL, []string{"r18f01-a", "r18f01-b", "r18f01-c"})
	remote.dropTableOnForwardID = forwards[1].ID

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("mid-loop db delete failure must abort tunnel delete, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "已删除 1 条转发") || !strings.Contains(msg, r18F01ForwardLabel(forwards[0])) {
		t.Fatalf("receipt must disclose deleted count and identity, got msg=%q body=%s", msg, res.Body.String())
	}
	if !strings.Contains(msg, "数据库记录仍保留") {
		t.Fatalf("receipt must state the failing forward's db row remains, got msg=%q", msg)
	}
	assertR18F01ForwardRow(t, h, forwards[0].ID, true)
	assertR18F01ForwardRow(t, h, forwards[1].ID, false)
	assertR18F01ForwardRow(t, h, forwards[2].ID, false)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR18F01BatchDeleteFailureReasonDisclosesDeleted：批量删隧道同一
// 中途失败——失败明细 Reason（appendBatchFailure 原样透传 err）
// 须含已删披露，证明 deleteTunnelAndCleanup 调用链上的回执全通。
func TestR18F01BatchDeleteFailureReasonDisclosesDeleted(t *testing.T) {
	// 远端节点是 httptest 回环地址，federation 传输默认拦截私网目标。
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	remote := &r18F01RemoteNode{failForwardIDs: map[int64]bool{}}
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)

	h := openR1B3Handler(t, "r18f01-batch.db")
	remote.h = h
	tunnelID, forwards := seedR18F01RemoteTunnel(t, h, server.URL, []string{"r18f01-a", "r18f01-b", "r18f01-c"})
	remote.failForwardIDs[forwards[1].ID] = true

	res := httptest.NewRecorder()
	h.tunnelBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"ids": []int64{tunnelID}}))
	var payload struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
			Failures     []struct {
				ID     int64  `json:"id"`
				Reason string `json:"reason"`
			} `json:"failures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, res.Body.String())
	}
	if payload.Data.FailCount != 1 || len(payload.Data.Failures) != 1 {
		t.Fatalf("expected 1 batch failure, got %+v body=%s", payload.Data, res.Body.String())
	}
	reason := payload.Data.Failures[0].Reason
	if !strings.Contains(reason, "已删除 1 条转发") || !strings.Contains(reason, r18F01ForwardLabel(forwards[0])) {
		t.Fatalf("batch failure reason must disclose deleted forwards, got %q body=%s", reason, res.Body.String())
	}
	assertR18F01ForwardRow(t, h, forwards[0].ID, true)
	assertR18F01ForwardRow(t, h, forwards[1].ID, false)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR18F01SuccessPathUnchanged：全删成功路径行为不变——接口成功、
// 隧道与全部转发均删除。
func TestR18F01SuccessPathUnchanged(t *testing.T) {
	// 远端节点是 httptest 回环地址，federation 传输默认拦截私网目标。
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	remote := &r18F01RemoteNode{failForwardIDs: map[int64]bool{}}
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)

	h := openR1B3Handler(t, "r18f01-success.db")
	remote.h = h
	tunnelID, forwards := seedR18F01RemoteTunnel(t, h, server.URL, []string{"r18f01-a", "r18f01-b", "r18f01-c"})

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("success path must succeed, got code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	for _, fwd := range forwards {
		assertR18F01ForwardRow(t, h, fwd.ID, true)
	}
	assertR18F01TunnelRow(t, h, tunnelID, true)
}

// TestR18F01OfflineBranchDbDeleteFailureDisclosesDeleted：离线分支删库
// 硬失败（本地节点离线 + 删库表缺失）——同一披露构造须覆盖该返回点。
func TestR18F01OfflineBranchDbDeleteFailureDisclosesDeleted(t *testing.T) {
	h := openR1B3Handler(t, "r18f01-offline-db-fail.db")
	tunnelID, forward := seedGostTunnelWithForward(t, h)
	if err := h.repo.DB().Exec("DROP TABLE IF EXISTS nft_counter_state").Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}
	forwards, err := h.repo.ListForwardsByTunnel(tunnelID)
	if err != nil {
		t.Fatalf("list forwards: %v", err)
	}
	if len(forwards) != 1 {
		t.Fatalf("expected 1 forward, got %d", len(forwards))
	}
	_, err = h.cleanupForwardsDataPlane(tunnelID, forwards, "隧道", true)
	if err == nil {
		t.Fatalf("offline-branch db delete failure must return an error")
	}
	if !strings.Contains(err.Error(), "已删除 0 条转发") {
		t.Fatalf("error must disclose zero deleted forwards, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), forward.Name) {
		t.Fatalf("error must name the failing forward, got %q", err.Error())
	}
	assertR18F01ForwardRow(t, h, forward.ID, false)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR18F01UserDeletePathDisclosesDeleted：删用户路径复用同一函数——
// 第 2 条数据面真实失败时回执 scope 为「用户」，并披露已删 1 条。
func TestR18F01UserDeletePathDisclosesDeleted(t *testing.T) {
	// 远端节点是 httptest 回环地址，federation 传输默认拦截私网目标。
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	remote := &r18F01RemoteNode{failForwardIDs: map[int64]bool{}}
	server := httptest.NewServer(remote)
	t.Cleanup(server.Close)

	h := openR1B3Handler(t, "r18f01-user-path.db")
	remote.h = h
	now := time.Now().UnixMilli()
	if _, err := h.repo.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	userID, err := h.repo.CreateUser("user2", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create user2: %v", err)
	}
	remoteNode := &model.Node{
		Name: "r18f01-remote", Secret: "remote-secret", ServerIP: "198.51.100.30", Port: "1000-65535",
		Status: 1, IsRemote: 1,
		RemoteURL:   sql.NullString{String: server.URL, Valid: true},
		RemoteToken: sql.NullString{String: "token", Valid: true},
	}
	if err := h.repo.DB().Create(remoteNode).Error; err != nil {
		t.Fatalf("create remote node: %v", err)
	}
	tunnelID := seedTunnelForNftables(t, h, "r18f01-user-tunnel", remoteNode.ID)
	names := []string{"r18f01-u-a", "r18f01-u-b", "r18f01-u-c"}
	forwards := make([]*forwardRecord, 0, len(names))
	for i, name := range names {
		forwardID, err := h.repo.CreateForwardTx(
			userID, "user2", name, tunnelID, "203.0.113.9:8080", "fifo", now, 1,
			[]int64{remoteNode.ID}, 20100+i, "", nil, 0, 0, nil, 0, 0, 0,
		)
		if err != nil {
			t.Fatalf("create forward %s: %v", name, err)
		}
		forward, err := h.getForwardRecord(forwardID)
		if err != nil {
			t.Fatalf("get forward %s: %v", name, err)
		}
		forwards = append(forwards, forward)
	}
	remote.failForwardIDs[forwards[1].ID] = true

	res := httptest.NewRecorder()
	h.userDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": userID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("user delete mid-loop failure must abort, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "用户未删除") {
		t.Fatalf("receipt must keep user scope, got msg=%q", msg)
	}
	if !strings.Contains(msg, "已删除 1 条转发") || !strings.Contains(msg, r18F01ForwardLabel(forwards[0])) {
		t.Fatalf("receipt must disclose deleted count and identity, got msg=%q body=%s", msg, res.Body.String())
	}
	assertR18F01ForwardRow(t, h, forwards[0].ID, true)
	assertR18F01ForwardRow(t, h, forwards[1].ID, false)
	assertR18F01ForwardRow(t, h, forwards[2].ID, false)
	if roleID, err := h.repo.GetUserRoleID(userID); err != nil || roleID == 0 {
		t.Fatalf("user row must remain, roleID=%d err=%v", roleID, err)
	}
}
