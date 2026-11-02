package service

import (
	"net/url"
	"testing"
)

// v2 发送器 URL 改写（A-T-07）：只替换尾段、保留地址形态，与 v1
// 同源。此前整段覆盖 Path，候选 URL 若带反代路径前缀，前缀会被
// 丢掉——v1 可达、v2 永远 404 等不到确认。
func TestReliableReportURLReplacesOnlyTailSegment(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"裸地址", "https://panel.example.com/flow/upload?secret=s", "https://panel.example.com/flow/upload/v2?secret=s"},
		{"带路径前缀", "https://panel.example.com/proxy/flow/upload?secret=s", "https://panel.example.com/proxy/flow/upload/v2?secret=s"},
		{"带端口与前缀", "http://10.0.0.1:8080/p/flow/upload?secret=s", "http://10.0.0.1:8080/p/flow/upload/v2?secret=s"},
		{"未知尾段兜底整段覆盖", "https://panel.example.com/other?secret=s", "https://panel.example.com/flow/upload/v2?secret=s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := url.Parse(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			reliableReportURL(parsed)
			if got := parsed.String(); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
