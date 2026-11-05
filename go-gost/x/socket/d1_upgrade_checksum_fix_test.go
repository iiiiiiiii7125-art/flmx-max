package socket

import (
	"strings"
	"testing"
)

// D1-05: UpgradeAgent parsed the panel-supplied checksumUrl field but
// never consumed it, while its sibling downloadUrl is hard-rejected.
// Under S-02 a URL is exactly the carrier that must not be trusted —
// a checksum fetched from a sender-supplied URL is same-source with
// the binary and proves nothing (cf. the A3-02 install.sh finding).
// The trusted carrier is the hash value itself, already supported as
// expectedSha256 with mandatory verification. checksumUrl therefore
// gets the same hard rejection as downloadUrl, before any staging,
// route probing or download work begins.

func TestD1UpgradeAgentRejectsChecksumURL(t *testing.T) {
	stubUpgradeInContainer(t, false)
	reporter := &WebSocketReporter{}

	for name, data := range map[string]map[string]interface{}{
		"valid version":   {"version": "1.9.9", "checksumUrl": "https://evil.example/gost.sha256"},
		"invalid version": {"version": "bogus", "checksumUrl": "https://evil.example/gost.sha256"},
	} {
		err := reporter.handleUpgradeAgent(data)
		if err == nil {
			t.Fatalf("%s: checksumUrl must be rejected, got nil error", name)
		}
		if !strings.Contains(err.Error(), "checksumUrl") {
			t.Fatalf("%s: rejection must name checksumUrl, got: %v", name, err)
		}
	}

	// Control: the same request without checksumUrl passes this gate
	// (it then fails on the version format, proving the checksumUrl
	// check — not some earlier failure — produced the rejections above).
	err := reporter.handleUpgradeAgent(map[string]interface{}{"version": "bogus"})
	if err == nil || strings.Contains(err.Error(), "checksumUrl") {
		t.Fatalf("control case without checksumUrl changed behavior, got: %v", err)
	}
}
