package socket

// Phase F 回归（K2 簇内新 P2）：自升级 .sha256 校验腿回退缺口。
// Phase F 在集成树 @7084cd14 真机实测：校验文件经镜像回 200 头后
// 正文停滞时，看门狗掐断后整单以 ErrNoProgress 终止、无回退
// （90.5s 失败）——readAgentChecksum 的读取在只管响应头阶段的
// getWithUpgradeProxyFallbackCtx 之外，与 A002 登记口径矛盾。
// 修复形态：校验腿与二进制腿共用 upgradeWithProxyFallback，
// 每次尝试 = GET+正文读取整体（fetchUpgradeChecksumAttempt），
// 任一失败（含正文停滞）剥离前缀直连重试一次。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/download"
)

// upgradeDirectServer serves the binary payload and its checksum
// file on the direct (prefix-stripped) origin, counting hits.
func upgradeDirectServer(payload []byte, binHits, sumHits *atomic.Int32) *httptest.Server {
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	mux.HandleFunc("/gost-amd64", func(w http.ResponseWriter, r *http.Request) {
		binHits.Add(1)
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/gost-amd64.sha256", func(w http.ResponseWriter, r *http.Request) {
		sumHits.Add(1)
		_, _ = w.Write([]byte(hash + "  gost-amd64\n"))
	})
	return httptest.NewServer(mux)
}

// ① 校验文件停滞 -> 回退直连成功，且与二进制腿组合后整单下载
// 阶段完成（handleUpgradeAgent 的下载+校验序列：共享一个 ctx，
// 二进制经回退落盘、校验经回退取到期望哈希、两者比对一致）。
// 代理对两腿的首次尝试都停滞（stallUpgradeServer 不分路径）。
func TestR3K2FixChecksumStallFallsBackAndUpgradeCompletes(t *testing.T) {
	shrinkUpgradeStallWindow(t, 300*time.Millisecond)
	payload := []byte("new-agent-binary-payload")
	sum := sha256.Sum256(payload)
	wantHash := hex.EncodeToString(sum[:])

	var binHits, sumHits atomic.Int32
	direct := upgradeDirectServer(payload, &binHits, &sumHits)
	defer direct.Close()
	proxy := stallUpgradeServer()
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	start := time.Now()
	_, actualHash, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), proxy.URL+"/"+direct.URL+"/gost-amd64", tmp, nil)
	if err != nil {
		t.Fatalf("二进制腿应经回退成功: %v", err)
	}
	expectedHash, err := downloadUpgradeChecksum(ctx, newUpgradeDownloadClient(""), proxy.URL+"/"+direct.URL+"/gost-amd64.sha256")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("校验腿停滞后应回退直连成功 (Phase F 缺陷形态为整单 ErrNoProgress): %v", err)
	}
	if expectedHash != wantHash || actualHash != wantHash {
		t.Fatalf("哈希不一致: expected=%q actual=%q want=%q", expectedHash, actualHash, wantHash)
	}
	if binHits.Load() == 0 || sumHits.Load() == 0 {
		t.Fatalf("直连腿未被命中: binHits=%d sumHits=%d", binHits.Load(), sumHits.Load())
	}
	if b, err := os.ReadFile(tmp); err != nil || string(b) != string(payload) {
		t.Fatalf("落盘内容错误: %v %q", err, b)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("两腿回退总耗时 %v 过长", elapsed)
	}
}

// ② 校验文件两腿皆停滞 -> 整单在预算内失败且错误可辨（合并
// 错误中含停滞原因）。
func TestR3K2FixChecksumBothLegsStallFailsWithinBudget(t *testing.T) {
	shrinkUpgradeStallWindow(t, 300*time.Millisecond)
	stall1 := stallUpgradeServer()
	defer stall1.Close()
	stall2 := stallUpgradeServer()
	defer stall2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	_, err := downloadUpgradeChecksum(ctx, newUpgradeDownloadClient(""), stall1.URL+"/"+stall2.URL+"/gost-amd64.sha256")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("两腿皆停滞时校验腿必须失败")
	}
	if !strings.Contains(err.Error(), download.ErrNoProgress.Error()) {
		t.Fatalf("合并错误应点名停滞原因, got: %v", err)
	}
	if elapsed < 400*time.Millisecond {
		t.Fatalf("耗时 %v 过短, 两次尝试应各耗一看门狗窗 (~300ms)", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("两腿皆停滞耗时 %v, 应在 ~2 个看门狗窗内失败而非烧满预算", elapsed)
	}
}

// 无前缀 URL 的校验腿停滞：只尝试一次、无重试（与二进制腿同
// 口径），错误即 ErrNoProgress 本体。
func TestR3K2FixChecksumDirectStallSingleAttempt(t *testing.T) {
	shrinkUpgradeStallWindow(t, 300*time.Millisecond)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(60 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := downloadUpgradeChecksum(ctx, newUpgradeDownloadClient(""), srv.URL+"/gost-amd64.sha256")
	if err == nil {
		t.Fatal("直连校验停滞必须失败")
	}
	if hits.Load() != 1 {
		t.Fatalf("无前缀 URL 应只尝试一次, hits=%d", hits.Load())
	}
}

// ③ 二进制停滞 + 校验停滞组合的最坏耗时与 A002 登记预算对账：
// 两腿两试全停滞时总耗时 ≈ 4 个看门狗窗（实测断言），且生产
// 常量满足 4 × 45s = 180s ≤ 整程 240s（upgradeDownloadTimeout），
// 回退吃不掉 verify/install 的余量（240s < 面板 300s）。
func TestR3K2FixWorstCaseStallBudgetReconciles(t *testing.T) {
	// 生产常量对账（A002）。
	if worst := 4 * download.StallWindow; worst > upgradeDownloadTimeout {
		t.Fatalf("最坏停滞组合 %v 超过整程预算 %v", worst, upgradeDownloadTimeout)
	}
	if 4*download.StallWindow != 180*time.Second || upgradeDownloadTimeout != 240*time.Second {
		t.Fatalf("A002 登记值漂移: StallWindow=%v upgradeDownloadTimeout=%v", download.StallWindow, upgradeDownloadTimeout)
	}

	shrinkUpgradeStallWindow(t, 300*time.Millisecond)
	stall1 := stallUpgradeServer()
	defer stall1.Close()
	stall2 := stallUpgradeServer()
	defer stall2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	start := time.Now()
	if _, _, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), stall1.URL+"/"+stall2.URL+"/gost-amd64", tmp, nil); err == nil {
		t.Fatal("二进制两腿皆停滞必须失败")
	}
	if _, err := downloadUpgradeChecksum(ctx, newUpgradeDownloadClient(""), stall1.URL+"/"+stall2.URL+"/gost-amd64.sha256"); err == nil {
		t.Fatal("校验两腿皆停滞必须失败")
	}
	elapsed := time.Since(start)
	// 4 次尝试 × 300ms 窗 ≈ 1.2s；上限取 4 窗 + 5s 余量，远低于
	// 60s 的 ctx（证明整程预算不是先到期的那个）。
	if elapsed > 4*300*time.Millisecond+5*time.Second {
		t.Fatalf("最坏组合耗时 %v 超过 4 个看门狗窗 + 余量", elapsed)
	}
	if elapsed < 800*time.Millisecond {
		t.Fatalf("最坏组合耗时 %v 过短, 4 次停滞尝试应各耗一看门狗窗", elapsed)
	}
}
