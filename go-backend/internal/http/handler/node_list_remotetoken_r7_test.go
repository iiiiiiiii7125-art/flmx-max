package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// R7-F2-01 回归：/node/list DTO 不得回传联邦凭据 remoteToken（前端
// 零消费、仅类型声明），且远端节点状态同步不得因此断裂——同步改
// 经 repo.ListRemoteNodes 在服务端内部按节点 ID 取 URL/令牌，本
// 测试以真实对端（httptest）验证同步仍带着存量令牌完成，且列表
// 回执全文不含令牌本体与 remoteToken 键。

func TestNodeListOmitsRemoteTokenAndSyncStillWorks(t *testing.T) {
	allowFetchEnv(t)

	const peerToken = "peer-token-r7-g4"
	var gotAuth string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/federation/connect" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"操作成功","data":{"shareId":7,"nodeId":9,"status":1}}`))
	}))
	defer peer.Close()

	h, r := setupLatencyHandler(t, "")
	node := model.Node{
		Name: "r7-remote-node", Secret: "r7-remote-secret", ServerIP: "198.51.100.77",
		Status: 0, IsRemote: 1,
		RemoteURL:   sql.NullString{String: peer.URL, Valid: true},
		RemoteToken: sql.NullString{String: peerToken, Valid: true},
	}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatalf("create remote node: %v", err)
	}

	res := postJSONToHandler(t, h.nodeList, map[string]interface{}{})
	body := res.Body.String()
	if strings.Contains(body, peerToken) {
		t.Fatalf("node/list response leaks remote token: %s", body)
	}
	if strings.Contains(body, "remoteToken") {
		t.Fatalf("node/list response still carries remoteToken key: %s", body)
	}

	var env struct {
		Code int                      `json:"code"`
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if env.Code != 0 || len(env.Data) != 1 {
		t.Fatalf("node list code=%d items=%d body=%s", env.Code, len(env.Data), body)
	}
	// 同步仍完成：对端收到存量令牌，节点状态被同步为对端回执的 1。
	if gotAuth != "Bearer "+peerToken {
		t.Fatalf("peer connect Authorization = %q, want Bearer <stored token>", gotAuth)
	}
	if st, _ := env.Data[0]["status"].(float64); st != 1 {
		t.Fatalf("remote node status = %v, want 1 (synced from peer)", env.Data[0]["status"])
	}
	// remoteUrl 不是凭据，列表呈现口径不变。
	if env.Data[0]["remoteUrl"] != peer.URL {
		t.Fatalf("remoteUrl = %v, want %q", env.Data[0]["remoteUrl"], peer.URL)
	}
}

// /node/check-status 与 /node/list 共用 ListNodes DTO，同口径不得
// 回传 remoteToken（同一 DTO 的第二下发面，防只修一面）。
func TestNodeCheckStatusOmitsRemoteToken(t *testing.T) {
	h, r := setupLatencyHandler(t, "")
	const peerToken = "peer-token-r7-g4-check"
	node := model.Node{
		Name: "r7-remote-node-2", Secret: "r7-remote-secret-2", ServerIP: "198.51.100.78",
		Status: 0, IsRemote: 1,
		RemoteURL:   sql.NullString{String: "http://127.0.0.1:1", Valid: true},
		RemoteToken: sql.NullString{String: peerToken, Valid: true},
	}
	if err := r.DB().Create(&node).Error; err != nil {
		t.Fatalf("create remote node: %v", err)
	}
	res := postJSONToHandler(t, h.nodeCheckStatus, map[string]interface{}{})
	body := res.Body.String()
	if strings.Contains(body, peerToken) || strings.Contains(body, "remoteToken") {
		t.Fatalf("node/check-status response leaks remoteToken: %s", body)
	}
}
