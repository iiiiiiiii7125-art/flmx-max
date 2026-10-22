package socket

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// FIX-A #1（D1-01）：agent 收到面板命令时只许记录 type 与 requestId，
// 命令全文（含服务配置里的代理密码/SS 密钥等凭据字段）不得落日志。
// 修复前 routeCommand 以 fmt.Println 打印命令 JSON 全文 → FAIL。
func TestLogReceivedCommandRedactsPayload(t *testing.T) {
	const marker = "CMD-CREDENTIAL-MARKER-12345"
	cmd := CommandMessage{
		Type:      "AddService",
		RequestId: "42_1001",
		Data:      map[string]interface{}{"password": marker},
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	logReceivedCommand(cmd)
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()
	if strings.Contains(out, marker) {
		t.Fatalf("command log leaked credential payload: %q", out)
	}
	if !strings.Contains(out, "AddService") || !strings.Contains(out, "42_1001") {
		t.Fatalf("command log should keep type and requestId, got: %q", out)
	}
}
