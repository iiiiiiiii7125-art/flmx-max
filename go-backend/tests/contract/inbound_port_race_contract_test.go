package contract_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
)

// AUDIT-R3 (F2X-001): 入站自动端口分配的并发竞态回归测试。
// R5-B4 把 per-node 端口锁放在 CreateNodeInbound 内部，冲突检查在锁外，
// 两并发 inboundSave(port=0) 可挑中同一随机端口，一个撞唯一索引。
// 修复后锁覆盖"选端口→冲突检查→入库"临界区，并发创建必须全部成功且端口互异。
func TestConcurrentInboundCreateAutoPortNoConflict(t *testing.T) {
	secret := "f2x001-contract-secret"
	router, r := setupContractRouter(t, secret)

	// 建节点与内核。
	node := &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "127.0.0.1", Port: "30000", Status: 1}
	if err := r.DB().Create(node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	kernel := &model.NodeKernel{NodeID: 1, KernelType: "xray", Version: "1.0", Status: "running", Enabled: 1, CreatedTime: time.Now().UnixMilli()}
	if err := r.CreateNodeKernel(kernel); err != nil {
		t.Fatalf("seed kernel: %v", err)
	}

	adminToken, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]interface{}{
				"nodeId":     1,
				"kernelType": "xray",
				"name":       fmt.Sprintf("conc-%d", i),
				"protocol":   "shadowsocks",
				"port":       0, // 自动分配
				"settings":   map[string]interface{}{"password": "testpassword123", "method": "aes-256-gcm"},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound/create", bytes.NewReader(body))
			req.Header.Set("Authorization", adminToken)
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			var out response.R
			if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
				errs[i] = fmt.Errorf("decode: %w", err)
				return
			}
			if out.Code != 0 {
				errs[i] = fmt.Errorf("code=%d msg=%s", out.Code, out.Msg)
				return
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v (并发下唯一索引冲突=竞态未修复)", i, err)
		}
	}
	// 从 DB 查各入站端口，验证互异（修复前并发下会撞唯一索引导致部分失败）。
	var rows []model.NodeInbound
	if err := r.DB().Where("node_id = ?", 1).Find(&rows).Error; err != nil {
		t.Fatalf("query inbounds: %v", err)
	}
	if len(rows) != n {
		t.Fatalf("expected %d inbounds, got %d", n, len(rows))
	}
	seen := make(map[int]bool)
	for _, row := range rows {
		if row.Port == 0 {
			t.Fatalf("inbound %d 未分配到端口", row.ID)
		}
		if seen[row.Port] {
			t.Fatalf("端口 %d 被分配两次（竞态）", row.Port)
		}
		seen[row.Port] = true
	}
}
