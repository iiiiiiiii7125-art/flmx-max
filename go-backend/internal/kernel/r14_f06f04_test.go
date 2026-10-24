package kernel

import (
	"strings"
	"testing"
)

// R14 F-04 / 148-P3-1: Normalize accepts 0 (= clear / Xray default) but
// the out-of-range error still claimed "0 不是合法值". The wording and
// the accepted set must agree, on the backend and in the frontend
// inbound-form-modal validate (which quotes the same range).
func TestR14F06F04ConnIdleZeroWording(t *testing.T) {
	base := func() Inbound {
		return Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443, Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}
	}
	in := base()
	in.ConnIdleSeconds = 0
	if err := Normalize(TypeXray, &in); err != nil {
		t.Fatalf("connIdle 0 must be accepted (clear/default): %v", err)
	}
	in = base()
	in.ConnIdleSeconds = MaxConnIdleSeconds + 1
	err := Normalize(TypeXray, &in)
	if err == nil {
		t.Fatal("connIdle 86401 must be rejected")
	}
	msg := err.Error()
	if strings.Contains(msg, "0 不是合法值") {
		t.Fatalf("out-of-range error must not call 0 illegal: %q", msg)
	}
	if !strings.Contains(msg, "空闲超时") {
		t.Fatalf("error must name the field: %q", msg)
	}
	if !strings.Contains(msg, "填 0") && !strings.Contains(msg, "或 0") {
		t.Fatalf("error must state 0 means default/clear: %q", msg)
	}
}
