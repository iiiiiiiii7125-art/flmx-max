package handler

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"
)

// D-C2-01 回归：v2 两路 409 拒绝（reset_stream / identity_conflict）
// 此前只回包不落日志——克隆双写分岔落 identity_conflict 格时面板
// 日志侧无痕，序号缺口落 reset_stream 格同理。修复后两路各落一条
// 与 behind/replay 同字段格的结构化日志（class=reset_stream /
// class=identity_conflict），使面板日志对全部未入账结局闭环；
// 拒绝判定、回执与不入账语义一律不变（本用例同时钉住）。
func TestFlowUploadV2RejectsAreLogged(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)

	var logBuf bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldLog)

	// 先正常入账一批，游标推进到 1（in=D 合计、out=U 合计）。
	items := fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID)
	code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, items))
	if code != http.StatusOK || ack.Error != "" {
		t.Fatalf("setup batch must ack: code=%d ack=%+v", code, ack)
	}

	// 序号缺口（游标 1、发 3）：reset_stream 拒绝、不入账、游标不动，
	// 且落 class=reset_stream 日志（cursor=判定前游标 1）。
	skipped := fmt.Sprintf(`[{"n":"%d_1_0","u":1,"d":1}]`, fwdID)
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 3, skipped))
	if code != http.StatusConflict || ack.Error != "reset_stream" {
		t.Fatalf("sequence gap must reset_stream: code=%d ack=%+v", code, ack)
	}

	// 同序号不同内容：identity_conflict 拒绝、不入账，
	// 且落 class=identity_conflict 日志。
	other := fmt.Sprintf(`[{"n":"%d_1_0","u":71,"d":50}]`, fwdID)
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, other))
	if code != http.StatusConflict || ack.Error != "identity_conflict" {
		t.Fatalf("same seq different content must conflict: code=%d ack=%+v", code, ack)
	}

	// 两路拒绝均不得入账、不得推进游标（语义零变更钉死）。
	if in, out := v2ForwardFlow(t, h, fwdID); in != 50 || out != 70 {
		t.Fatalf("rejected batches must not be accounted: in=%d out=%d, want 50/70", in, out)
	}
	cursor, ok := v2Cursor(t, h, 1, v2StreamA)
	if !ok || cursor.Sequence != 1 {
		t.Fatalf("rejected batches must not advance cursor: %+v ok=%v", cursor, ok)
	}

	logs := logBuf.String()
	resetLine := findLogLine(logs, "class=reset_stream")
	if resetLine == "" {
		t.Fatalf("gap rejection must be logged as class=reset_stream, logs:\n%s", logs)
	}
	for _, field := range []string{"node_id=1", "stream=" + v2StreamA, "sequence=3", "cursor=1", "items=1", "bytes=2"} {
		if !strings.Contains(resetLine, field) {
			t.Fatalf("reset_stream log line must carry %s, line: %s", field, resetLine)
		}
	}
	conflictLine := findLogLine(logs, "class=identity_conflict")
	if conflictLine == "" {
		t.Fatalf("conflict rejection must be logged as class=identity_conflict, logs:\n%s", logs)
	}
	for _, field := range []string{"node_id=1", "stream=" + v2StreamA, "sequence=1", "items=1", "bytes=121"} {
		if !strings.Contains(conflictLine, field) {
			t.Fatalf("identity_conflict log line must carry %s, line: %s", field, conflictLine)
		}
	}
}

func findLogLine(logs, marker string) string {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	return ""
}
