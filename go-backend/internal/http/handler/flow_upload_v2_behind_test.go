package handler

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"
)

// C-C2-01 回归：序号严格小于游标的批次（agent spool 被整机快照
// 恢复/旧备份还原/整目录克隆回退后的重发段）。
//
// 修复前：面板无条件按重放确认、不比摘要、不写日志、回执不带游标
// 位置——回退段被逐批「确认并清批」静默少计，两侧零可观测。
// 修复后：
//   - 回执带 cursor（判定后面板该 stream 的游标序号），agent 据此
//     识别自身落后并切新 stream 自愈；
//   - behind ack（sequence < cursor）落 class=agent_state_behind
//     结构化日志，与正常重放（sequence == cursor 且摘要一致，
//     class=replay_exact）在日志上可区分；
//   - behind 批次仍只确认不入账（不产生双计），后续正常序号照常入账。
func TestFlowUploadV2BehindCursorAckIsObservableAndCarriesCursor(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)

	var logBuf bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldLog)

	// 先正常入账三批，游标推进到 3（in=D 合计、out=U 合计）。
	booked := []string{
		fmt.Sprintf(`[{"n":"%d_1_0","u":70,"d":50}]`, fwdID),
		fmt.Sprintf(`[{"n":"%d_1_0","u":30,"d":10}]`, fwdID),
		fmt.Sprintf(`[{"n":"%d_1_0","u":10,"d":10}]`, fwdID),
	}
	for i, items := range booked {
		seq := int64(i + 1)
		code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, seq, items))
		if code != http.StatusOK || ack.Error != "" {
			t.Fatalf("setup batch seq=%d must ack: code=%d ack=%+v", seq, code, ack)
		}
		if ack.Cursor != seq {
			t.Fatalf("applied ack must carry cursor=%d, got %+v", seq, ack)
		}
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 70 || out != 110 {
		t.Fatalf("setup batches must be accounted: in=%d out=%d, want 70/110", in, out)
	}

	// 回退段批次：序号 2 < 游标 3，内容与当初入账的 seq=2 不同
	// （回退后新计的流量）——仍只确认不入账，但回执必须带游标，
	// 且落 agent_state_behind 日志。
	behind := fmt.Sprintf(`[{"n":"%d_1_0","u":999,"d":999}]`, fwdID)
	code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 2, behind))
	if code != http.StatusOK || ack.Error != "" || ack.Sequence != 2 {
		t.Fatalf("behind batch must still ack: code=%d ack=%+v", code, ack)
	}
	if ack.Cursor != 3 {
		t.Fatalf("behind ack must carry panel cursor=3, got %+v", ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 70 || out != 110 {
		t.Fatalf("behind batch must not be accounted: in=%d out=%d", in, out)
	}

	// 序号 1 同样 behind：回执游标仍为 3、不入账。
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, behind))
	if code != http.StatusOK || ack.Cursor != 3 {
		t.Fatalf("older behind batch must ack with cursor=3: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 70 || out != 110 {
		t.Fatalf("older behind batch must not be accounted: in=%d out=%d", in, out)
	}

	// 正常重放：序号 == 游标且摘要一致——回执游标 3、不入账，
	// 日志类别必须是 replay_exact，与 behind 可区分。
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 3, booked[2]))
	if code != http.StatusOK || ack.Error != "" || ack.Cursor != 3 {
		t.Fatalf("exact replay must ack with cursor=3: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 70 || out != 110 {
		t.Fatalf("exact replay must not double-account: in=%d out=%d", in, out)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "class=agent_state_behind") ||
		!strings.Contains(logs, "sequence=2") || !strings.Contains(logs, "cursor=3") {
		t.Fatalf("behind ack must be logged as agent_state_behind with seq/cursor, logs:\n%s", logs)
	}
	if !strings.Contains(logs, "class=replay_exact") {
		t.Fatalf("exact replay must be logged as replay_exact (distinguishable from behind), logs:\n%s", logs)
	}

	// 游标之后的下一序号照常入账，回执游标跟随推进到 4。
	next := fmt.Sprintf(`[{"n":"%d_1_0","u":1,"d":1}]`, fwdID)
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 4, next))
	if code != http.StatusOK || ack.Cursor != 4 {
		t.Fatalf("next sequence must ack with cursor=4: code=%d ack=%+v", code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 71 || out != 111 {
		t.Fatalf("next batch must be accounted: in=%d out=%d, want 71/111", in, out)
	}
}
