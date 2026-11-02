package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spool 生产命名派生钉死（AG-07）＋身份归一化与孤儿点名（A-T-04）。
// 此前 spool 测试全部自带显式 spoolPath 注入、绕过生产派生：派生
// 若被误改（哈希漏掉 secret、归一化口径漂移），行为测试全绿而身
// 份隔离语义静默变。本文件以金值与等价类直接钉住派生函数本身。

func TestTrafficSpoolFileNamePinnedGolden(t *testing.T) {
	// 金值＝sha256("panel.example.com\x00test-secret")，独立以
	// sha256sum 算出后写死：派生公式任何变动都会在此变红。
	const want = "flmx_traffic_9c7fe79f025a358a21ffca758fcb6dab37fcbe7ec44ca5801ea350eec6b6ce67.json"
	if got := trafficSpoolFileName(TrafficSpoolIdentity("panel.example.com", "test-secret")); got != want {
		t.Fatalf("生产命名派生漂移: got %s, want %s", got, want)
	}
}

func TestTrafficSpoolIdentitySamePanelSameFile(t *testing.T) {
	// 同一面板的地址写法变体必须派生同一 spool 文件：裸 host、
	// 带 scheme、带尾缀路径、ws/wss 形态——改写法不弃旧 spool。
	base := TrafficSpoolIdentity("panel.example.com", "test-secret")
	for _, addr := range []string{
		"panel.example.com",
		"https://panel.example.com",
		"https://panel.example.com/",
		"https://panel.example.com/sub/path",
		"wss://panel.example.com",
		"ws://panel.example.com",
		" panel.example.com ",
	} {
		if got := TrafficSpoolIdentity(addr, "test-secret"); got != base {
			t.Fatalf("地址写法 %q 应与裸 host 同身份: got %q, want %q", addr, got, base)
		}
	}
	// host:port 一族同理同源。
	withPort := TrafficSpoolIdentity("panel.example.com:8443", "test-secret")
	for _, addr := range []string{
		"https://panel.example.com:8443",
		"https://panel.example.com:8443/prefix",
	} {
		if got := TrafficSpoolIdentity(addr, "test-secret"); got != withPort {
			t.Fatalf("地址写法 %q 应与裸 host:port 同身份: got %q, want %q", addr, got, withPort)
		}
	}
	if withPort == base {
		t.Fatal("不同端口必须不同身份（端口是面板区分面）")
	}
}

func TestTrafficSpoolIdentityIsolatesPanelAndSecret(t *testing.T) {
	base := TrafficSpoolIdentity("panel.example.com", "test-secret")
	if got := TrafficSpoolIdentity("panel.example.com", "other-secret"); got == base {
		t.Fatal("换 secret 必须换 spool 身份（防串账）")
	}
	if got := TrafficSpoolIdentity("other.example.com", "test-secret"); got == base {
		t.Fatal("换面板必须换 spool 身份（防串账）")
	}
	if trafficSpoolFileName(base) == trafficSpoolFileName(TrafficSpoolIdentity("panel.example.com", "other-secret")) {
		t.Fatal("异 secret 的 spool 文件名必须不同")
	}
}

func TestConfigureTrafficSpoolKeepsPendingAcrossAddrRewrite(t *testing.T) {
	orig := configuredTrafficSpool
	defer func() { configuredTrafficSpool = orig }()

	dir := t.TempDir()
	// 第一代进程：裸 host 形态配置，攒下一批待确认流量。
	if err := ConfigureTrafficSpool(dir, TrafficSpoolIdentity("panel.example.com", "test-secret")); err != nil {
		t.Fatal(err)
	}
	first := configuredTrafficSpool
	first.state.Pending = &trafficEnvelope{
		Stream: first.state.Stream, Sequence: first.state.Next,
		Items: []TrafficReportItem{{N: "svc-a", U: 11, D: 22}},
	}
	if err := first.save(first.state); err != nil {
		t.Fatal(err)
	}

	// 第二代进程：同面板改用带 scheme 的地址写法——必须命中同一
	// spool、待确认批次原样保留，且不产生孤儿点名。
	if err := ConfigureTrafficSpool(dir, TrafficSpoolIdentity("https://panel.example.com/", "test-secret")); err != nil {
		t.Fatal(err)
	}
	second := configuredTrafficSpool
	if second.path != first.path {
		t.Fatalf("改地址写法应命中同一 spool 文件: %s vs %s", second.path, first.path)
	}
	if second.state.Pending == nil || len(second.state.Pending.Items) != 1 ||
		second.state.Pending.Items[0].U != 11 || second.state.Pending.Items[0].D != 22 {
		t.Fatalf("待确认批次应跨改址写法保留: %+v", second.state.Pending)
	}
	if orphans := findOrphanTrafficSpools(dir, second.path); len(orphans) != 0 {
		t.Fatalf("同面板改写法不应产生孤儿: %v", orphans)
	}
}

func TestConfigureTrafficSpoolSecretRotationNamesOrphan(t *testing.T) {
	orig := configuredTrafficSpool
	defer func() { configuredTrafficSpool = orig }()

	dir := t.TempDir()
	if err := ConfigureTrafficSpool(dir, TrafficSpoolIdentity("panel.example.com", "old-secret")); err != nil {
		t.Fatal(err)
	}
	oldPath := configuredTrafficSpool.path

	// secret 轮换：新身份新 spool 正常起步；旧 spool 被点名（带
	// 大小）但不被删除、不被并账。
	if err := ConfigureTrafficSpool(dir, TrafficSpoolIdentity("panel.example.com", "new-secret")); err != nil {
		t.Fatal(err)
	}
	newSpool := configuredTrafficSpool
	if newSpool.path == oldPath {
		t.Fatal("轮换 secret 必须换 spool 文件")
	}
	if newSpool.state.Pending != nil || newSpool.state.Next != 1 {
		t.Fatalf("新 spool 应干净起步: %+v", newSpool.state)
	}
	orphans := findOrphanTrafficSpools(dir, newSpool.path)
	if len(orphans) != 1 || !strings.HasPrefix(orphans[0], filepath.Base(oldPath)) {
		t.Fatalf("旧 spool 应被点名为孤儿: %v", orphans)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("孤儿 spool 不得被自动删除: %v", err)
	}

	// 隔离留证存档不算孤儿 spool，不点名。
	archive := oldPath + ".rejected-" + strings.Repeat("0", 32) + "-1.json"
	if err := os.WriteFile(archive, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if orphans := findOrphanTrafficSpools(dir, newSpool.path); len(orphans) != 1 {
		t.Fatalf("隔离存档不应计入孤儿: %v", orphans)
	}
}
