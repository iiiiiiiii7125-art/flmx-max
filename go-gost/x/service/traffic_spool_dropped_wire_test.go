package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// 流量审计 A-中-1 回归：spool 死信字节必须随 v2 信封上行——
// trafficEnvelope 的 dropped_bytes 字段须在线上 JSON 里出现，
// 否则面板侧永远收不到死信量（此前面板信封无此字段，编码解码
// 时静默丢弃）。死信计数本身（deadLetterPending -> DroppedBytes
// -> 下批 Pending -> 确认后扣减）由 traffic_spool_backoff_test
// 覆盖。
func TestTrafficEnvelopeDroppedBytesOnWire(t *testing.T) {
	env := trafficEnvelope{
		Stream:       "00112233445566778899aabbccddeeff",
		Sequence:     7,
		Items:        []TrafficReportItem{{N: "1_1_0", U: 10, D: 20}},
		DroppedBytes: 3333,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"dropped_bytes":3333`) {
		t.Fatalf("envelope JSON must carry dropped_bytes: %s", raw)
	}
	var back trafficEnvelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.DroppedBytes != 3333 || back.Sequence != 7 || len(back.Items) != 1 {
		t.Fatalf("envelope round-trip lost fields: %+v", back)
	}
	// 零值死信省略字段（omitempty）：旧面板按未知字段忽略、新面
	// 板按缺字段为 0，均不拒收。
	empty, err := json.Marshal(trafficEnvelope{Stream: "00112233445566778899aabbccddeeff", Sequence: 1, Items: []TrafficReportItem{{N: "1_1_0", U: 1, D: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "dropped_bytes") {
		t.Fatalf("zero dropped_bytes must be omitted: %s", empty)
	}
}
