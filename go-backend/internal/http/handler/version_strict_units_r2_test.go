package handler

import "testing"

// R2-016 严格变体逐函数钉死（与门级用例互补）：豁免集（空串/latest）
// 不视为过低；具体版本串必须完整解析才进入数值比较，缺段/段内垃圾
// 一律视为低于下限；数值比较与宽松版同口径（四段按前三段、padded
// 缺段补 0）。
func TestVersionBelowStrictUnits(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"1.14", false},
		{"1.13", true},
		{"1.14.2", false}, // 三段取前两段，与宽松版同口径
		{"1", true},       // 缺段：硬门不满足
		{"1.14-rc1", true},
		// R4-06 扩格：第三段是语义段（修订号），在席即须纯数字——
		// 后缀/空段一律畸形，不得绕过两段下限（修复前全放行）。
		{"1.14.0", false},       // 纯数字第三段恰达下限，维持放行
		{"1.14.0-rc1", true},    // 第三段带后缀
		{"1.14.2-rc1", true},    // 第三段带后缀（数值段达标亦畸形）
		{"1.15.0-rc1", true},    // 数值高于下限、第三段带后缀仍畸形（与三段严格变体同口径）
		{"v1.14.0-rc1", true},   // v 前缀形态同判
		{"1.14.", true},         // 第三段在席为空（与 "25..5" 空段格同口径）
		{"1.13.0-rc1", true},    // 数值本就低于下限，结论不变
		{"1.14.0.5", false},     // 第四段起为发行后缀、按家族契约忽略（与三段/补段严格变体一致）
		{"1.14.0.5-rc1", false}, // 第四段带后缀同为发行后缀、不消费，达下限放行（R6-06 补钉）
		{"1.13.0.5-rc1", true},  // 前两段低于下限，第四段带后缀不改变判定、仍拒（R6-06 补钉）
		{"latest", false},
		{"", false},
		{" 1.13 ", true}, // TrimSpace 后数值比较
	} {
		if got := versionBelowStrict(tc.version, 1, 14); got != tc.want {
			t.Errorf("versionBelowStrict(%q, 1, 14) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestVersionBelow3StrictUnits(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"25.9.5", false},
		{"25.9.4", true},
		{"25.9", true},          // 台账点名形态：缺修订段
		{"25.5.16-rc1", true},   // 台账点名形态：段内带后缀
		{"25.9.5.1", false},     // 四段按前三段，恰达下限
		{"25.9.4.9", true},      // 四段按前三段，低于下限
		{"25.9.5.1-rc1", false}, // 第四段带后缀不消费，恰达下限放行（R6-06 补钉）
		{"25.9.4.9-rc1", true},  // 第四段带后缀不消费，低于下限仍拒（R6-06 补钉）
		{"latest", false},
		{"", false},
	} {
		if got := versionBelow3Strict(tc.version, 25, 9, 5); got != tc.want {
			t.Errorf("versionBelow3Strict(%q, 25, 9, 5) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestVersionBelowPaddedStrictUnits(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"25.6.8", false},
		{"25.6", true},          // 缺段补 0 的既有口径不变
		{"25.9", false},         // 25.9.0 数值达标
		{"25.5.16-rc1", true},   // 台账点名形态
		{"25.6.x", true},        // 与宽松版 TestVersionBelowFourSegmentVersions 的同形态结论相反——这正是本条 BC 的收紧点
		{"25.6.8.9", false},     // 四段按前三段
		{"25.6.8.9-rc1", false}, // 第四段带后缀不消费，恰达下限放行（R6-06 补钉）
		{"25.6.7.9-rc1", true},  // 第四段带后缀不消费，低于下限仍拒（R6-06 补钉）
		{"latest", false},
		{"", false},
	} {
		if got := versionBelowPaddedStrict(tc.version, 25, 6, 8); got != tc.want {
			t.Errorf("versionBelowPaddedStrict(%q, 25, 6, 8) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestVersionFloorExempt(t *testing.T) {
	for _, v := range []string{"", "latest", " latest ", "Latest"} {
		if !versionFloorExempt(v) {
			t.Errorf("versionFloorExempt(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"25.9", "1.14", "dev"} {
		if versionFloorExempt(v) {
			t.Errorf("versionFloorExempt(%q) = true, want false", v)
		}
	}
}
