package repo

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// 2.1.2 订阅编辑改造的备份面：旧备份里已删的 Options 键（处理整块 +
// 订阅级链式 + 规则与脚本）在导入时真剔除（不只是忽略），存量
// relayIds 按三档折算表折进 lines，两件事都计数并在 ImportWarnings
// 聚合点名（E-02 删除先例口径）。

func TestFoldLegacyRelayIDs(t *testing.T) {
	cases := []struct {
		ids  []int64
		want string
	}{
		{nil, ""},
		{[]int64{}, ""},
		{[]int64{0}, "direct"},
		{[]int64{2}, "relay"},
		{[]int64{2, 3}, "relay"},
		{[]int64{0, 2}, ""},
		{[]int64{-1}, ""},
		{[]int64{-1, 0}, "direct"},
	}
	for _, c := range cases {
		if got := foldLegacyRelayIDs(c.ids); got != c.want {
			t.Fatalf("foldLegacyRelayIDs(%v) = %q, want %q", c.ids, got, c.want)
		}
	}
}

func TestScrubRemovedSubOptions(t *testing.T) {
	// 已删键非默认 + relayIds [0]：剔键、折算 direct、两面计数。
	out, removed, folded := scrubRemovedSubOptions(
		`{"regions":["🇭🇰 香港"],"rules":true,"script":"s","chainRelay":"x","relayIds":[0],"templates":{"clash":3}}`)
	if !removed || !folded {
		t.Fatalf("removed=%v folded=%v, want both true", removed, folded)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"regions", "rules", "script", "chainRelay", "relayIds"} {
		if _, ok := m[k]; ok {
			t.Fatalf("removed key %q survived scrub: %s", k, out)
		}
	}
	if _, ok := m["templates"]; !ok {
		t.Fatalf("templates must survive scrub: %s", out)
	}
	var lines string
	if err := json.Unmarshal(m["lines"], &lines); err != nil || lines != "direct" {
		t.Fatalf("folded lines = %q (err %v), want direct", lines, err)
	}

	// 显式 lines 优先于 relayIds 折算；已删键仅默认值时不计数
	// （但键本身仍被剔除——删净口径）。
	out, removed, folded = scrubRemovedSubOptions(`{"lines":"relay","relayIds":[0],"rules":false,"dedupe":false}`)
	if removed || !folded {
		t.Fatalf("default-only removed values must not count: removed=%v folded=%v", removed, folded)
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["rules"]; ok {
		t.Fatalf("default-valued removed key must still be scrubbed: %s", out)
	}
	if err := json.Unmarshal(m["lines"], &lines); err != nil || lines != "relay" {
		t.Fatalf("explicit lines must win over the fold: %q", lines)
	}

	// 干净 Options 原样返回；坏 JSON 不动。
	clean := `{"lines":"direct","templates":{"clash":3}}`
	if out, removed, folded := scrubRemovedSubOptions(clean); out != clean || removed || folded {
		t.Fatalf("clean options altered: %q %v %v", out, removed, folded)
	}
	if out, removed, folded := scrubRemovedSubOptions(`{oops`); out != `{oops` || removed || folded {
		t.Fatalf("invalid JSON must pass through: %q %v %v", out, removed, folded)
	}
}

func TestImportScrubsRemovedSubOptionsWithReceipt(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "subedit-scrub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	backup := &model.BackupData{Version: "1.0", Inbounds: &model.InboundsBackup{
		Subscriptions: []model.SubscriptionBackup{
			{ID: 101, Name: "legacy-sub", Token: "tok-legacy-sub", UUID: "uuid-l", Password: "pw-l", Enabled: 1, CreatedTime: now,
				Options: `{"regions":["🇭🇰 香港"],"addFlag":true,"chainLanding":"落地","ruleGroups":["adblock"],"relayIds":[7,9]}`},
			{ID: 102, Name: "clean-sub", Token: "tok-clean-sub", UUID: "uuid-c", Password: "pw-c", Enabled: 1, CreatedTime: now,
				Options: `{"lines":"relay"}`},
		},
	}}
	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.InboundSubscriptionsImported != 2 {
		t.Fatalf("subscriptions imported = %d, want 2 (warnings=%v)", result.InboundSubscriptionsImported, result.ImportWarnings)
	}
	joined := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(joined, "1 条订阅携带的相关设置已忽略未恢复") {
		t.Fatalf("receipt must name the scrubbed subscription count, warnings=%v", result.ImportWarnings)
	}
	if !strings.Contains(joined, "1 条订阅的中转范围白名单（relayIds）已折算为三档") {
		t.Fatalf("receipt must name the relayIds fold, warnings=%v", result.ImportWarnings)
	}

	subs, err := dst.ListSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	byToken := map[string]string{}
	for _, s := range subs {
		byToken[s.Token] = s.Options
	}
	legacy := byToken["tok-legacy-sub"]
	for _, k := range []string{`"regions"`, `"addFlag"`, `"chainLanding"`, `"ruleGroups"`, `"relayIds"`} {
		if strings.Contains(legacy, k) {
			t.Fatalf("stored legacy options still carry %s: %s", k, legacy)
		}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(legacy), &m); err != nil {
		t.Fatalf("stored legacy options not JSON: %v (%s)", err, legacy)
	}
	var lines string
	if err := json.Unmarshal(m["lines"], &lines); err != nil || lines != "relay" {
		t.Fatalf("legacy relayIds [7,9] must fold to lines=relay, stored %s", legacy)
	}
	if got := byToken["tok-clean-sub"]; got != `{"lines":"relay"}` {
		t.Fatalf("clean subscription options must pass through untouched, got %s", got)
	}
}
