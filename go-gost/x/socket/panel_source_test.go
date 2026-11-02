package socket

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// 面板源分发（DESIGN panel-source）：派生只从本机配置的面板
// 地址出发，映射与 /flow 上报（traffic_reporter）同口径——
// wss/https→https、ws/http→http、裸地址默认 https、只取
// origin（路径/query 丢弃）、非标准端口保留。派生结果是
// Route 前缀 + 面板 gh_mirror 路径，形态与内置镜像线路一致。

func TestDerivePanelOriginTable(t *testing.T) {
	cases := []struct {
		addr string
		want string
		ok   bool
	}{
		{"wss://panel.example.com", "https://panel.example.com", true},
		{"ws://panel.example.com", "http://panel.example.com", true},
		{"https://panel.example.com", "https://panel.example.com", true},
		{"http://panel.example.com", "http://panel.example.com", true},
		{"panel.example.com", "https://panel.example.com", true},                 // 裸地址默认 https，不降级
		{"wss://panel.example.com:8443", "https://panel.example.com:8443", true}, // 非标准端口保留
		{"ws://10.0.0.8:6365", "http://10.0.0.8:6365", true},
		{"https://panel.example.com/sub/path?x=1", "https://panel.example.com", true}, // 路径/query 丢弃，只剩 origin
		{"wss://panel.example.com:443/", "https://panel.example.com:443", true},
		{"", "", false},
		{"   ", "", false},
	}
	for _, c := range cases {
		got, ok := derivePanelOrigin(c.addr)
		if ok != c.ok || got != c.want {
			t.Fatalf("derivePanelOrigin(%q) = %q, %v; want %q, %v", c.addr, got, ok, c.want, c.ok)
		}
	}
}

func TestPanelSourceRouteShape(t *testing.T) {
	route, host, ok := panelSourceRoute("wss://Panel.Example.com:8443")
	if !ok {
		t.Fatal("wss 地址应派生出面板线路")
	}
	if route.Prefix != "https://Panel.Example.com:8443/api/v1/open_api/gh" {
		t.Fatalf("面板线路前缀错误: %q", route.Prefix)
	}
	// 重定向放行位是小写主机名（含端口）。
	if host != "panel.example.com:8443" {
		t.Fatalf("面板主机名应小写且带端口: %q", host)
	}
	// 线路 URL 形态与内置镜像逐字同构：前缀 + "/" + 本地构造的直连 URL。
	direct := "https://github.com/iiiiiiiii7125-art/flmx-max/releases/download/1.8.0/gost-amd64.sha256"
	if got := route.URL(direct); got != "https://Panel.Example.com:8443/api/v1/open_api/gh/"+direct {
		t.Fatalf("面板线路 URL 形态错误: %q", got)
	}
	if _, _, ok := panelSourceRoute(""); ok {
		t.Fatal("空地址不得派生面板线路（退化为现状行为）")
	}
}

// expectedSha256 三方校验（面板源二期）：面板经控制通道下发的
// 期望哈希在场时是信任根——下载通道的二进制与 .sha256 成对
// 伪造也必须被它拦下；缺失时回落同源 .sha256 比对（旧口径）。
func TestVerifyUpgradeHashes(t *testing.T) {
	sum := sha256.Sum256([]byte("genuine-binary"))
	good := hex.EncodeToString(sum[:])
	fakeSum := sha256.Sum256([]byte("poisoned-binary"))
	fake := hex.EncodeToString(fakeSum[:])

	// 三方一致 -> 通过；大小写不敏感。
	if err := verifyUpgradeHashes(good, good, good); err != nil {
		t.Fatalf("三方一致应通过: %v", err)
	}
	if err := verifyUpgradeHashes(strings.ToUpper(good), good, good); err != nil {
		t.Fatalf("面板哈希大写形态应通过: %v", err)
	}
	// 下载通道成对伪造（binary 与 .sha256 同为 fake、自洽）：
	// 无面板哈希时这是旧口径的已知边界；有面板哈希必须拦截，
	// 且回执点名 expectedSha256 不符。
	if err := verifyUpgradeHashes("", fake, fake); err != nil {
		t.Fatalf("无面板哈希时同源自洽应按旧口径通过: %v", err)
	}
	err := verifyUpgradeHashes(good, fake, fake)
	if err == nil || !strings.Contains(err.Error(), "expectedSha256") {
		t.Fatalf("成对伪造必须被面板期望哈希拦截并点名, got: %v", err)
	}
	// 面板哈希与实际一致、但下载的 .sha256 被换 -> 交叉核对拦截
	// （沿用旧报错形态，不点名 expectedSha256）。
	err = verifyUpgradeHashes(good, fake, good)
	if err == nil || strings.Contains(err.Error(), "expectedSha256") {
		t.Fatalf(".sha256 交叉核对不符应按旧形态报错, got: %v", err)
	}
	// 无面板哈希、.sha256 与实际不符 -> 旧口径拦截。
	if err := verifyUpgradeHashes("", fake, good); err == nil {
		t.Fatal("无面板哈希时 .sha256 不符必须拦截")
	}
}
