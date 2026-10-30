package socket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-gost/x/service"
)

// A01-002 回归：SetProtocol 部分字段下发时，未给出的开关必须沿用
// 当前值。此前默认一律 0——只下发 {"http":1} 会把已开启的 tls/
// socks 屏蔽清零并落盘，静默解禁且重启不恢复。

func TestHandleSetProtocolPartialKeepsCurrentValues(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// 现状：tls 与 socks 屏蔽开启，http 未开；config.json 同值。
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"addr":"127.0.0.1:1","secret":"s","http":0,"tls":1,"socks":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	service.SetProtocolBlock(0, 1, 1)
	defer service.SetProtocolBlock(0, 0, 0)

	w := &WebSocketReporter{}

	// 只下发 http：tls/socks 必须保留为 1（内存态与落盘一致）。
	if err := w.handleSetProtocol(map[string]interface{}{"http": 1}); err != nil {
		t.Fatalf("partial SetProtocol: %v", err)
	}
	if h, tls, s := service.GetProtocolBlock(); h != 1 || tls != 1 || s != 1 {
		t.Fatalf("未下发字段被清零: http=%d tls=%d socks=%d, want 1/1/1", h, tls, s)
	}
	assertConfigProtocol(t, dir, 1, 1, 1)

	// 显式下发 0 只清该字段。
	if err := w.handleSetProtocol(map[string]interface{}{"tls": 0}); err != nil {
		t.Fatalf("explicit zero SetProtocol: %v", err)
	}
	if h, tls, s := service.GetProtocolBlock(); h != 1 || tls != 0 || s != 1 {
		t.Fatalf("显式清零口径错误: http=%d tls=%d socks=%d, want 1/0/1", h, tls, s)
	}
	assertConfigProtocol(t, dir, 1, 0, 1)

	// 非法值拒绝且状态不变。
	if err := w.handleSetProtocol(map[string]interface{}{"http": 2}); err == nil {
		t.Fatal("http=2 必须被拒绝")
	}
	if h, tls, s := service.GetProtocolBlock(); h != 1 || tls != 0 || s != 1 {
		t.Fatalf("非法值后状态被改动: %d/%d/%d", h, tls, s)
	}
}

func assertConfigProtocol(t *testing.T, dir string, wantHTTP, wantTLS, wantSocks int) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		HTTP  int `json:"http"`
		TLS   int `json:"tls"`
		Socks int `json:"socks"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP != wantHTTP || cfg.TLS != wantTLS || cfg.Socks != wantSocks {
		t.Fatalf("config.json 落盘值 = %d/%d/%d, want %d/%d/%d",
			cfg.HTTP, cfg.TLS, cfg.Socks, wantHTTP, wantTLS, wantSocks)
	}
}
