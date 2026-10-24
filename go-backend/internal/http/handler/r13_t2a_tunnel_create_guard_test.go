package handler

import (
	"strings"
	"testing"
)

// R13 批 T2-a（R13X-002，P3）：tunnelCreate 守卫键表须与 tunnelUpdate
// 同形（type/flow/status + trafficRatio）。先红后绿：修复前 type /
// status 的不可解析输入静默套硬默认落库，本测试应失败。

func TestR13T2aTunnelCreateRejectsUnparsableTypeAndStatus(t *testing.T) {
	cases := []struct {
		name    string
		fields  string
		wantMsg string
	}{
		{"type-string", `"type":"abc","status":1,"flow":1,"trafficRatio":1`, "type 参数无效"},
		{"status-string", `"type":1,"status":"abc","flow":1,"trafficRatio":1`, "status 参数无效"},
		{"type-float", `"type":1.5,"status":1,"flow":1,"trafficRatio":1`, "type 参数无效"},
		// 对照组：R12X-003 断言在位，flow 不可解析仍被拒。
		{"flow-string-control", `"type":1,"status":1,"flow":"abc","trafficRatio":1`, "flow 参数无效"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := setupProbeTargetTunnelHandler(t)
			tunnelName := "r13t2a-bad-" + tc.name
			code, msg := r11b4PostTunnel(t, h.tunnelCreate, "/api/v1/tunnel/create",
				`{"name":"`+tunnelName+`",`+tc.fields+`,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
			if code == 0 {
				t.Fatalf("R13X-002: unparsable create must be rejected, got success")
			}
			if !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("rejection must name the field (%s), got msg=%q", tc.wantMsg, msg)
			}
			if _, _, count := r11b4TunnelRow(t, h, tunnelName); count != 0 {
				t.Fatalf("rejected create must not persist, got %d rows", count)
			}
		})
	}
}

func TestR13T2aTunnelCreateAcceptsLegalTypeAndStatus(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	tunnelName := "r13t2a-ok-legal"
	code, msg := r11b4PostTunnel(t, h.tunnelCreate, "/api/v1/tunnel/create",
		`{"name":"`+tunnelName+`","type":1,"status":1,"flow":2,"trafficRatio":2.5,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code != 0 {
		t.Fatalf("legal create must succeed, code=%d msg=%s", code, msg)
	}
	flow, ratio, count := r11b4TunnelRow(t, h, tunnelName)
	if count != 1 || flow != 2 || ratio != 2.5 {
		t.Fatalf("stored = flow %d ratio %v count %d, want 2/2.5/1", flow, ratio, count)
	}
}
