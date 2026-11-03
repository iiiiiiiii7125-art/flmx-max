package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// RM-C2-2 收口（LEDGER5 §7 验收行）：克隆双写（整目录克隆形态）落
// behind 格时，面板日志此前一律归 class=agent_state_behind——与
// 单写者 spool 回退（RM-C2-1）在日志上不可区分，LEDGER5 §7 要求
// 的 class=identity_conflict 专用行在该格缺失。本用例钉住补齐后
// 的分类判据（判据与边界见 repo/traffic_stream.go 头注）：
//
// behind 批次同时满足两条才落 identity_conflict——
//
//	(a) 内容分岔可证：序号仍在面板保留的近窗摘要内，且本批摘要与
//	    该序号已入账摘要不同；
//	(b) 并发写者可证：本批来源（clientip 可信链解析）与该 stream
//	    已入账批次的写者来源不同。
//
// 其余 behind 一律维持 agent_state_behind：同写者回退重计（原格）、
// 异源但内容与已入账原批逐字一致（真重发）、序号超出近窗（内容
// 不可验）。全部格回执与记账语义不变：200 确认、不入账、带游标。
func v2PostFrom(t *testing.T, h *Handler, secret string, plain []byte, remoteAddr string) (int, v2Ack, string) {
	t.Helper()
	body := f01EncryptEnvelope(t, secret, plain)
	req := httptest.NewRequest(http.MethodPost, "/flow/upload/v2?secret="+secret, bytes.NewReader(body))
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.flowUploadV2(rec, req)
	var ack v2Ack
	_ = json.Unmarshal(rec.Body.Bytes(), &ack)
	return rec.Code, ack, rec.Body.String()
}

func TestFlowUploadV2CloneBehindBatchClassifiedIdentityConflict(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)

	var logBuf bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldLog)

	const writerA = "10.10.0.1:41001" // 正主 agent 来源
	const cloneB = "10.10.0.2:41002"  // 克隆 agent 来源（独立 netns）

	// 正主依次入账三批，游标推进到 3（in=D 合计 70、out=U 合计 110）。
	booked := []string{
		fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID),
		fmt.Sprintf(`[{"n":"%d_1_0","u":30,"d":10}]`, fwdID),
		fmt.Sprintf(`[{"n":"%d_1_0","u":10,"d":10}]`, fwdID),
	}
	for i, items := range booked {
		seq := int64(i + 1)
		code, ack, _ := v2PostFrom(t, h, f01TestSecret, v2Envelope(v2StreamA, seq, items), writerA)
		if code != http.StatusOK || ack.Cursor != seq {
			t.Fatalf("setup batch seq=%d must ack with cursor=%d: code=%d ack=%+v", seq, seq, code, ack)
		}
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 70 || out != 110 {
		t.Fatalf("setup batches must be accounted: in=%d out=%d, want 70/110", in, out)
	}
	logBuf.Reset()

	// 克隆格：异源 + 同序号异内容（克隆以复制的 spool 在 seq=2 计
	// 了自己的流量）——须落 class=identity_conflict 专用行，仍只确
	// 认不入账。
	forked := fmt.Sprintf(`[{"n":"%d_1_0","u":999,"d":999}]`, fwdID)
	code, ack, _ := v2PostFrom(t, h, f01TestSecret, v2Envelope(v2StreamA, 2, forked), cloneB)
	if code != http.StatusOK || ack.Error != "" || ack.Cursor != 3 {
		t.Fatalf("clone behind batch must still ack unbooked with cursor=3: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 70 || out != 110 {
		t.Fatalf("clone behind batch must not be accounted: in=%d out=%d", in, out)
	}
	cloneLogs := logBuf.String()
	conflictLine := findLogLine(cloneLogs, "class=identity_conflict")
	if conflictLine == "" {
		t.Fatalf("clone behind batch must be logged as class=identity_conflict, logs:\n%s", cloneLogs)
	}
	for _, field := range []string{"node_id=1", "stream=" + v2StreamA, "sequence=2", "cursor=3"} {
		if !strings.Contains(conflictLine, field) {
			t.Fatalf("identity_conflict log line must carry %s, line: %s", field, conflictLine)
		}
	}
	if strings.Contains(cloneLogs, "class=agent_state_behind") {
		t.Fatalf("clone behind batch must not be classified agent_state_behind, logs:\n%s", cloneLogs)
	}
	logBuf.Reset()

	// 原格不回归：同写者回退重计（同源、同序号异内容）仍落
	// agent_state_behind。
	rewound := fmt.Sprintf(`[{"n":"%d_1_0","u":888,"d":888}]`, fwdID)
	code, ack, _ = v2PostFrom(t, h, f01TestSecret, v2Envelope(v2StreamA, 2, rewound), writerA)
	if code != http.StatusOK || ack.Cursor != 3 {
		t.Fatalf("same-writer behind batch must ack with cursor=3: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 70 || out != 110 {
		t.Fatalf("same-writer behind batch must not be accounted: in=%d out=%d", in, out)
	}
	rewindLogs := logBuf.String()
	if !strings.Contains(rewindLogs, "class=agent_state_behind") ||
		strings.Contains(rewindLogs, "class=identity_conflict") {
		t.Fatalf("same-writer rewind must stay agent_state_behind, logs:\n%s", rewindLogs)
	}
	logBuf.Reset()

	// 异源但内容逐字等于已入账原批（真重发）：内容同一性成立，
	// 不构成身份冲突，仍落 agent_state_behind。
	code, ack, _ = v2PostFrom(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, booked[0]), cloneB)
	if code != http.StatusOK || ack.Cursor != 3 {
		t.Fatalf("cross-source exact replay must ack with cursor=3: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 70 || out != 110 {
		t.Fatalf("cross-source exact replay must not be accounted: in=%d out=%d", in, out)
	}
	replayLogs := logBuf.String()
	if !strings.Contains(replayLogs, "class=agent_state_behind") ||
		strings.Contains(replayLogs, "class=identity_conflict") {
		t.Fatalf("cross-source exact replay must stay agent_state_behind, logs:\n%s", replayLogs)
	}
	logBuf.Reset()

	// 近窗边界（stream B）：正主连入 20 批（游标 20，近窗保留最近
	// 16 个摘要＝序号 5..20）。窗内序号异源异内容 → identity_
	// conflict；窗外序号（4）内容不可验 → agent_state_behind。
	small := fmt.Sprintf(`[{"n":"%d_1_0","u":1,"d":1}]`, fwdID)
	for seq := int64(1); seq <= 20; seq++ {
		code, ack, _ := v2PostFrom(t, h, f01TestSecret, v2Envelope(v2StreamB, seq, small), writerA)
		if code != http.StatusOK || ack.Cursor != seq {
			t.Fatalf("stream B setup seq=%d must ack with cursor=%d: code=%d ack=%+v", seq, seq, code, ack)
		}
	}
	inBefore, outBefore := v2ForwardFlow(t, h, fwdID)
	logBuf.Reset()

	code, ack, _ = v2PostFrom(t, h, f01TestSecret, v2Envelope(v2StreamB, 5, forked), cloneB)
	if code != http.StatusOK || ack.Cursor != 20 {
		t.Fatalf("in-window forked batch must ack with cursor=20: code=%d ack=%+v", code, ack)
	}
	edgeLogs := logBuf.String()
	if findLogLine(edgeLogs, "class=identity_conflict") == "" {
		t.Fatalf("in-window forked batch from a distinct writer must be identity_conflict, logs:\n%s", edgeLogs)
	}
	logBuf.Reset()

	code, ack, _ = v2PostFrom(t, h, f01TestSecret, v2Envelope(v2StreamB, 4, forked), cloneB)
	if code != http.StatusOK || ack.Cursor != 20 {
		t.Fatalf("out-of-window batch must ack with cursor=20: code=%d ack=%+v", code, ack)
	}
	oldLogs := logBuf.String()
	if !strings.Contains(oldLogs, "class=agent_state_behind") ||
		strings.Contains(oldLogs, "class=identity_conflict") {
		t.Fatalf("out-of-window unverifiable batch must stay agent_state_behind, logs:\n%s", oldLogs)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != inBefore || out != outBefore {
		t.Fatalf("window-edge batches must not be accounted: in=%d out=%d, want %d/%d", in, out, inBefore, outBefore)
	}
	if cursor, ok := v2Cursor(t, h, 1, v2StreamB); !ok || cursor.Sequence != 20 {
		t.Fatalf("stream B cursor must stay 20: %+v ok=%v", cursor, ok)
	}
}
