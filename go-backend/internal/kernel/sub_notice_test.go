package kernel

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

var allSubFormats = []string{
	FormatBase64, FormatClash, FormatStash, FormatSingbox,
	FormatSurge, FormatLoon, FormatQX, FormatShadowrocket,
}

// TestSubscriptionNoticesReachEveryFormat renders an active
// subscription carrying placeholder notices (the SubOptions.Notices
// mechanism — the same one stopped subscriptions use for their
// reason): both notices must be present in the output of every
// supported format, in front of the real nodes.
// （2.1.2: the info-node builder InfoNotices left with the infoNodes
// option; the notice strings here are literals of the same shape.）
func TestSubscriptionNoticesReachEveryFormat(t *testing.T) {
	eps := shareFixtures(t)
	notices := []string{"⏳ 到期 2027-01-02", "📊 剩余 3.00KB / 4.00KB"}
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
	notices := []string{"⏳ 长期有效", "📊 已用 1B"}
	for _, format := range allSubFormats {
		plain := SubscriptionNodeCount(format, eps, SubOptions{})
		withNotices := SubscriptionNodeCount(format, eps, SubOptions{Notices: notices})
		if plain != withNotices {
			t.Fatalf("%s: count changed by notices: %d -> %d", format, plain, withNotices)
		}
	}
}
