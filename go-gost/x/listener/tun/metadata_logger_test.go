package tun

import (
	"testing"

	xmetadata "github.com/go-gost/x/metadata"
)

// R8-B-01: parseMetadata builds the internal router's LoggerOption
// from logger.Default(); without a registered default logger it must
// not panic. This test deliberately does NOT call SetDefault and
// needs no TUN device: parseMetadata only parses metadata.
func TestParseMetadataWithoutDefaultLogger(t *testing.T) {
	l := &tunListener{}
	md := xmetadata.NewMetadata(map[string]any{
		// GetStrings only accepts []string/[]any values.
		"routes": []string{"10.0.0.0/8 192.168.1.1"},
	})
	if err := l.parseMetadata(md); err != nil {
		t.Fatalf("parseMetadata: %v", err)
	}
	if len(l.routes) == 0 {
		t.Fatal("expected routes parsed from metadata")
	}
	if l.md.config == nil || l.md.config.Router == nil {
		t.Fatal("expected internal router built from routes")
	}
}
