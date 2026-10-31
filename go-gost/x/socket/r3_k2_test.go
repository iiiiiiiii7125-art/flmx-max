package socket

// R3 K2 回归：A01-001（SetProtocol 落盘失败回滚）、A01-002
// （config.json 原子写）、WDG-01（自升级下载无进展看门狗）。
// A01-001 的用例由 A01 席探针 TestR3A01SetProtocolFailureDivergence
// 转正：探针断言的是缺陷形态（落盘失败但运行时已变），这里断言
// 修复形态（落盘失败即回滚、运行时不变）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gost/x/internal/util/download"
	"github.com/go-gost/x/service"
)

// config.json 被目录占位 -> 落盘必然失败；命令必须报错且运行时
// 屏蔽值保持旧值（三态一致：回执失败 = 运行时未变 = 落盘未变）。
func TestR3A01SetProtocolPersistFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "config.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	service.SetProtocolBlock(0, 0, 0)
	defer service.SetProtocolBlock(0, 0, 0)

	w := &WebSocketReporter{}
	err := w.handleSetProtocol(map[string]interface{}{"http": 1})
	if err == nil {
		t.Fatal("落盘失败时命令必须报错")
	}
	if h, tls, s := service.GetProtocolBlock(); h != 0 || tls != 0 || s != 0 {
		t.Fatalf("落盘失败后运行时未回滚: (%d,%d,%d), want (0,0,0)", h, tls, s)
	}
}

// updateLocalConfigJSON 的原子写形态：写回后磁盘上恒为完整新版，
// 未知字段/既有字段原样保全（R2-A01-002 口径不回退），既有文件
// 权限保留，且不留临时文件。
func TestR3A01UpdateLocalConfigJSONAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "config.json")
	orig := `{"addr":"127.0.0.1:1","secret":"s","proxy":"https://mirror.example","http":0,"tls":0,"socks":0,"custom":"keep"}`
	if err := os.WriteFile(path, []byte(orig), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := updateLocalConfigJSON(1, 0, 1); err != nil {
		t.Fatalf("updateLocalConfigJSON: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("写回的 config.json 不是完整 JSON: %v", err)
	}
	if string(fields["custom"]) != `"keep"` || string(fields["proxy"]) != `"https://mirror.example"` ||
		string(fields["addr"]) != `"127.0.0.1:1"` {
		t.Fatalf("既有字段未保全: %s", b)
	}
	var vals struct {
		HTTP  int `json:"http"`
		TLS   int `json:"tls"`
		Socks int `json:"socks"`
	}
	if err := json.Unmarshal(b, &vals); err != nil || vals.HTTP != 1 || vals.TLS != 0 || vals.Socks != 1 {
		t.Fatalf("落盘值 = %+v (err=%v), want 1/0/1", vals, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("既有文件权限未保留: %v %v", st, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("写回后目录应只有 config.json, got %v", entries)
	}

	// LB174-01 裸 null 守卫不得回退：文件内容为 null 时按空表写回。
	if err := os.WriteFile(path, []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := updateLocalConfigJSON(0, 1, 0); err != nil {
		t.Fatalf("裸 null 文件写回失败: %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) == 0 {
		t.Fatalf("裸 null 写回后文件异常: %v %q", err, b)
	}
}

// writeFileAtomic 本身的失败形态：rename 目标被目录占位时报错、
// 临时文件清干净、原目标不动——写中途失败不留半文件。
func TestR3A01WriteFileAtomicFailureLeavesNoHalfFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte(`{"http":1`), 0o644); err == nil {
		t.Fatal("rename 到目录占位目标必须失败")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" || !entries[0].IsDir() {
		t.Fatalf("失败后目录被污染: %v", entries)
	}
}

// stallUpgradeServer sends headers and one chunk, then goes silent
// until the client disconnects.
func stallUpgradeServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
}

func shrinkUpgradeStallWindow(t *testing.T, window time.Duration) {
	t.Helper()
	old := upgradeStallWindow
	upgradeStallWindow = window
	t.Cleanup(func() { upgradeStallWindow = old })
}

// 正文停滞：一次下载尝试必须在看门狗窗内以 ErrNoProgress 失败，
// 而不是烧满整程预算（调用方 ctx 给到 60s 证明不是它先到期）。
func TestR3WDGUpgradeAttemptStallFailsFast(t *testing.T) {
	shrinkUpgradeStallWindow(t, 300*time.Millisecond)
	srv := stallUpgradeServer()
	defer srv.Close()

	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err := downloadUpgradeAttempt(ctx, newUpgradeDownloadClient(""), srv.URL+"/gost-amd64", tmp, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, download.ErrNoProgress) {
		t.Fatalf("停滞下载错误 = %v, want download.ErrNoProgress", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("停滞下载耗时 %v, 看门狗应在 ~300ms 判失败", elapsed)
	}
}

// 代理腿停滞 -> 看门狗判失败 -> 剥离前缀直连腿成功，且内容与
// 哈希完整（WDG-01 的回退口径：一台的最坏成本由 240s 压到
// 看门狗窗 + 直连腿）。
func TestR3WDGUpgradeStallFallsBackToDirect(t *testing.T) {
	shrinkUpgradeStallWindow(t, 300*time.Millisecond)
	payload := []byte("new-agent-binary-payload")
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer direct.Close()
	proxy := stallUpgradeServer()
	defer proxy.Close()

	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	n, hash, err := downloadUpgradeBinary(ctx, newUpgradeDownloadClient(""), proxy.URL+"/"+direct.URL+"/gost-amd64", tmp, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("代理停滞后应直连成功: %v", err)
	}
	sum := sha256.Sum256(payload)
	if n != int64(len(payload)) || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("直连腿结果错误: n=%d hash=%s", n, hash)
	}
	if b, err := os.ReadFile(tmp); err != nil || string(b) != string(payload) {
		t.Fatalf("落盘内容错误: %v %q", err, b)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("停滞回退总耗时 %v 过长", elapsed)
	}
}

// 慢但持续进展的正文不是停滞：dribble 服务器（r8_t09 同款）在
// 窗内持续给字节，必须完整成功。
func TestR3WDGUpgradeSlowProgressCompletes(t *testing.T) {
	shrinkUpgradeStallWindow(t, 800*time.Millisecond)
	payload := slowUpgradePayload()
	srv := dribbleUpgradeServer(payload, 2048, 150*time.Millisecond)
	defer srv.Close()

	tmp := filepath.Join(t.TempDir(), "flux_agent.new")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, hash, err := downloadUpgradeAttempt(ctx, newUpgradeDownloadClient(""), srv.URL+"/gost-amd64", tmp, nil)
	if err != nil {
		t.Fatalf("慢速但有进展的下载必须成功: %v", err)
	}
	sum := sha256.Sum256(payload)
	if n != int64(len(payload)) || hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("慢速下载结果错误: n=%d hash=%s", n, hash)
	}
}

// 登记口径：生产看门狗窗 = 共用 45s 常量（A002 行 WDG-01 登记）。
func TestR3WDGUpgradeStallWindowIsSharedConstant(t *testing.T) {
	if download.StallWindow != 45*time.Second {
		t.Fatalf("download.StallWindow = %v, want 45s", download.StallWindow)
	}
	if download.StallWindow >= upgradeDownloadTimeout {
		t.Fatalf("看门狗窗 %v 必须远小于整程预算 %v", download.StallWindow, upgradeDownloadTimeout)
	}
}
