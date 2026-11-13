package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// SWEEP-CPU L3-01 回归：流量上报单批 Items 数上限（与 connlog
// 批上限 1000 对齐）。v1 与 v2 两路都必须封——修一漏一等于没修。
// 超限整批拒收、不入账；恰为 1000 条的边界批正常入账。
// 下列字面值钉死生产常量 flowUploadMaxItemsPerBatch /
// flowUploadMaxOrphanDeletesPerBatch 的取值（测试须在修复前
// 的旧代码上也能编译并变红，故不用生产常量）。

const (
	sweepL3MaxItems         = 1000
	sweepL3MaxOrphanDeletes = 100
)

func sweepL3ItemsJSON(fwdID int64, n int) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"n":"%d_1_0","u":1,"d":1}`, fwdID)
	}
	b.WriteByte(']')
	return b.String()
}

func TestFlowUploadV1RejectsOversizedBatch(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)
	seq := int64(1)

	// 1001 条：整批拒收返非 ok，转发零入账、seq 未推进。
	if got := f01PostFlowUpload(t, h, f01TestSecret, []byte(sweepL3ItemsJSON(fwdID, sweepL3MaxItems+1)), &seq); got == "ok" {
		t.Fatalf("v1 batch of %d items must be rejected, got ok", sweepL3MaxItems+1)
	}
	if in, out := c101ForwardFlow(t, h, fwdID); in != 0 || out != 0 {
		t.Fatalf("rejected oversized v1 batch must not be accounted: in=%d out=%d", in, out)
	}
	if lastSeq, ok := f01Seq(t, h, 1); ok {
		t.Fatalf("rejected oversized v1 batch must not advance seq, got lastSeq=%d", lastSeq)
	}

	// 同 seq、恰 1000 条的边界批：正常入账（u/d 各 1 × 1000）。
	if got := f01PostFlowUpload(t, h, f01TestSecret, []byte(sweepL3ItemsJSON(fwdID, sweepL3MaxItems)), &seq); got != "ok" {
		t.Fatalf("v1 batch of exactly %d items must be accepted, got %q", sweepL3MaxItems, got)
	}
	if in, out := c101ForwardFlow(t, h, fwdID); in != sweepL3MaxItems || out != sweepL3MaxItems {
		t.Fatalf("boundary v1 batch must be accounted: in=%d out=%d, want %d/%d",
			in, out, sweepL3MaxItems, sweepL3MaxItems)
	}
}

func TestFlowUploadV2RejectsOversizedBatch(t *testing.T) {
	h := newF01Handler(t)
	fwdID := seedF01Forward(t, h)

	// 1001 条：400 invalid_batch，不入账、不推进游标。
	code, ack, _ := v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, sweepL3ItemsJSON(fwdID, sweepL3MaxItems+1)))
	if code != http.StatusBadRequest || ack.Error != "invalid_batch" {
		t.Fatalf("v2 batch of %d items must be invalid_batch: code=%d ack=%+v", sweepL3MaxItems+1, code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != 0 || out != 0 {
		t.Fatalf("rejected oversized v2 batch must not be accounted: in=%d out=%d", in, out)
	}
	if _, ok := v2Cursor(t, h, 1, v2StreamA); ok {
		t.Fatal("rejected oversized v2 batch must not create a cursor")
	}

	// 同序号、恰 1000 条的边界批：正常确认入账。
	code, ack, _ = v2Post(t, h, f01TestSecret, v2Envelope(v2StreamA, 1, sweepL3ItemsJSON(fwdID, sweepL3MaxItems)))
	if code != http.StatusOK || ack.Error != "" {
		t.Fatalf("v2 batch of exactly %d items must ack: code=%d ack=%+v", sweepL3MaxItems, code, ack)
	}
	if in, out := v2ForwardFlow(t, h, fwdID); in != sweepL3MaxItems || out != sweepL3MaxItems {
		t.Fatalf("boundary v2 batch must be accounted: in=%d out=%d, want %d/%d",
			in, out, sweepL3MaxItems, sweepL3MaxItems)
	}
}

// SWEEP-CPU L3-01 回归：单批孤儿清理发令上限。150 个 distinct
// 孤儿服务一次上报，只发前 100 条 DeleteService（确定性排序后
// 的前 N）；超限部分留待后续批次——已删孤儿不再上报，清理逐
// 批推进。
func TestFlowUploadOrphanCleanupCappedPerBatch(t *testing.T) {
	a := newCleanupAgent(t)
	names := make([]string, 0, 150)
	for i := 1; i <= 150; i++ {
		// 不存在的转发 ID（900xxx）：ForwardExists 为假即判孤儿。
		names = append(names, fmt.Sprintf("900%03d_1_0", i))
	}
	runCleanupPath(a.h, "batch", names)
	a.probe(t)

	commands := a.commandsOfType("DeleteService")
	if len(commands) != sweepL3MaxOrphanDeletes {
		t.Fatalf("orphan cleanup must send exactly %d DeleteService commands, got %d",
			sweepL3MaxOrphanDeletes, len(commands))
	}
	// 确定性顺序：发出的是排序后的前 100 个孤儿（900001–900100），
	// 900101 之后的孤儿本批不发、留待后续批次。
	servicesOf := func(cmd cleanupAgentCommand) []string {
		var data struct {
			Services []string `json:"services"`
		}
		if err := json.Unmarshal(cmd.Data, &data); err != nil {
			t.Fatalf("decode DeleteService data: %v", err)
		}
		return data.Services
	}
	first := servicesOf(commands[0])
	if len(first) == 0 || first[0] != "900001_1_0_tcp" {
		t.Fatalf("first orphan delete must target 900001_1_0, got %v", first)
	}
	last := servicesOf(commands[len(commands)-1])
	if len(last) == 0 || last[0] != "900100_1_0_tcp" {
		t.Fatalf("100th orphan delete must target 900100_1_0, got %v", last)
	}
	for _, cmd := range commands {
		for _, svc := range servicesOf(cmd) {
			if strings.HasPrefix(svc, "900101_") || strings.HasPrefix(svc, "900150_") {
				t.Fatalf("deferred orphan must not be deleted in this batch: %s", svc)
			}
		}
	}
}
