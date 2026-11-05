package dtls

import (
	"crypto/tls"
	"testing"
)

// F1-01 regression: with no TLS config supplied, the DTLS dialer must
// verify the peer. Previously it silently substituted
// InsecureSkipVerify:true. The expected server name is derived from
// the dialled address when the config does not carry one.
func TestEffectiveTLSConfigDefaults(t *testing.T) {
	cfg, serverName := effectiveTLSConfig(nil, "dtls.example.com:443")
	if cfg == nil {
		t.Fatal("effectiveTLSConfig returned nil config")
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("default TLS config has InsecureSkipVerify=true")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Fatalf("default TLS config MinVersion = %x, want >= TLS1.2", cfg.MinVersion)
	}
	if serverName != "dtls.example.com" {
		t.Fatalf("serverName = %q, want %q", serverName, "dtls.example.com")
	}
}

// An explicitly supplied TLS config must pass through untouched,
// including its ServerName.
func TestEffectiveTLSConfigExplicit(t *testing.T) {
	custom := &tls.Config{InsecureSkipVerify: true, ServerName: "pinned.example"}
	cfg, serverName := effectiveTLSConfig(custom, "other.example.com:443")
	if cfg != custom {
		t.Fatal("explicit TLS config was replaced")
	}
	if serverName != "pinned.example" {
		t.Fatalf("serverName = %q, want the config's pinned name", serverName)
	}
}
