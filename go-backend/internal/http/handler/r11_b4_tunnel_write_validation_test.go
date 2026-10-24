package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R11 批 4（R11X-008 / R11X-009）：tunnelCreate/tunnelUpdate 写入面补
// TrafficRatio 范围校验 [0,100] 与 Flow 枚举校验 {1,2}，域外值点名
// 拒绝；合法值落库逐字不变。update 为真部分更新口径（A2-04 /
// R4X-004）：仅校验请求中在场字段，缺席字段回填的存量值不因本校验
// 被拒（存量域外行仍可做无关编辑，值原样保留）。

func r11b4PostTunnel(t *testing.T, fn func(http.ResponseWriter, *http.Request), url, body string) (int, string) {
	t.Helper()
	res := httptest.NewRecorder()
	fn(res, httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body))))
	return decodeEnvelopeA204(t, res)
}

func r11b4TunnelRow(t *testing.T, h *Handler, name string) (flow int64, ratio float64, count int) {
	t.Helper()
	var row struct {
		Flow         int64
		TrafficRatio float64
	}
	if err := h.repo.DB().Raw(`SELECT COUNT(*) FROM tunnel WHERE name = ?`, name).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		return 0, 0, 0
	}
	if err := h.repo.DB().Raw(`SELECT flow, traffic_ratio FROM tunnel WHERE name = ?`, name).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.Flow, row.TrafficRatio, count
}

func TestR11B4TunnelCreateRejectsOutOfRangeRatioAndFlow(t *testing.T) {
	cases := []struct {
		name    string
		fields  string
		wantMsg string
	}{
		{"ratio-negative", `"flow":1,"trafficRatio":-2`, "流量倍率"},
		{"ratio-over-100", `"flow":1,"trafficRatio":101`, "流量倍率"},
		{"flow-zero", `"flow":0,"trafficRatio":1`, "计费方向"},
		{"flow-three", `"flow":3,"trafficRatio":1`, "计费方向"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := setupProbeTargetTunnelHandler(t)
			tunnelName := "r11b4-bad-" + tc.name
			code, msg := r11b4PostTunnel(t, h.tunnelCreate, "/api/v1/tunnel/create",
				`{"name":"`+tunnelName+`","type":1,"status":1,`+tc.fields+`,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
			if code == 0 {
				t.Fatalf("R11X-008/009: out-of-range create must be rejected, got success")
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

func TestR11B4TunnelCreateAcceptsLegalRatioAndFlow(t *testing.T) {
	cases := []struct {
		name      string
		fields    string
		wantFlow  int64
		wantRatio float64
	}{
		{"ratio-zero-flow1", `"flow":1,"trafficRatio":0`, 1, 0},
		{"ratio-100-flow2", `"flow":2,"trafficRatio":100`, 2, 100},
		{"ratio-fraction-flow2", `"flow":2,"trafficRatio":2.5`, 2, 2.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := setupProbeTargetTunnelHandler(t)
			tunnelName := "r11b4-ok-" + tc.name
			code, msg := r11b4PostTunnel(t, h.tunnelCreate, "/api/v1/tunnel/create",
				`{"name":"`+tunnelName+`","type":1,"status":1,`+tc.fields+`,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
			if code != 0 {
				t.Fatalf("legal create must succeed, code=%d msg=%s", code, msg)
			}
			flow, ratio, count := r11b4TunnelRow(t, h, tunnelName)
			if count != 1 || flow != tc.wantFlow || ratio != tc.wantRatio {
				t.Fatalf("stored = flow %d ratio %v count %d, want %d/%v/1", flow, ratio, count, tc.wantFlow, tc.wantRatio)
			}
		})
	}
}

func TestR11B4TunnelUpdateRejectsOutOfRangeRatioAndFlow(t *testing.T) {
	cases := []struct {
		name    string
		fields  string
		wantMsg string
	}{
		{"ratio-negative", `"trafficRatio":-2`, "流量倍率"},
		{"ratio-over-100", `"trafficRatio":100.5`, "流量倍率"},
		{"flow-zero", `"flow":0`, "计费方向"},
		{"flow-three", `"flow":3`, "计费方向"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := setupProbeTargetTunnelHandler(t)
			seedProbeTargetTunnel(t, h, 77, "r11b4-upd", "", 0)
			code, msg := r11b4PostTunnel(t, h.tunnelUpdate, "/api/v1/tunnel/update",
				`{"id":77,"name":"r11b4-upd","type":1,"status":1,`+tc.fields+`,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
			if code == 0 {
				t.Fatalf("R11X-008/009: out-of-range update must be rejected, got success")
			}
			if !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("rejection must name the field (%s), got msg=%q", tc.wantMsg, msg)
			}
			flow, ratio, _ := r11b4TunnelRow(t, h, "r11b4-upd")
			if flow != 1 || ratio != 1 {
				t.Fatalf("rejected update must not change stored values, got flow=%d ratio=%v", flow, ratio)
			}
		})
	}
}

func TestR11B4TunnelUpdateAcceptsLegalRatioAndFlow(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 78, "r11b4-upd-ok", "", 0)
	code, msg := r11b4PostTunnel(t, h.tunnelUpdate, "/api/v1/tunnel/update",
		`{"id":78,"name":"r11b4-upd-ok","type":1,"status":1,"flow":2,"trafficRatio":100,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code != 0 {
		t.Fatalf("legal update must succeed, code=%d msg=%s", code, msg)
	}
	flow, ratio, _ := r11b4TunnelRow(t, h, "r11b4-upd-ok")
	if flow != 2 || ratio != 100 {
		t.Fatalf("stored = flow %d ratio %v, want 2/100", flow, ratio)
	}

	code, msg = r11b4PostTunnel(t, h.tunnelUpdate, "/api/v1/tunnel/update",
		`{"id":78,"name":"r11b4-upd-ok","type":1,"status":1,"flow":1,"trafficRatio":0,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code != 0 {
		t.Fatalf("legal boundary update must succeed, code=%d msg=%s", code, msg)
	}
	flow, ratio, _ = r11b4TunnelRow(t, h, "r11b4-upd-ok")
	if flow != 1 || ratio != 0 {
		t.Fatalf("stored = flow %d ratio %v, want 1/0", flow, ratio)
	}
}

// 缺席字段回填存量域外值不得被本校验连坐拒绝（A2-04 / R4X-004 的
// presence 护栏）：存量行做无关编辑应成功且域外值原样保留，根治靠
// 写入面拦截新值、存量呈现登记见批 4 报告。
func TestR11B4TunnelUpdatePartialPreservesStockOutOfRange(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 79, "r11b4-stock", "", 0)
	if err := h.repo.DB().Exec(`UPDATE tunnel SET flow = 0, traffic_ratio = -2 WHERE id = 79`).Error; err != nil {
		t.Fatal(err)
	}
	code, msg := r11b4PostTunnel(t, h.tunnelUpdate, "/api/v1/tunnel/update",
		`{"id":79,"name":"r11b4-stock-renamed","type":1,"status":1,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code != 0 {
		t.Fatalf("partial update omitting flow/trafficRatio must succeed for stock row, code=%d msg=%s", code, msg)
	}
	flow, ratio, _ := r11b4TunnelRow(t, h, "r11b4-stock-renamed")
	if flow != 0 || ratio != -2 {
		t.Fatalf("stock values must be preserved verbatim, got flow=%d ratio=%v", flow, ratio)
	}
}
