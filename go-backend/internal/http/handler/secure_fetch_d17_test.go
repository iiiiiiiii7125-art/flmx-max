package handler

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// R5-B5B D17: SecureFetch 分级策略。
// 可达性证据：D16 强制 https 打挂既有 http 订阅（契约破坏）。

func TestD17_PublicHTTPrejected(t *testing.T) {
	// 公网 http → 拒绝，任何配置下。
	os.Unsetenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS")
	os.Unsetenv("FLVX_FETCH_PRIVATE_ALLOWLIST")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 用 IP 字面量避免 DNS（8.8.8.8 是公网）。
	if _, err := SecureFetch(ctx, "http://8.8.8.8/test"); err == nil {
		t.Error("public http should be rejected")
	}
}

func TestD17_PrivateRequiresAllowlist(t *testing.T) {
	os.Unsetenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS")
	os.Unsetenv("FLVX_FETCH_PRIVATE_ALLOWLIST")
	defer os.Unsetenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS")
	defer os.Unsetenv("FLVX_FETCH_PRIVATE_ALLOWLIST")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 私网 http 未配置 → 拒绝。
	if _, err := SecureFetch(ctx, "http://192.168.1.1/test"); err == nil {
		t.Error("private http without allowlist should be rejected")
	}

	// 配置 allowlist → validateFetchURL 通过（拨号会失败，但校验通过）。
	os.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "192.168.0.0/16")
	u, err := validateFetchURL("http://192.168.1.1/test")
	if err != nil {
		t.Errorf("private http with allowlist should pass validation: %v", err)
	}
	if u == nil {
		t.Error("expected URL")
	}
}

func TestD17_MetadataAlwaysRejected(t *testing.T) {
	// 元数据地址：任何配置下都拒绝。
	os.Setenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	os.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "169.254.0.0/16")
	defer os.Unsetenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS")
	defer os.Unsetenv("FLVX_FETCH_PRIVATE_ALLOWLIST")

	// ::ffff:169.254.169.254 展开后仍拒绝。
	ip := net.ParseIP("::ffff:169.254.169.254")
	if !isMetadataAddress(ip) {
		t.Error("::ffff:169.254.169.254 should be detected as metadata")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := SecureFetch(ctx, "http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Error("metadata address should always be rejected")
	}
}

func TestD17_MixedIPsRejected(t *testing.T) {
	// 解析返回 [公网, 私网] → 整体拒绝（逐地址判定）。
	// 模拟：validateFetchIP 对每个 IP 单独判定。
	if err := validateFetchIP(net.ParseIP("8.8.8.8"), "https://example.com", "https"); err != nil {
		t.Errorf("public https should pass: %v", err)
	}
	os.Unsetenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS")
	os.Unsetenv("FLVX_FETCH_PRIVATE_ALLOWLIST")
	if err := validateFetchIP(net.ParseIP("10.0.0.1"), "https://example.com", "https"); err == nil {
		t.Error("private without allowlist should be rejected")
	}
}

func TestD17_PublicHTTPSAllowed(t *testing.T) {
	// 公网 https → validateFetchURL 通过。
	u, err := validateFetchURL("https://8.8.8.8/test")
	if err != nil {
		t.Errorf("public https should pass: %v", err)
	}
	if u == nil {
		t.Error("expected URL")
	}
}

func TestD17_AllowlistNotUpgradePublic(t *testing.T) {
	// allowlist 不提升公网协议：公网 http 仍然拒绝。
	os.Setenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	defer os.Unsetenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := SecureFetch(ctx, "http://8.8.8.8/test"); err == nil {
		t.Error("public http should be rejected even with private allowlist")
	}
}
