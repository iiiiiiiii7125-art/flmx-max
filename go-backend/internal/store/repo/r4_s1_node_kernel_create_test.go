package repo_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R4 补修 S1（R4X-023，R4-FIX-REPORT §6-1 转出）：CreateNodeKernel 的
// check-then-insert 在两条产品路径并发到达时撞 (node_id, kernel_type)
// 唯一约束。repo 层探针：同键并发直插——修复前恰一成功、其余吃
// UNIQUE 错误（确定性红）；修复后全部幂等成功且回读到同一行。
func TestR4S1CreateNodeKernelConcurrentSameKey(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r4s1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	const workers = 8
	now := time.Now().UnixMilli()
	start := make(chan struct{})
	errs := make([]error, workers)
	ids := make([]int64, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := &model.NodeKernel{
				NodeID: 42, KernelType: "realm", Version: "0.7.0",
				Status: "pending", Enabled: 1, CreatedTime: now, UpdatedTime: now,
			}
			<-start
			errs[i] = r.CreateNodeKernel(k)
			ids[i] = k.ID
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: concurrent create must be idempotent, got %v", i, err)
		}
		if ids[i] <= 0 || ids[i] != ids[0] {
			t.Fatalf("worker %d: id=%d, want the shared winner id %d", i, ids[i], ids[0])
		}
	}
	var count int64
	if err := r.DB().Model(&model.NodeKernel{}).
		Where("node_id = ? AND kernel_type = ?", 42, "realm").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows for (42, realm) = %d, want exactly 1", count)
	}
}

// 串行重复创建同键同为幂等成功（双击/重放口径），且零值 Enabled
// 写回范式在真插入路径上不回退。
func TestR4S1CreateNodeKernelSequentialIdempotent(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r4s1-seq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	now := time.Now().UnixMilli()
	first := &model.NodeKernel{NodeID: 7, KernelType: "singbox", Version: "1.12", Status: "pending", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeKernel(first); err != nil {
		t.Fatalf("first create: %v", err)
	}
	second := &model.NodeKernel{NodeID: 7, KernelType: "singbox", Version: "1.12", Status: "pending", Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeKernel(second); err != nil {
		t.Fatalf("second create of same key must succeed idempotently: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second create id=%d, want existing row id %d", second.ID, first.ID)
	}
}
