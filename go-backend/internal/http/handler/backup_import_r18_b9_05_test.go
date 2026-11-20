package handler

import (
	"io"
	"strings"
	"testing"
)

// R18-B9-05：decodeBackupImportRequest 快径单次 Decode 后不查尾部
// 数据，拼接/损坏备份（合法 JSON + 尾部垃圾）静默只导前半段。
// 以下测试先于修复落盘（先红后绿）。本实例只修备份导入面；
// decodeJSON 全仓同型收口归 W2-12 OPEN-11 裁决。
func TestR18B905DecodeBackupImportRejectsTrailingData(t *testing.T) {
	valid := `{"version":"1.0","types":["configs"],"configs":{"app_name":"x"}}`
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"single value", valid, false},
		{"trailing whitespace", valid + "\n  \n", false},
		{"trailing garbage", valid + "GARBAGE", true},
		{"trailing second JSON object", valid + `{"version":"1.0"}`, true},
		{"trailing second JSON number", valid + "123", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req backupImportRequest
			dropped, err := decodeBackupImportRequest(io.NopCloser(strings.NewReader(tc.body)), &req, 100<<20)
			if tc.wantErr && err == nil {
				t.Fatalf("R18-B9-05：%q 应被拒绝，got err=nil dropped=%v", tc.name, dropped)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("R18-B9-05：%q 应通过，got err=%v", tc.name, err)
			}
		})
	}
}
