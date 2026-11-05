package exchanger

import (
	"crypto/tls"
	"testing"

	xlogger "github.com/go-gost/x/logger"
)

// F1-01 regression: an address-only DoT/DoH resolver (no explicit TLS
// config supplied) must get a verifying TLS config, not a hardcoded
// InsecureSkipVerify:true one. Non-verification stays available only
// through an explicitly supplied TLS config.
func TestNewExchangerDefaultTLSConfigVerifies(t *testing.T) {
	for _, addr := range []string{
		"tls://1.1.1.1:853",
		"dot://dns.example.com:853",
		"https://dns.example.com/dns-query",
		"https://1.0.0.1/dns-query",
	} {
		ex, err := NewExchanger(addr, LoggerOption(xlogger.Nop()))
		if err != nil {
			t.Fatalf("NewExchanger(%q): %v", addr, err)
		}
		e := ex.(*exchanger)
		cfg := e.options.tlsConfig
		if cfg == nil {
			t.Fatalf("NewExchanger(%q): no TLS config built", addr)
		}
		if cfg.InsecureSkipVerify {
			t.Errorf("NewExchanger(%q): default TLS config has InsecureSkipVerify=true", addr)
		}
		if cfg.ServerName == "" {
			t.Errorf("NewExchanger(%q): default TLS config has empty ServerName", addr)
		}
		if cfg.MinVersion < tls.VersionTLS12 {
			t.Errorf("NewExchanger(%q): default TLS config MinVersion = %x, want >= TLS1.2", addr, cfg.MinVersion)
		}
	}
}

// An explicitly supplied TLS config must be honoured untouched,
// including an operator's deliberate InsecureSkipVerify opt-in.
func TestNewExchangerExplicitTLSConfigHonoured(t *testing.T) {
	custom := &tls.Config{InsecureSkipVerify: true, ServerName: "custom.example"}
	ex, err := NewExchanger("tls://1.1.1.1:853", TLSConfigOption(custom), LoggerOption(xlogger.Nop()))
	if err != nil {
		t.Fatalf("NewExchanger: %v", err)
	}
	if got := ex.(*exchanger).options.tlsConfig; got != custom {
		t.Fatalf("explicit TLS config was replaced: %v", got)
	}
}
