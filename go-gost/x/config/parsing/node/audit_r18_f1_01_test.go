package node

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	_ "github.com/go-gost/x/connector/http"
	_ "github.com/go-gost/x/dialer/tcp"
	_ "github.com/go-gost/x/dialer/tls"
)

// recordingLogger captures Warn/Warnf lines for the R18-F1-01
// startup-warning tests; every other method is a no-op.
type recordingLogger struct {
	mu     sync.Mutex
	warns  []string
	errors []string
}

func (l *recordingLogger) WithFields(map[string]any) logger.Logger { return l }
func (l *recordingLogger) Trace(args ...any)                       {}
func (l *recordingLogger) Tracef(format string, args ...any)       {}
func (l *recordingLogger) Debug(args ...any)                       {}
func (l *recordingLogger) Debugf(format string, args ...any)       {}
func (l *recordingLogger) Info(args ...any)                        {}
func (l *recordingLogger) Infof(format string, args ...any)        {}
func (l *recordingLogger) Warn(args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprint(args...))
}
func (l *recordingLogger) Warnf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(format, args...))
}
func (l *recordingLogger) Error(args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, fmt.Sprint(args...))
}
func (l *recordingLogger) Errorf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}
func (l *recordingLogger) Fatal(args ...any)                   {}
func (l *recordingLogger) Fatalf(format string, args ...any)   {}
func (l *recordingLogger) GetLevel() logger.LogLevel           { return logger.InfoLevel }
func (l *recordingLogger) IsLevelEnabled(logger.LogLevel) bool { return true }

func (l *recordingLogger) warnedContaining(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, w := range l.warns {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestInsecureTLSDialWarningMatrix pins the warning predicate: only
// TLS-family dialers without Secure warn.
func TestInsecureTLSDialWarningMatrix(t *testing.T) {
	empty := &config.TLSConfig{}
	secure := &config.TLSConfig{Secure: true}
	for _, tc := range []struct {
		dialer string
		cfg    *config.TLSConfig
		want   bool
	}{
		{"tls", empty, true},
		{"mtls", empty, true},
		{"wss", empty, true},
		{"mwss", empty, true},
		{"TLS", empty, true},
		{"tls", secure, false},
		{"mtls", secure, false},
		{"tcp", empty, false},
		{"kcp", empty, false},
		{"udp", empty, false},
		{"ws", empty, false},
		{"mws", empty, false},
		{"mtcp", empty, false},
		{"tls", nil, false},
	} {
		msg, got := insecureTLSDialWarning("hop_1", tc.dialer, tc.cfg)
		if got != tc.want {
			t.Fatalf("insecureTLSDialWarning(%q, secure=%v) = %v, want %v (msg %q)", tc.dialer, tc.cfg != nil && tc.cfg.Secure, got, tc.want, msg)
		}
		if tc.want && (!strings.Contains(msg, "R18-F1-01") || !strings.Contains(msg, "hop_1")) {
			t.Fatalf("warning message must name the finding and the hop, got %q", msg)
		}
	}
}

// TestParseNodeWarnsOnUnverifiedTLSDial is the behavioral red/green
// test for R18-F1-01 item 3: a TLS dial hop rendered without
// verification must warn at load time, not stay silent. On pre-fix
// code ParseNode emits no warning and this test fails.
func TestParseNodeWarnsOnUnverifiedTLSDial(t *testing.T) {
	rec := &recordingLogger{}
	cfg := &config.NodeConfig{
		Name: "tls-hop",
		Addr: "192.0.2.1:8443",
		Dialer: &config.DialerConfig{
			Type: "tls",
		},
	}
	if _, err := ParseNode("hop_1", cfg, rec); err != nil {
		t.Fatalf("ParseNode with unverified TLS dialer: %v", err)
	}
	if !rec.warnedContaining("R18-F1-01") {
		t.Fatalf("expected an R18-F1-01 unverified-TLS warning, got warns=%v", rec.warns)
	}
	if !rec.warnedContaining("hop_1") {
		t.Fatalf("warning must name the hop, got warns=%v", rec.warns)
	}
}

// TestParseNodeSilentForVerifiedTLSDial: secure=true must not warn.
func TestParseNodeSilentForVerifiedTLSDial(t *testing.T) {
	rec := &recordingLogger{}
	cfg := &config.NodeConfig{
		Name: "tls-hop",
		Addr: "192.0.2.1:8443",
		Dialer: &config.DialerConfig{
			Type: "tls",
			TLS:  &config.TLSConfig{Secure: true},
		},
	}
	if _, err := ParseNode("hop_1", cfg, rec); err != nil {
		t.Fatalf("ParseNode with verified TLS dialer: %v", err)
	}
	if len(rec.warns) != 0 {
		t.Fatalf("verified TLS dialer must not warn, got warns=%v", rec.warns)
	}
}

// TestParseNodeSilentForTCPDial: a plain TCP dialer is not a TLS hop.
func TestParseNodeSilentForTCPDial(t *testing.T) {
	rec := &recordingLogger{}
	cfg := &config.NodeConfig{
		Name: "tcp-hop",
		Addr: "192.0.2.1:8080",
		Dialer: &config.DialerConfig{
			Type: "tcp",
		},
	}
	if _, err := ParseNode("hop_1", cfg, rec); err != nil {
		t.Fatalf("ParseNode with TCP dialer: %v", err)
	}
	if len(rec.warns) != 0 {
		t.Fatalf("TCP dialer must not warn, got warns=%v", rec.warns)
	}
}
