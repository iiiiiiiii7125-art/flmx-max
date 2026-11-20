package kernel

import (
	"strings"
	"testing"
)

// R18-B5-02: a node name containing \r splits Surge/Loon/QX
// line-format subscriptions — the four name escapers replaced comma,
// "=" and "\n" but not "\r", while the value escapers already did.
// A name "A\rB" must render as "A B" on one line, never inject a CR.
func TestR18B502CRInNodeNameDoesNotSplitLineFormats(t *testing.T) {
	self := Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
		Settings: Settings{Method: "aes-128-gcm", Password: "pw"}}
	if err := Normalize(TypeSingbox, &self); err != nil {
		t.Fatal(err)
	}
	imp := Endpoint{Name: "A\rB", Host: "5.6.7.8", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "5.6.7.8", "port": 8388,
			"cipher": "aes-128-gcm", "password": "pw", "name": "A\rB"}}

	check := func(format, body, marker string) {
		t.Helper()
		if strings.Contains(body, "\r") {
			t.Fatalf("format %s: rendered body must not contain a bare CR: %q", format, body)
		}
		if !strings.Contains(body, "A B") {
			t.Fatalf("format %s: CR in name must render as a space: %q", format, body)
		}
		// The proxy line itself must appear exactly once (a CR in the
		// name must not smuggle a second line into the block); the
		// Surge group line derives its name from the escaped proxy
		// line, so it is covered by the no-CR assertion above.
		if n := strings.Count(body, marker); n != 1 {
			t.Fatalf("format %s: proxy line with escaped name must appear exactly once, got %d: %q", format, n, body)
		}
	}

	// Self-built path: SurgeLine.
	body, _, err := Subscription(FormatSurge,
		[]Endpoint{{Name: "A\rB", Host: "1.2.3.4", Port: 8388, Inbound: self, Direct: true}}, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	check("surge/self-built", body, "A B = ss, ")

	// Imported path: proxySurge (Surge), loonLine (Loon), qxLine (QX).
	markers := map[string]string{
		FormatSurge: "A B = ss, ",
		FormatLoon:  "A B=shadowsocks,",
		FormatQX:    "tag=A B",
	}
	for _, format := range []string{FormatSurge, FormatLoon, FormatQX} {
		body, _, err := Subscription(format, []Endpoint{imp}, SubOptions{})
		if err != nil {
			t.Fatal(err)
		}
		check(format+"/imported", body, markers[format])
	}
}
