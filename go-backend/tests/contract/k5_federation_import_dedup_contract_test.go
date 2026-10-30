package contract_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// B13-001（Round 2）：远端节点导入此前无 (remoteUrl, token/shareId)
// 查重——同一分享重复导入每次新建一个同名远端节点。修复口径：
// 命中既有行则原地刷新并在回执提示「已存在、已刷新」，不再新建。
// 本测试锁定三条腿：同 token 重复导入不新增且数据真刷新；提供方
// 为同一 share 换发 token 后凭 shareId 仍命中同一行（token 随之
// 更新）；不同 share 仍各自新建。

func k5ImportRemoteNode(t *testing.T, router http.Handler, adminToken, remoteURL, token string) response.R {
	t.Helper()
	body := `{"remoteUrl":` + mustJSONString(t, remoteURL) + `,"token":` + mustJSONString(t, token) + `}`
	return kernelContractPost(t, router, adminToken, "/api/v1/federation/node/import", body)
}

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal string: %v", err)
	}
	return string(b)
}

func TestFederationImportDedupContract(t *testing.T) {
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")

	providerRouter, providerRepo := setupContractRouter(t, "k5-provider-jwt")
	providerServer := httptest.NewServer(providerRouter)
	defer providerServer.Close()

	consumerRouter, consumerRepo := setupContractRouter(t, "k5-consumer-jwt")
	consumerAdmin, err := auth.GenerateToken(1, "consumer-admin", 0, "k5-consumer-jwt")
	if err != nil {
		t.Fatalf("consumer token: %v", err)
	}

	now := time.Now().UnixMilli()
	providerNodeID := insertContractNode(t, providerRepo, "k5-provider-node", "198.51.100.21", "43000-43010", "k5-provider-secret", 0)
	shareID := insertPeerShare(t, providerRepo, &repo.PeerShare{
		Name:           "k5-share",
		NodeID:         providerNodeID,
		Token:          "k5-share-token",
		PortRangeStart: 43000,
		PortRangeEnd:   43010,
		IsActive:       1,
		CreatedTime:    now,
		UpdatedTime:    now,
	})

	countRemote := func() int {
		return mustQueryInt(t, consumerRepo, `SELECT COUNT(*) FROM node WHERE is_remote = 1`)
	}

	// 第一次导入：新建一行，回执无 refreshed 标记。
	first := k5ImportRemoteNode(t, consumerRouter, consumerAdmin, providerServer.URL, "k5-share-token")
	if first.Code != 0 {
		t.Fatalf("first import: %+v", first)
	}
	if data, _ := first.Data.(map[string]interface{}); data != nil && data["refreshed"] == true {
		t.Fatalf("first import must not be marked refreshed: %+v", first)
	}
	if got := countRemote(); got != 1 {
		t.Fatalf("after first import remote nodes = %d, want 1", got)
	}
	nodeID := queryRemoteNodeIDByToken(t, consumerRepo, "k5-share-token")
	if got := mustQueryString(t, consumerRepo, `SELECT port FROM node WHERE id = ?`, nodeID); got != "43000-43010" {
		t.Fatalf("imported node port range = %q, want 43000-43010", got)
	}

	// 提供方改端口段后同 token 重复导入：不新增、回执提示已刷新、
	// 既有行数据被新值覆盖。
	if err := providerRepo.DB().Exec(`UPDATE peer_share SET port_range_end = 43012 WHERE id = ?`, shareID).Error; err != nil {
		t.Fatalf("widen provider share range: %v", err)
	}
	second := k5ImportRemoteNode(t, consumerRouter, consumerAdmin, providerServer.URL, "k5-share-token")
	if second.Code != 0 {
		t.Fatalf("second import: %+v", second)
	}
	if got := countRemote(); got != 1 {
		t.Fatalf("B13-001: after duplicate import remote nodes = %d, want 1", got)
	}
	if !strings.Contains(second.Msg, "已刷新") {
		t.Fatalf("B13-001: duplicate import msg = %q, want 已存在/已刷新提示", second.Msg)
	}
	data, _ := second.Data.(map[string]interface{})
	if data == nil || data["refreshed"] != true {
		t.Fatalf("B13-001: duplicate import data = %v, want refreshed=true", second.Data)
	}
	if got := mustQueryString(t, consumerRepo, `SELECT port FROM node WHERE id = ?`, nodeID); got != "43000-43012" {
		t.Fatalf("B13-001: refreshed node port range = %q, want 43000-43012", got)
	}

	// 提供方为同一 share 换发 token：凭 shareId 仍命中同一行，
	// 本地存的 token 随之更新为新 token。
	if err := providerRepo.DB().Exec(`UPDATE peer_share SET token = ? WHERE id = ?`, "k5-share-token-v2", shareID).Error; err != nil {
		t.Fatalf("rotate provider share token: %v", err)
	}
	third := k5ImportRemoteNode(t, consumerRouter, consumerAdmin, providerServer.URL, "k5-share-token-v2")
	if third.Code != 0 {
		t.Fatalf("third import (rotated token): %+v", third)
	}
	if got := countRemote(); got != 1 {
		t.Fatalf("B13-001: after rotated-token import remote nodes = %d, want 1", got)
	}
	if got := queryRemoteNodeIDByToken(t, consumerRepo, "k5-share-token-v2"); got != nodeID {
		t.Fatalf("B13-001: rotated-token import resolved to node %d, want existing %d", got, nodeID)
	}

	// 不同 share：仍各自新建一行（查重不得误伤）。
	providerNode2ID := insertContractNode(t, providerRepo, "k5-provider-node-2", "198.51.100.22", "44000-44010", "k5-provider-secret-2", 0)
	insertPeerShare(t, providerRepo, &repo.PeerShare{
		Name:           "k5-share-other",
		NodeID:         providerNode2ID,
		Token:          "k5-share-token-other",
		PortRangeStart: 44000,
		PortRangeEnd:   44010,
		IsActive:       1,
		CreatedTime:    now,
		UpdatedTime:    now,
	})
	fourth := k5ImportRemoteNode(t, consumerRouter, consumerAdmin, providerServer.URL, "k5-share-token-other")
	if fourth.Code != 0 {
		t.Fatalf("fourth import (other share): %+v", fourth)
	}
	if got := countRemote(); got != 2 {
		t.Fatalf("after importing a different share remote nodes = %d, want 2", got)
	}
}
