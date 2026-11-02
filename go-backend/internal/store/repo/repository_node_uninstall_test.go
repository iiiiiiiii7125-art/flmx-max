package repo

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func openNodeUninstallRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "node-uninstall.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// 待卸载登记的仓储语义：写入/读回、重复登记幂等、过期清扫、删除。
func TestNodeUninstallRegistryRoundTrip(t *testing.T) {
	r := openNodeUninstallRepo(t)
	now := time.Now()

	if err := r.AddNodeUninstall("secret-a", "node-a", now); err != nil {
		t.Fatalf("add: %v", err)
	}
	row, err := r.GetNodeUninstallBySecret("secret-a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row == nil {
		t.Fatal("expected a pending uninstall row")
	}
	if row.NodeName != "node-a" || row.CreatedTime != now.UnixMilli() {
		t.Fatalf("row mismatch: %+v", row)
	}

	// 重复登记幂等：不覆盖首次的节点名与时间。
	if err := r.AddNodeUninstall("secret-a", "node-a-renamed", now.Add(time.Hour)); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	row, err = r.GetNodeUninstallBySecret("secret-a")
	if err != nil || row == nil {
		t.Fatalf("get after re-add: row=%v err=%v", row, err)
	}
	if row.NodeName != "node-a" || row.CreatedTime != now.UnixMilli() {
		t.Fatalf("re-add must be a no-op, got %+v", row)
	}

	// 删除后读不到。
	if err := r.DeleteNodeUninstall("secret-a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	row, err = r.GetNodeUninstallBySecret("secret-a")
	if err != nil || row != nil {
		t.Fatalf("get after delete: row=%v err=%v", row, err)
	}
}

func TestNodeUninstallRegistryValidationAndRetention(t *testing.T) {
	r := openNodeUninstallRepo(t)
	now := time.Now()

	// 空 secret：写入报错、读取回 (nil, nil)。
	if err := r.AddNodeUninstall("  ", "x", now); err == nil {
		t.Fatal("empty secret must be rejected")
	}
	if row, err := r.GetNodeUninstallBySecret(""); err != nil || row != nil {
		t.Fatalf("empty secret get: row=%v err=%v", row, err)
	}
	// 未登记的 secret 读回 (nil, nil)。
	if row, err := r.GetNodeUninstallBySecret("never-registered"); err != nil || row != nil {
		t.Fatalf("missing get: row=%v err=%v", row, err)
	}

	// 过期登记（超过保留期）在下一次写入时被清扫。
	stale := &model.NodeUninstall{
		Secret:      "stale-secret",
		NodeName:    "stale",
		CreatedTime: now.Add(-nodeUninstallRetention - time.Hour).UnixMilli(),
	}
	if err := r.DB().Create(stale).Error; err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	if err := r.AddNodeUninstall("fresh-secret", "fresh", now); err != nil {
		t.Fatalf("add fresh: %v", err)
	}
	if row, err := r.GetNodeUninstallBySecret("stale-secret"); err != nil || row != nil {
		t.Fatalf("stale row must be swept: row=%v err=%v", row, err)
	}
	if row, err := r.GetNodeUninstallBySecret("fresh-secret"); err != nil || row == nil {
		t.Fatalf("fresh row must survive: row=%v err=%v", row, err)
	}
}
