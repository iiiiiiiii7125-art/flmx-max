package repo

import (
	"context"
	"path/filepath"
	"testing"
)

// X-P3-21+X-P3-25 (L-08-3 + L-10-1): Export 系列签名加 ctx 并
// WithContext 下传；客户端断连/取消后导出查询应随 ctx 终止，而非
// 照跑到底。Import 同步接 ctx。
func TestR1B8_XP321_ExportImportContextCancellation(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "xp321.db"))
	if err != nil {
		t.Fatal(err)
	}

	// 正常 ctx：导出成功。
	data, err := r.ExportAllContext(context.Background())
	if err != nil || data == nil {
		t.Fatalf("background export: data=%v err=%v", data != nil, err)
	}
	if _, err := r.ExportPartialContext(context.Background(), []string{"users"}); err != nil {
		t.Fatalf("background partial export: %v", err)
	}

	// 已取消 ctx：导出必须报错，不得无视取消跑完。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ExportAllContext(ctx); err == nil {
		t.Fatalf("cancelled ctx 下 ExportAll 应报错")
	}
	if _, err := r.ExportPartialContext(ctx, []string{"users"}); err == nil {
		t.Fatalf("cancelled ctx 下 ExportPartial 应报错")
	}
	if _, err := r.ImportContext(ctx, data, []string{"users"}); err == nil {
		t.Fatalf("cancelled ctx 下 Import 应报错")
	}
}
