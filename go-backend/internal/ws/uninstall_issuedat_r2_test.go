package ws

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R2-ISSUEDAT 面板半：终局帧 issuedAt 接受数字（正形）、数字
// 字符串、RFC3339 字符串、空串/缺席（按 0、不参与新旧比对）；
// 无法识别的字符串仍按 malformed 丢弃；与登记不符的数字仍按
// stale 忽略。生产形状（数字）是第一格，字符串形是兼容格——
// 字符串用例不得作为唯一证明。
// 注：本文件造帧把 success/reason 放在 data 内嵌形态——那是解
// 码兜底面（顶层缺席时回读），不是生产正形；正形（顶层
// success/message）的字节级证明在 uninstall_final_contract_
// test.go，本文件的格只钉 issuedAt 形态学，不作字段对齐证据。
func TestHandleUninstallResultIssuedAtShapes(t *testing.T) {
	issued := time.Now().Truncate(time.Millisecond)
	cases := []struct {
		name       string
		frame      func(ms int64) string
		wantState  string
		wantReason string
	}{
		{
			name: "numeric millis matches registry",
			frame: func(ms int64) string {
				b, _ := json.Marshal(map[string]interface{}{
					"type": "UninstallResult",
					"data": map[string]interface{}{"issuedAt": ms, "success": true},
				})
				return string(b)
			},
			wantState: repo.NodePendingUninstallSucceeded,
		},
		{
			name: "numeric string matches registry",
			frame: func(ms int64) string {
				return `{"type":"UninstallResult","data":{"issuedAt":"` + strconv.FormatInt(ms, 10) + `","success":true}}`
			},
			wantState: repo.NodePendingUninstallSucceeded,
		},
		{
			name: "rfc3339 string of same instant",
			frame: func(ms int64) string {
				return `{"type":"UninstallResult","data":{"issuedAt":"` + time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) + `","success":true}}`
			},
			wantState: repo.NodePendingUninstallSucceeded,
		},
		{
			name: "empty string accepted as unknown",
			frame: func(ms int64) string {
				return `{"type":"UninstallResult","data":{"issuedAt":"","success":true}}`
			},
			wantState: repo.NodePendingUninstallSucceeded,
		},
		{
			name: "missing key accepted as unknown",
			frame: func(ms int64) string {
				return `{"type":"UninstallResult","data":{"success":true}}`
			},
			wantState: repo.NodePendingUninstallSucceeded,
		},
		{
			name: "stale numeric ignored",
			frame: func(ms int64) string {
				b, _ := json.Marshal(map[string]interface{}{
					"type": "UninstallResult",
					"data": map[string]interface{}{"issuedAt": ms + 60000, "success": true},
				})
				return string(b)
			},
			wantState: repo.NodePendingUninstallIssued,
		},
		{
			name: "unparseable string malformed and ignored",
			frame: func(ms int64) string {
				return `{"type":"UninstallResult","data":{"issuedAt":"not-a-time","success":true}}`
			},
			wantState: repo.NodePendingUninstallIssued,
		},
		{
			name: "failure with reason lands as failed",
			frame: func(ms int64) string {
				b, _ := json.Marshal(map[string]interface{}{
					"type": "UninstallResult",
					"data": map[string]interface{}{"issuedAt": ms, "success": false, "reason": "rm failed"},
				})
				return string(b)
			},
			wantState:  repo.NodePendingUninstallFailed,
			wantReason: "rm failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := uninstallRepo(t)
			nodeID := int64(9001)
			if err := r.UpsertNodePendingUninstall(nodeID, issued); err != nil {
				t.Fatalf("seed pending: %v", err)
			}
			rec, err := r.GetNodePendingUninstall(nodeID)
			if err != nil || rec == nil {
				t.Fatalf("read seeded pending: rec=%v err=%v", rec, err)
			}
			srv := NewServer(r, "jwt-secret")
			srv.handleUninstallResult(nodeID, tc.frame(rec.IssuedAt))
			got, err := r.GetNodePendingUninstall(nodeID)
			if err != nil || got == nil {
				t.Fatalf("read pending after frame: got=%v err=%v", got, err)
			}
			if got.State != tc.wantState {
				t.Fatalf("state=%q, want %q", got.State, tc.wantState)
			}
			if tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Fatalf("reason=%q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

