package handler

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R18-B9-03：导入尾钩子内核重推 goroutine：ListKernelNodeIDs 失败时
// 裸 return，无日志、无回执披露，在线节点内核配置静默不同步；
// reconcileNodeKernels 内 ListNodeKernels 失败同样静默。
// 修复口径（零风险：只补日志，不改同步/异步形态——尾钩子改同步会
// 拖长导入回执等待，不属于 P3 零风险范畴）。
// 以下测试先于修复落盘（先红后绿）：以闭库注入读失败，断言失败被记日志。
func r18B903CaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(saved) })
	return &buf
}

func TestR18B903ReconcileNodeKernelsLogsListFailure(t *testing.T) {
	h, _ := setupBC01Test(t)
	r := handlerTestRepo(t, h)
	if err := r.Close(); err != nil {
		t.Fatalf("close repo: %v", err)
	}
	buf := r18B903CaptureLog(t)
	h.reconcileNodeKernels(1)
	if !strings.Contains(buf.String(), "list kernels") {
		t.Fatalf("R18-B9-03：ListNodeKernels 失败必须记日志，got log=%q", buf.String())
	}
}

func TestR18B903ImportTailKernelRepushLogsListFailure(t *testing.T) {
	h, _ := setupBC01Test(t)
	r := handlerTestRepo(t, h)
	if err := r.Close(); err != nil {
		t.Fatalf("close repo: %v", err)
	}
	buf := r18B903CaptureLog(t)
	result := &model.ImportResult{NodesImported: 1}
	h.runBackupImportTailHooks(result, &model.BackupData{}, []string{"nodes"})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "list kernel nodes") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("R18-B9-03：导入尾钩子 ListKernelNodeIDs 失败必须记日志，got log=%q", buf.String())
}
