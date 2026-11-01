package repo

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// R4B09-001 (KR3): NormalizeSubscriptionFrontURLs is the single
// implementation consumed by both the save side
// (handler.normalizeAndValidateConfigValue via its delegation alias)
// and the restore side (importConfigs). This corpus mirrors the
// handler-side KU6 corpus (handler/subscription_front_test.go) case
// for case with identical expectations, so the two faces stay locked
// to the same verdicts and normalized outputs.

func TestR4KR3NormalizeSubscriptionFrontURLsValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single", `["https://sub.0v0.dog"]`, []string{"https://sub.0v0.dog"}},
		{"trailing slash stripped", `["https://sub.0v0.dog/"]`, []string{"https://sub.0v0.dog"}},
		{"case normalized", `["HTTPS://SUB.0v0.DOG/"]`, []string{"https://sub.0v0.dog"}},
		{"port kept", `["http://192.0.2.10:8080"]`, []string{"http://192.0.2.10:8080"}},
		{"ipv6 literal", `["https://[2001:db8::1]:8443"]`, []string{"https://[2001:db8::1]:8443"}},
		{"multi order preserved", `["https://b.example.com","https://a.example.com"]`,
			[]string{"https://b.example.com", "https://a.example.com"}},
		{"dedupe exact", `["https://a.example.com","https://a.example.com"]`, []string{"https://a.example.com"}},
		{"dedupe after normalize", `["https://a.example.com/","https://A.example.com"]`, []string{"https://a.example.com"}},
		{"whitespace trimmed", `["  https://a.example.com  "]`, []string{"https://a.example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSubscriptionFrontURLs(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var decoded []string
			if err := json.Unmarshal([]byte(got), &decoded); err != nil {
				t.Fatalf("result is not a JSON array: %q (%v)", got, err)
			}
			if strings.Join(decoded, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", decoded, tc.want)
			}
		})
	}
}

func TestR4KR3NormalizeSubscriptionFrontURLsClearable(t *testing.T) {
	for _, in := range []string{"", "   ", "[]"} {
		got, err := NormalizeSubscriptionFrontURLs(in)
		if err != nil {
			t.Fatalf("input %q: unexpected error: %v", in, err)
		}
		if got != "" {
			t.Fatalf("input %q: got %q, want empty (clear)", in, got)
		}
	}
}

func TestR4KR3NormalizeSubscriptionFrontURLsInvalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"not json", `https://a.example.com`},
		{"json object not array", `{"url":"https://a.example.com"}`},
		{"non-string element", `[42]`},
		{"empty element", `[""]`},
		{"missing scheme", `["a.example.com"]`},
		{"bad scheme", `["ftp://a.example.com"]`},
		{"with path", `["https://a.example.com/sub"]`},
		{"with double slash path", `["https://a.example.com//"]`},
		{"with query", `["https://a.example.com/?x=1"]`},
		{"with fragment", `["https://a.example.com/#f"]`},
		{"with userinfo", `["https://user:pass@a.example.com"]`},
		{"empty label host", `["https://a..example.com"]`},
		{"trailing dot host", `["https://a.example.com."]`},
		{"missing host", `["https://"]`},
		{"port zero", `["https://a.example.com:0"]`},
		{"port too large", `["https://a.example.com:99999"]`},
		{"one bad among good", `["https://a.example.com","a.example.com"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSubscriptionFrontURLs(tc.in)
			if err == nil {
				t.Fatalf("input %s: expected rejection, got %q", tc.in, got)
			}
		})
	}
}

func TestR4KR3NormalizeSubscriptionFrontURLsCap(t *testing.T) {
	entries := make([]string, 0, MaxSubscriptionFrontURLs+1)
	for i := 0; i <= MaxSubscriptionFrontURLs; i++ {
		entries = append(entries, fmt.Sprintf(`"https://f%d.example.com"`, i))
	}
	raw := "[" + strings.Join(entries, ",") + "]"
	if _, err := NormalizeSubscriptionFrontURLs(raw); err == nil {
		t.Fatalf("expected cap rejection for %d entries", MaxSubscriptionFrontURLs+1)
	}
	// Exactly at the cap passes.
	raw = "[" + strings.Join(entries[:MaxSubscriptionFrontURLs], ",") + "]"
	if _, err := NormalizeSubscriptionFrontURLs(raw); err != nil {
		t.Fatalf("at-cap input rejected: %v", err)
	}
	// Cap is judged after dedupe: over-cap raw input that collapses
	// to the cap passes (same as the save side).
	dupes := make([]string, 0, MaxSubscriptionFrontURLs+1)
	for i := 0; i < MaxSubscriptionFrontURLs; i++ {
		dupes = append(dupes, fmt.Sprintf(`"https://f%d.example.com"`, i))
	}
	dupes = append(dupes, `"https://F0.example.com/"`)
	raw = "[" + strings.Join(dupes, ",") + "]"
	got, err := NormalizeSubscriptionFrontURLs(raw)
	if err != nil {
		t.Fatalf("dedupe-to-cap input rejected: %v", err)
	}
	var decoded []string
	if err := json.Unmarshal([]byte(got), &decoded); err != nil || len(decoded) != MaxSubscriptionFrontURLs {
		t.Fatalf("dedupe-to-cap: decoded %v (err %v), want %d entries", decoded, err, MaxSubscriptionFrontURLs)
	}
}
