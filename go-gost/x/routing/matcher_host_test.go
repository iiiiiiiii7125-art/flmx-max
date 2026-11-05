package routing

import (
	"testing"

	"github.com/go-gost/core/routing"
)

// D3-02 trigger 2: a whitespace-only Host value passes the rule
// parser's non-empty check, but host() TrimSpace's it to "", and
// the built matcher then panics on host[0] at the first matched
// request. Blank Host values must be rejected at build time.
func TestNewMatcherRejectsBlankHostValue(t *testing.T) {
	for _, rule := range []string{`Host(" ")`, `Host("  ")`, "Host(\" \t\")"} {
		m, err := NewMatcher(rule)
		if err == nil {
			// Build a match attempt so a regression to the
			// panicking closure is caught here, not in prod.
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("rule %s: Match panicked: %v", rule, r)
					}
				}()
				_ = m.Match(&routing.Request{Host: "example.com:443"})
			}()
			t.Fatalf("rule %s: blank Host value accepted, want error", rule)
		}
	}
}

func TestHostMatcherNormalRulesStillWork(t *testing.T) {
	m, err := NewMatcher(`Host("example.com")`)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match(&routing.Request{Host: "example.com:443"}) {
		t.Fatal("Host(example.com) should match example.com:443")
	}
	if m.Match(&routing.Request{Host: "other.com:443"}) {
		t.Fatal("Host(example.com) should not match other.com:443")
	}

	w, err := NewMatcher(`Host("*.example.com")`)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Match(&routing.Request{Host: "a.example.com:443"}) {
		t.Fatal("Host(*.example.com) should match a.example.com:443")
	}
}
