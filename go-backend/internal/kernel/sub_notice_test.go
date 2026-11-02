package kernel

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestInfoNotices(t *testing.T) {
	expire := time.Date(2026, 12, 31, 23, 59, 59, 0, time.Local).UnixMilli()
	got := InfoNotices(expire, 3*1024*1024*1024, 10*1024*1024*1024)
	if len(got) != 2 || got[0] != "⏳ 到期 2026-12-31" || got[1] != "📊 剩余 7.00GB / 10.00GB" {
		t.Fatalf("info notices: %v", got)
	}
	// No expiry and no limit: long-term + used only.
	got = InfoNotices(0, 512, 0)
	if got[0] != "⏳ 长期有效" || got[1] != "📊 已用 512B" {
		t.Fatalf("unlimited notices: %v", got)
	}
	// Over-used clamps the remainder at zero.
	got = InfoNotices(0, 2048, 1024)
	if got[1] != "📊 剩余 0B / 1.00KB" {
		t.Fatalf("over-used notices: %v", got)
	}
}

var allSubFormats = []string{
	FormatBase64, FormatClash, FormatStash, FormatSingbox,
	FormatSurge, FormatLoon, FormatQX, FormatShadowrocket,
}

// TestSubscriptionInfoNoticesReachEveryFormat renders an active
// subscription with info nodes on: both notices must be present in the
// output of every supported format, in front of the real nodes.
func TestSubscriptionInfoNoticesReachEveryFormat(t *testing.T) {
	eps := shareFixtures(t)
	notices := InfoNotices(time.Date(2027, 1, 2, 0, 0, 0, 0, time.Local).UnixMilli(), 1024, 4096)
	for _, format := range allSubFormats {
		body, _, err := Subscription(format, eps, SubOptions{Notices: notices})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		text := body
		if format == FormatBase64 {
			raw, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				t.Fatalf("base64 decode: %v", err)
			}
			text = string(raw)
			lines := strings.Split(text, "\n")
			if !strings.HasPrefix(lines[0], "ss://") || !strings.Contains(lines[0], "127.0.0.1:1") {
				t.Fatalf("base64: first node must be the notice placeholder: %s", lines[0])
			}
		}
		for _, n := range notices {
			needle := n
			if format == FormatBase64 {
				needle = url.PathEscape(n)
			}
			if !strings.Contains(text, needle) {
				t.Fatalf("%s: notice %q missing from output:\n%s", format, n, text)
			}
		}
	}
}

// TestSubscriptionReasonNoticeOnly renders a stopped subscription: no
// real endpoints, just the reason placeholder — every format must carry
// it so the client replaces its node list with the explanation instead
// of keeping stale nodes.
func TestSubscriptionReasonNoticeOnly(t *testing.T) {
	reason := "⚠️ 订阅已停用，请联系管理员"
	for _, format := range allSubFormats {
		body, _, err := Subscription(format, nil, SubOptions{Notices: []string{reason}})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		text := body
		needle := reason
		if format == FormatBase64 {
			raw, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				t.Fatalf("base64 decode: %v", err)
			}
			text = string(raw)
			needle = url.PathEscape(reason)
		}
		if !strings.Contains(text, needle) {
			t.Fatalf("%s: reason notice missing:\n%s", format, text)
		}
	}
}

// TestSubscriptionNodeCountIgnoresNotices: the admin list counts real
// nodes; placeholders are not nodes.
func TestSubscriptionNodeCountIgnoresNotices(t *testing.T) {
	eps := shareFixtures(t)
	notices := InfoNotices(0, 1, 2)
	for _, format := range allSubFormats {
		plain := SubscriptionNodeCount(format, eps, SubOptions{})
		withNotices := SubscriptionNodeCount(format, eps, SubOptions{Notices: notices})
		if plain != withNotices {
			t.Fatalf("%s: count changed by notices: %d -> %d", format, plain, withNotices)
		}
	}
}
