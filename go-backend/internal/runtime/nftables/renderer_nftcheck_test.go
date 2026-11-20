package nftables

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// R-B01-01 校验门：RenderTable 的输出必须是 nft 能接受的真实 ruleset。
// 此前的测试只断言渲染字符串，`tcp ip daddr …` 这样的顺序错误从未被
// 语法校验发现。本测试把渲染结果喂给真实的 `nft -c -f`，只有退出码
// 为 0 才算通过。
//
// 环境说明：nft -c 即使在 check-only 模式下也会初始化 netlink cache，
// 无 netlink 权限的环境（如本沙箱）下它对合法 ruleset 也会报
// `cache initialization failed: Operation not permitted` 且退出码非 0。
//
// R18-C3-07：本门拆成两半，跳过不再等于覆盖——
// (a) 解析断言（渲染字符串里 L3 匹配在 L4 匹配之前、禁 `tcp ip daddr`
// 子串）放在 TestRenderTableBindMatchOrder（fix_c3_07_test.go），
// 任何环境都跑；
// (b) 完整的 `nft -c` 语义校验仍在本测试里，但跳过即显性缺口：
// 默认仍 skip（沙箱/无 netlink 的 CI 容器跑全量套件时不误报），
// 而有 netlink 的 CI 任务/测试机必须以 FLMX_REQUIRE_NFT_CHECK=1
// 运行本测试——此时 nft 不可用或 netlink 初始化失败即硬失败，
// 绝不以 SKIP 假装通过。输出里一旦出现 `syntax error` 就是硬
// 失败，与环境无关——解析错误在 netlink 初始化之前报出。
func TestRenderTablePassesNftCheck(t *testing.T) {
	// nftCheckRequired reports whether this run must actually
	// execute the nft -c leg (R18-C3-07): set
	// FLMX_REQUIRE_NFT_CHECK=1 on the netlink-capable CI job /
	// test machine. Without it, an environment that cannot run
	// nft -c skips loudly (COVERAGE GAP) instead of failing —
	// the honest default for sandbox/CI-container runs.
	nftCheckRequired := os.Getenv("FLMX_REQUIRE_NFT_CHECK") == "1"
	nft, err := exec.LookPath("nft")
	if err != nil {
		if nftCheckRequired {
			t.Fatal("FLMX_REQUIRE_NFT_CHECK=1 but the nft binary is not available: the nft -c gate cannot run in this environment")
		}
		t.Skip("nft binary not available in this environment: real nft -c check not performed (COVERAGE GAP: R18-C3-07)")
	}
	cases := []struct {
		name string
		plan NodePlan
	}{
		{
			name: "with-bind-ipv4",
			plan: NodePlan{
				NodeID: 10,
				Rules: []Rule{{
					ForwardID:  42,
					InPort:     24000,
					BindIP:     "192.0.2.10",
					TargetHost: "198.51.100.20",
					TargetPort: 443,
					Protocols:  []string{"tcp", "udp"},
				}},
			},
		},
		{
			// 注意：v6 用例必须用 v6 目标地址——v6 BindIP + v4 目标的
			// 混用组合渲染出的 `ip6 daddr … dnat ip to …` 会被 nft
			// 以 `conflicting protocols: ip6 vs. ip` 拒绝；该组合
			// 已由 R18-C3-03 在 intake（validateNftablesForward-
			// Request）与渲染（ValidatePlan）双点拦截，见
			// fix_c3_03_test.go，本门只覆盖合法的同族组合。
			name: "with-bind-ipv6",
			plan: NodePlan{
				NodeID: 10,
				Rules: []Rule{{
					ForwardID:  43,
					InPort:     24001,
					BindIP:     "2001:db8::10",
					TargetHost: "2001:db8::20",
					TargetPort: 8443,
					Protocols:  []string{"tcp"},
				}},
			},
		},
		{
			name: "without-bind",
			plan: NodePlan{
				NodeID: 10,
				Rules: []Rule{{
					ForwardID:  44,
					InPort:     24002,
					TargetHost: "198.51.100.20",
					TargetPort: 443,
					Protocols:  []string{"tcp"},
				}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := RenderTable(tc.plan)
			f, err := os.CreateTemp("", "flmx-nftcheck-*.nft")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(f.Name())
			if _, err := f.WriteString(script); err != nil {
				f.Close()
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(nft, "-c", "-f", f.Name()).CombinedOutput()
			text := string(out)
			if strings.Contains(text, "syntax error") {
				t.Fatalf("nft -c rejected rendered ruleset:\n%s\n--- ruleset ---\n%s", text, script)
			}
			if err != nil {
				if strings.Contains(text, "cache initialization failed") {
					if nftCheckRequired {
						t.Fatalf("FLMX_REQUIRE_NFT_CHECK=1 but nft -c cannot complete in this environment (no netlink access: %q); the semantic half of this gate never ran", strings.TrimSpace(text))
					}
					t.Skipf("nft -c cannot complete in this environment (no netlink access: %q); real check must run on a machine with netlink access (test machine) (COVERAGE GAP: R18-C3-07)", strings.TrimSpace(text))
				}
				t.Fatalf("nft -c exited non-zero: %v\noutput:\n%s\n--- ruleset ---\n%s", err, text, script)
			}
		})
	}
}

// TestRenderTableMixedFamilyRejectedByNftCheck renders the
// mixed-family combination (v6 BindIP + v4 target) that
// ValidatePlan/intake now refuse (R18-C3-03) and asserts nft
// itself rejects the ruleset — the semantic half of the gate
// (R18-C3-07). A semantic rejection only happens after netlink
// initialization, so in a no-netlink environment this subtest
// skips (or fails hard with FLMX_REQUIRE_NFT_CHECK=1) instead of
// pretending; it must never pass by accepting the ruleset.
func TestRenderTableMixedFamilyRejectedByNftCheck(t *testing.T) {
	nftCheckRequired := os.Getenv("FLMX_REQUIRE_NFT_CHECK") == "1"
	nft, err := exec.LookPath("nft")
	if err != nil {
		if nftCheckRequired {
			t.Fatal("FLMX_REQUIRE_NFT_CHECK=1 but the nft binary is not available: the nft -c gate cannot run in this environment")
		}
		t.Skip("nft binary not available in this environment (COVERAGE GAP: R18-C3-07)")
	}
	plan := NodePlan{
		NodeID: 10,
		Rules: []Rule{{
			ForwardID:  45,
			InPort:     24003,
			BindIP:     "2001:db8::10",
			TargetHost: "198.51.100.20",
			TargetPort: 443,
			Protocols:  []string{"tcp"},
		}},
	}
	script := RenderTable(plan)
	f, err := os.CreateTemp("", "flmx-nftcheck-mixed-*.nft")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(nft, "-c", "-f", f.Name()).CombinedOutput()
	text := string(out)
	if strings.Contains(text, "cache initialization failed") {
		if nftCheckRequired {
			t.Fatalf("FLMX_REQUIRE_NFT_CHECK=1 but nft -c cannot complete in this environment (no netlink access: %q); the mixed-family semantic rejection never ran", strings.TrimSpace(text))
		}
		t.Skipf("nft -c cannot complete in this environment (no netlink access: %q) (COVERAGE GAP: R18-C3-07)", strings.TrimSpace(text))
	}
	if err == nil {
		t.Fatalf("nft -c accepted a mixed-family ruleset (v6 BindIP + v4 target) it must reject with conflicting protocols:\n--- ruleset ---\n%s", script)
	}
	t.Logf("nft -c rejected the mixed-family ruleset as expected:\n%s", strings.TrimSpace(text))
}
