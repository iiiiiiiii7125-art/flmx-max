package socket

// Phase F 回归（K2 簇内新 P2）：自升级 .sha256 校验腿回退缺口。
// Phase F 在集成树 @7084cd14 真机实测：校验文件经镜像回 200 头后
// 正文停滞时，看门狗掐断后整单以 ErrNoProgress 终止、无回退
// （90.5s 失败）——readAgentChecksum 的读取在只管响应头阶段的
// 回退辅助之外，与 A002 登记口径矛盾。
// 修复形态：校验腿与二进制腿共用同一回退组合子，每次尝试 =
// GET+正文读取整体（fetchUpgradeChecksumAttempt），任一失败
// （含正文停滞）切下一线路。KU4 (1.7.7) 起组合子泛化为
// download.TryPlan（测速选线计划），两腿共用同一 plan 入参，
// 本文件的用例随之改用显式静态计划，断言不变。

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
	plan := download.NewStaticPlan([]download.Route{{Prefix: proxy.URL}, {}})
	start := time.Now()
	_, actualHash, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), plan, direct.URL+"/gost-amd64", tmp, nil)
	if err != nil {
		t.Fatalf("二进制腿应经回退成功: %v", err)
	}
	expectedHash, err := downloadUpgradeChecksum(ctx, newUpgradeDownloadClient(""), plan, direct.URL+"/gost-amd64.sha256")
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

// ② 校验文件两条线路皆停滞 -> 整单在预算内失败且错误可辨
// （聚合错误中含停滞原因）。KU4 形态：线路 = [stall1 前缀,
// 直连]，直连目标本身是 stall2（同样停滞）。
func TestR3K2FixChecksumBothLegsStallFailsWithinBudget(t *testing.T) {
	shrinkUpgradeStallWindow(t, 300*time.Millisecond)
	stall1 := stallUpgradeServer()
	defer stall1.Close()
	stall2 := stallUpgradeServer()
	defer stall2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	plan := download.NewStaticPlan([]download.Route{{Prefix: stall1.URL}, {}})
	start := time.Now()
	_, err := downloadUpgradeChecksum(ctx, newUpgradeDownloadClient(""), plan, stall2.URL+"/gost-amd64.sha256")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("两线路皆停滞时校验腿必须失败")
	}
	if !strings.Contains(err.Error(), download.ErrNoProgress.Error()) {
		t.Fatalf("聚合错误应点名停滞原因, got: %v", err)
	}
	if elapsed < 400*time.Millisecond {
		t.Fatalf("耗时 %v 过短, 两次尝试应各耗一看门狗窗 (~300ms)", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("两线路皆停滞耗时 %v, 应在 ~2 个看门狗窗内失败而非烧满预算", elapsed)
	}
}

// 线路集只有直连时的校验腿停滞：只尝试一次、无重试（与二进制
// 腿同口径），错误即 ErrNoProgress 本体。KU4 形态：旧「无前缀
// URL」即排名中只有直连一条线路。
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
	_, err := downloadUpgradeChecksum(ctx, newUpgradeDownloadClient(""), download.NewStaticPlan([]download.Route{{}}), srv.URL+"/gost-amd64.sha256")
	if err == nil {
		t.Fatal("直连校验停滞必须失败")
	}
	if hits.Load() != 1 {
		t.Fatalf("单线路应只尝试一次, hits=%d", hits.Load())
	}
}

// ③ 二进制停滞 + 校验停滞组合的最坏耗时与预算对账：两腿两线
// 全停滞时总耗时 ≈ 4 个看门狗窗（实测断言）。生产常量对账：
// A002 的两个锚点不变（看门狗窗 45s、整程 240s < 面板 300s）；
// KU4 起最坏组合的口径变了——旧式「4 次尝试 × 45s = 180s」是
// 两线路世界的恒等式，新世界里测速先以 ≤4s 总界把停滞线路挡在
// 排名外（探针即 .sha256 拉取），能进排名的线路若在下载腿再
// 停滞，每试仍受 45s 看门狗约束、整段受 240s 共享 ctx 硬顶，
// 故登记断言改为锚点值 + 探测预算上限，不再断言旧恒等式。
func TestR3K2FixWorstCaseStallBudgetReconciles(t *testing.T) {
	// 生产常量对账（A002 锚点 + KU4 探测预算）。
	if download.StallWindow != 45*time.Second || upgradeDownloadTimeout != 240*time.Second {
		t.Fatalf("A002 登记值漂移: StallWindow=%v upgradeDownloadTimeout=%v", download.StallWindow, upgradeDownloadTimeout)
	}
	if download.ProbeRouteTimeout != 3*time.Second || download.ProbeBudget != 4*time.Second {
		t.Fatalf("KU4 探测预算漂移: ProbeRouteTimeout=%v ProbeBudget=%v", download.ProbeRouteTimeout, download.ProbeBudget)
	}
	if download.ProbeBudget >= upgradeDownloadTimeout {
		t.Fatalf("探测总界 %v 必须远小于整程预算 %v", download.ProbeBudget, upgradeDownloadTimeout)
	}

	shrinkUpgradeStallWindow(t, 300*time.Millisecond)
	stall1 := stallUpgradeServer()
	defer stall1.Close()
	stall2 := stallUpgradeServer()
	defer stall2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	plan := download.NewStaticPlan([]download.Route{{Prefix: stall1.URL}, {}})
	start := time.Now()
	if _, _, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), plan, stall2.URL+"/gost-amd64", tmp, nil); err == nil {
		t.Fatal("二进制两线路皆停滞必须失败")
	}
	if _, err := downloadUpgradeChecksum(ctx, newUpgradeDownloadClient(""), plan, stall2.URL+"/gost-amd64.sha256"); err == nil {
		t.Fatal("校验两线路皆停滞必须失败")
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
