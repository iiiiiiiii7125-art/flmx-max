package repo

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// TestApplyInboundCounterSamplesUnaccountedWarn: 计数 ID 不属于任何现存
// 入站的样本不得静默丢弃——必须打 warn 日志（realm 分流漏做时的症状
// 正是样本落到这里恒 0 无报错）；已知入站的样本不得触发该日志。
func TestApplyInboundCounterSamplesUnaccountedWarn(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "realm-unaccounted.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	inbound := model.NodeInbound{Name: "known-inbound", Protocol: "vless", Port: 443}
	if err := r.DB().Create(&inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	if err := r.ApplyInboundCounterSamples([]InboundCounterSample{
		{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: 100},
		{InboundID: 1 << 40, Protocol: "tcp", Direction: "in", Epoch: "e1", Bytes: 555},
	}, 1000); err != nil {
		t.Fatalf("apply samples: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "unaccounted") || !strings.Contains(out, "inbound_id=1099511627776") {
		t.Fatalf("expected unaccounted warn for unknown inbound id, log = %q", out)
	}
	if strings.Count(out, "unaccounted") != 1 {
		t.Fatalf("known inbound must not trigger unaccounted warn, log = %q", out)
	}
}
