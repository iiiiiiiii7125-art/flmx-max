package handler

import (
	"strings"
	"testing"
)

// A-01 (AUDIT-R215): 自定义空勾选回落须在保存回执点名。后端
// FilterEndpoints 语义不动（空集回落三档仍由
// user_subscription_test.go:190 钉死）；回执按请求里 relayIds
// 键在场且为空集判定此次回落，三档保存（键缺省）与非空勾选不点名。
func TestR215SaveReceiptNamesCustomEmptyRelayFallback(t *testing.T) {
	r, h := r202Setup(t)
	okSrc := r202ImportSource(t, r, h, "ok-src", false, r202LinkOK)

	save := func(name string, options map[string]interface{}) string {
		t.Helper()
		out := callSubscriptionSaveR4(t, h, map[string]interface{}{
			"name": "r215-" + name, "sourceIds": []int64{okSrc.ID}, "options": options,
		})
		if out.Code != 0 {
			t.Fatalf("save %s: code=%d msg=%q", name, out.Code, out.Msg)
		}
		return out.Warning
	}

	// 空集＋只要中转档：点名回落与档位。
	if w := save("empty-relay", map[string]interface{}{"lines": "relay", "relayIds": []int64{}}); !strings.Contains(w, "已选 0 个端点") || !strings.Contains(w, "「只要中转」") {
		t.Fatalf("empty+relay warning = %q, want fallback naming 只要中转", w)
	}
	// 空集＋全部档：点名回落与档位。
	if w := save("empty-all", map[string]interface{}{"lines": "", "relayIds": []int64{}}); !strings.Contains(w, "已选 0 个端点") || !strings.Contains(w, "「全部」") {
		t.Fatalf("empty+all warning = %q, want fallback naming 全部", w)
	}
	// 三档保存（relayIds 键缺省）：不点名。
	if w := save("tier-only", map[string]interface{}{"lines": "direct"}); w != "" {
		t.Fatalf("tier-only warning = %q, want empty", w)
	}
	// 非空勾选：不点名。
	if w := save("nonempty", map[string]interface{}{"lines": "", "relayIds": []int64{0}}); w != "" {
		t.Fatalf("non-empty whitelist warning = %q, want empty", w)
	}
}

// A-01 回执合并：纯中转点名与空集回落同属一个回执时两条并存。
func TestR215SaveReceiptJoinsRelayOnlyAndEmptyFallback(t *testing.T) {
	r, h := r202Setup(t)
	roSrc := r202ImportSource(t, r, h, "ro-src", true, r202LinkROa)
	out := callSubscriptionSaveR4(t, h, map[string]interface{}{
		"name": "r215-join", "sourceIds": []int64{roSrc.ID},
		"options": map[string]interface{}{"lines": "relay", "relayIds": []int64{}},
	})
	if out.Code != 0 {
		t.Fatalf("save join: code=%d msg=%q", out.Code, out.Msg)
	}
	if !strings.Contains(out.Warning, "纯中转导入节点") || !strings.Contains(out.Warning, "已选 0 个端点") {
		t.Fatalf("joined warning = %q, want both relay-only naming and empty fallback", out.Warning)
	}
}
