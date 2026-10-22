package handler

import "testing"

// R4-N1 回归：node.version 存的是 agent 握手上报的 fullVersion，形如
// "1.3.0 (debian/amd64)"（go-gost/main.go 口径：版本 + " (发行版/架构)"）。
// FIX-G 把 compareVersions 严格化后，带后缀的存量版本串处处不可比：
// 面板内 agent 升级/批量升级被 fail-closed 全拒、federation 的
// flmx-no-private 版本门对真实节点恒 false。
// 修复口径：被比较的「上报型版本串」先经 extractLeadingSemver 提取
// 首段语义版本再走严格比较；真正不可解析（无数字段）仍 fail-closed。
// 用户输入的目标版本不经本路径，严格语义不变（见 compare_versions_test.go）。

func TestExtractLeadingSemver(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"fullVersion debian", "1.3.0 (debian/amd64)", "1.3.0", true},
		{"fullVersion arm64", "1.3.3 (linux/arm64)", "1.3.3", true},
		{"pure semver", "1.3.3", "1.3.3", true},
		{"v prefix", "v1.3.2", "1.3.2", true},
		{"surrounding spaces", "  1.3.1 (alpine)  ", "1.3.1", true},
		{"dev passthrough", "dev", "dev", true},
		{"empty", "", "", false},
		{"blank", "   ", "", false},
		{"garbage", "not-a-version", "", false},
		{"garbage with spaces", "hello (world)", "", false},
		{"suffix glued no space", "1.3.2-beta1", "", false},
		{"empty segment", "1..2 (debian/amd64)", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractLeadingSemver(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("extractLeadingSemver(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCompareWithReportedVersion(t *testing.T) {
	cases := []struct {
		name     string
		target   string
		reported string
		want     int
		wantErr  bool
	}{
		{"upgrade over fullVersion", "9.9.9", "1.3.0 (debian/amd64)", 1, false},
		{"downgrade vs fullVersion", "1.2.0", "1.3.0 (debian/amd64)", -1, false},
		{"equal fullVersion", "1.3.3", "1.3.3 (linux/arm64)", 0, false},
		{"pure reported", "1.3.3", "1.3.2", 1, false},
		{"dev reported is lowest", "1.3.3", "dev", 1, false},
		{"empty reported fails closed", "1.3.3", "", 0, true},
		{"garbage reported fails closed", "1.3.3", "garbage!", 0, true},
		{"strict target still enforced", "1.3.3-beta", "1.3.0 (debian/amd64)", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := compareWithReportedVersion(tc.target, tc.reported)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("compareWithReportedVersion(%q, %q) = %d, want error", tc.target, tc.reported, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("compareWithReportedVersion(%q, %q) unexpected error: %v", tc.target, tc.reported, err)
			}
			if got != tc.want {
				t.Fatalf("compareWithReportedVersion(%q, %q) = %d, want %d", tc.target, tc.reported, got, tc.want)
			}
		})
	}
}

// bypass 版本门：真实节点版本串（带后缀）达到基线必须放行下发；
// dev/未知/垃圾仍按不支持处理（主控裁定的 fail-closed 口径不变）。
func TestAgentSupportsNoPrivateBypassFullVersion(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"1.3.3 (debian/amd64)", true},
		{"1.3.4 (linux/arm64)", true},
		{"1.3.3", true},
		{"1.3.2 (debian/amd64)", false},
		{"1.3.0 (debian/amd64)", false},
		{"dev", false},
		{"", false},
		{"garbage", false},
	}
	for _, tc := range cases {
		if got := agentSupportsNoPrivateBypass(tc.in); got != tc.want {
			t.Fatalf("agentSupportsNoPrivateBypass(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// 联邦版本警告：对端上报的版本串带后缀时按首段判定；dev 豁免与
// 「不可解析不臆断」口径（FIX2-B 主控裁定）保持不变。
func TestFederationConsumerVersionWarningFullVersion(t *testing.T) {
	if w := federationConsumerVersionWarning("1.3.2 (debian/amd64)"); w == "" {
		t.Fatal("old peer reporting fullVersion should produce a version warning")
	}
	if w := federationConsumerVersionWarning("1.3.3 (debian/amd64)"); w != "" {
		t.Fatalf("current peer reporting fullVersion should not warn, got %q", w)
	}
	if w := federationConsumerVersionWarning("dev"); w != "" {
		t.Fatalf("dev peer must stay exempt, got %q", w)
	}
	if w := federationConsumerVersionWarning("garbage"); w != "" {
		t.Fatalf("unparseable peer version must not be asserted on, got %q", w)
	}
	if w := federationConsumerVersionWarning(""); w == "" {
		t.Fatal("peer reporting no version must warn (pre-auth consumer)")
	}
}
