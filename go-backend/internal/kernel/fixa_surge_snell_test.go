package kernel

import (
	"strconv"
	"strings"
	"testing"
)

// C2-01 钉格：Surge 面自建 Snell 对 SnellVersion=0 必须与
// clashSnell / sing-box / 导入 Surge 同口径归一到
// DefaultSnellVersion，不得直出域外的 version=0；
// 4/5/6 现行直出不变。
func TestFixASurgeSnellVersionZeroDefaults(t *testing.T) {
	render := func(v int) string {
		in := Inbound{Protocol: ProtocolSnell, Port: 8388,
			Settings: Settings{Password: "psk-test", SnellVersion: v}}
		line, ok := SurgeLine(Endpoint{Name: "snell", Host: "203.0.113.10", Port: 8388, Inbound: in, Direct: true})
		if !ok {
			t.Fatalf("SurgeLine must render Snell v%d", v)
		}
		return line
	}
	if line := render(0); strings.Contains(line, "version=0") || !strings.Contains(line, "version=5") {
		t.Fatalf("SnellVersion 0 must render as version=%d on the Surge face, got %q", DefaultSnellVersion, line)
	}
	for _, v := range []int{4, 5, 6} {
		if line := render(v); !strings.Contains(line, "version="+strconv.Itoa(v)) {
			t.Fatalf("SnellVersion %d must render verbatim, got %q", v, line)
		}
	}
}
