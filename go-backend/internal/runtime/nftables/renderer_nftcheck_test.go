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
// 这种情况下测试明确 skip（写清原因），不假装通过；真正的校验在
// 有 netlink 权限的测试机上跑通。但输出里一旦出现 `syntax error` 就是
// 硬失败，与环境无关——解析错误在 netlink 初始化之前报出。
func TestRenderTablePassesNftCheck(t *testing.T) {
	nft, err := exec.LookPath("nft")
	if err != nil {
		t.Skip("nft binary not available in this environment: real nft -c check not performed")
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
					t.Skipf("nft -c cannot complete in this environment (no netlink access: %q); real check must run on a machine with netlink access (test machine)", strings.TrimSpace(text))
				}
				t.Fatalf("nft -c exited non-zero: %v\noutput:\n%s\n--- ruleset ---\n%s", err, text, script)
			}
		})
	}
}
