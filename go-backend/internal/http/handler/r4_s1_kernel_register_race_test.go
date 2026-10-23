package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R4 补修 S1（R4X-023，R4-FIX-REPORT §6-1 / 批 9 spike 转出）：
// ensureRealmKernel 与 kernelInstall 均为 check-then-insert，两条产品
// 路径（WS 重连对账 syncRealmNodes 与同步 switchTunnelEngine / 管理员
// 双击安装）并发到达时败者吃 (node_id, kernel_type) UNIQUE 约束错误。
// 以下探针模拟双到达：修复前败者报错（红），修复后双双幂等成功、
// 库内恰一行。

func TestR4S1EnsureRealmKernelConcurrentDoubleArrival(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r4s1-realm.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	const rounds = 30
	for round := 0; round < rounds; round++ {
		nodeID := int64(1000 + round)
		start := make(chan struct{})
		errs := make([]error, 2)
		ids := make([]int64, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				k, err := h.ensureRealmKernel(nodeID, true)
				errs[i] = err
				if k != nil {
					ids[i] = k.ID
				}
			}(i)
		}
		close(start)
		wg.Wait()
		for i := range errs {
			if errs[i] != nil {
				t.Fatalf("round %d worker %d: ensureRealmKernel must be idempotent under concurrent arrival, got %v", round, i, errs[i])
			}
		}
		if ids[0] <= 0 || ids[0] != ids[1] {
			t.Fatalf("round %d: kernel ids %v, want one shared row", round, ids)
		}
	}
}

func TestR4S1KernelInstallConcurrentDoubleSubmit(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r4s1-install.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "r4s1-node", "192.0.2.44")
	if err := r.DB().Exec(`UPDATE node SET status = 1 WHERE id = ?`, nodeID).Error; err != nil {
		t.Fatal(err)
	}

	install := func() (int, string) {
		body := `{"nodeId":` + strconv.FormatInt(nodeID, 10) + `,"kernelType":"` + kernel.TypeSingbox + `","version":"1.12"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/kernel/install", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.kernelInstall(rec, req)
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			return -999, "decode: " + rec.Body.String()
		}
		return env.Code, env.Msg
	}

	start := make(chan struct{})
	msgs := make([]string, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, msgs[i] = install()
		}(i)
	}
	close(start)
	wg.Wait()

	for i, msg := range msgs {
		if strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "constraint failed") {
			t.Fatalf("worker %d: concurrent install leaked raw constraint error: %q", i, msg)
		}
	}
	var count int64
	if err := r.DB().Table("node_kernel").
		Where("node_id = ? AND kernel_type = ?", nodeID, kernel.TypeSingbox).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("node_kernel rows = %d, want exactly 1", count)
	}
	row, err := r.GetNodeKernelByType(nodeID, kernel.TypeSingbox)
	if err != nil || row == nil {
		t.Fatalf("load installed kernel: %v / %v", row, err)
	}
	if row.Version != "1.12" {
		t.Fatalf("installed version = %q, want requested 1.12", row.Version)
	}
}
