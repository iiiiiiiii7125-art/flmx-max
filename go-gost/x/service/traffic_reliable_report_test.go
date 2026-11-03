package service

import (
	"net/url"
	"testing"
)

// v2 发送器 URL 改写（A-T-07）：只替换尾段、保留地址形态，与 v1
// 同源。其中「带路径前缀」用例是防御性覆盖：唯一生产者
// buildReportURLCandidates 的地址归一化恒丢弃路径、候选恒为
// host 形态（R3-C3-O1 前提订正），前缀形态生产不可达——钉住的
// 是改写函数对该形态的保守行为，而非生产路径。
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
