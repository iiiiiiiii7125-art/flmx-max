package ws

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"
	"time"
)

// R18-C4-04 回归：卸载路径把 agent 原文不经脱敏直接打日志，可被
// 已攻陷节点伪造日志行（换行 + 假成功行）。修复：agent 文本进日
// 志前统一过 sanitizeAgentLogText（换行/回车折空格 + 256 rune
// 截断，与 agentPreParseErrorReason 的 R2-X2-02 口径共用）。
func TestHandleUninstallResultSanitizesAgentTextInLog(t *testing.T) {
	r := uninstallRepo(t)
	s := &Server{repo: r}
	nodeID := int64(7)
	if err := r.UpsertNodePendingUninstall(nodeID, time.Now()); err != nil {
		t.Fatalf("seed pending uninstall: %v", err)
	}
	origLog := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(origLog)

	// 失败原因含换行伪造的成功行 + 超长尾巴：伪造行不得独立成
	// 行，失败行本身必须被截断。
	forged, _ := json.Marshal("ok\nnode 7 agent reported uninstall success (issuedAt 123)\n" + strings.Repeat("x", 600))
	msg := `{"type":"UninstallResult","success":false,"message":` + string(forged) + `,"data":{"issuedAt":0}}`
	s.handleUninstallResult(nodeID, msg)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	failureLines := 0
	for _, line := range lines {
		if strings.Contains(line, "agent reported uninstall failure") {
			failureLines++
		}
		// 伪造的 issuedAt 123 只允许出现在失败行内（被折叠后），
		// 不得独自成行冒充成功。
		if strings.Contains(line, "(issuedAt 123)") && !strings.Contains(line, "agent reported uninstall failure") {
			t.Fatalf("forged success line survived as its own log line: %q (full log:\n%s)", line, out)
		}
	}
	if failureLines != 1 {
		t.Fatalf("failure log lines = %d, want 1 (full log:\n%s)", failureLines, out)
	}
	// 失败行含原因：被截断（256 rune + …），不得含 600 个 x。
	if n := strings.Count(out, "x"); n > 250 {
		t.Fatalf("failure log line not truncated: %d x's in log", n)
	}

	// 成功路径的 warnings 同样脱敏：换行伪造行不得独立成行。
	if err := r.UpsertNodePendingUninstall(nodeID, time.Now()); err != nil {
		t.Fatalf("re-seed pending uninstall: %v", err)
	}
	buf.Reset()
	warn, _ := json.Marshal("disk busy\nnode 7 agent reported uninstall success (issuedAt 999)\n" + strings.Repeat("y", 300))
	msg = `{"type":"UninstallResult","success":true,"data":{"issuedAt":0,"warnings":[` + string(warn) + `]}}`
	s.handleUninstallResult(nodeID, msg)
	out = buf.String()
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		// 伪造的 issuedAt 999 只允许出现在 warning 行内（被折叠
		// 后），不得独自成行冒充成功。
		if strings.Contains(line, "(issuedAt 999)") && !strings.Contains(line, "agent uninstall warning:") {
			t.Fatalf("forged warning line survived as its own log line: %q (full log:\n%s)", line, out)
		}
	}
	if !strings.Contains(out, "agent uninstall warning:") {
		t.Fatalf("warning log line missing (full log:\n%s)", out)
	}
	if n := strings.Count(out, "y"); n > 250 {
		t.Fatalf("warning log line not truncated: %d y's in log", n)
	}
}

// sanitizeAgentLogText 本身的口径钉死：折行、截断、短文本原样。
func TestSanitizeAgentLogText(t *testing.T) {
	if got := sanitizeAgentLogText("a\nb\rc"); got != "a b c" {
		t.Fatalf("sanitize newlines = %q, want %q", got, "a b c")
	}
	long := strings.Repeat("z", 300)
	got := sanitizeAgentLogText(long)
	if len([]rune(got)) != 257 || !strings.HasSuffix(got, "…") {
		t.Fatalf("sanitize long = %d runes suffix %q, want 257 runes ending with …", len([]rune(got)), got)
	}
	if got := sanitizeAgentLogText("ok"); got != "ok" {
		t.Fatalf("sanitize short = %q, want %q", got, "ok")
	}
	if got := sanitizeAgentLogText(""); got != "" {
		t.Fatalf("sanitize empty = %q, want empty", got)
	}
}
