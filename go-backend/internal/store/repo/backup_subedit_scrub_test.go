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
// 订阅级链式 + 规则与脚本）在导入时真剔除（不只是忽略）并计数、
// 在 ImportWarnings 聚合点名（E-02 删除先例口径）。relayIds 白名单
// 是活选项（逐端点精确集），导入时原样保留、不折算 lines。

func TestScrubRemovedSubOptions(t *testing.T) {
	// 已删键非默认 + relayIds [0]：剔已删键、白名单原样保留、
	// 不生成 lines 折算。
	out, removed := scrubRemovedSubOptions(
		`{"regions":["🇭🇰 香港"],"rules":true,"script":"s","chainRelay":"x","relayIds":[0],"templates":{"clash":3}}`)
	if !removed {
		t.Fatalf("removed=%v, want true", removed)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"regions", "rules", "script", "chainRelay"} {
		if _, ok := m[k]; ok {
			t.Fatalf("removed key %q survived scrub: %s", k, out)
		}
	}
	if _, ok := m["templates"]; !ok {
		t.Fatalf("templates must survive scrub: %s", out)
	}
	var ids []int64
	if err := json.Unmarshal(m["relayIds"], &ids); err != nil || len(ids) != 1 || ids[0] != 0 {
		t.Fatalf("relayIds whitelist must survive the scrub verbatim: %s", out)
	}
	if _, ok := m["lines"]; ok {
		t.Fatalf("relayIds must not fold into lines: %s", out)
	}

	// 已删键仅默认值时不计数（但键本身仍被剔除——删净口径）；
	// 显式 lines 与白名单互不影响、双双原样。
	out, removed = scrubRemovedSubOptions(`{"lines":"relay","relayIds":[0],"rules":false,"dedupe":false}`)
	if removed {
		t.Fatalf("default-only removed values must not count: removed=%v", removed)
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["rules"]; ok {
		t.Fatalf("default-valued removed key must still be scrubbed: %s", out)
	}
	var lines string
	if err := json.Unmarshal(m["lines"], &lines); err != nil || lines != "relay" {
		t.Fatalf("explicit lines must pass through verbatim: %q", lines)
	}
	if err := json.Unmarshal(m["relayIds"], &ids); err != nil || len(ids) != 1 || ids[0] != 0 {
		t.Fatalf("relayIds must pass through verbatim next to lines: %s", out)
	}

	// 只带白名单的 Options 无已删键 → 原样返回、不计数。
	whitelistOnly := `{"relayIds":[2,3]}`
	if out, removed := scrubRemovedSubOptions(whitelistOnly); out != whitelistOnly || removed {
		t.Fatalf("whitelist-only options altered: %q %v", out, removed)
	}

	// 干净 Options 原样返回；坏 JSON 不动。
	clean := `{"lines":"direct","templates":{"clash":3}}`
	if out, removed := scrubRemovedSubOptions(clean); out != clean || removed {
		t.Fatalf("clean options altered: %q %v", out, removed)
	}
	if out, removed := scrubRemovedSubOptions(`{oops`); out != `{oops` || removed {
		t.Fatalf("invalid JSON must pass through: %q %v", out, removed)
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
	if strings.Contains(joined, "relayIds") {
		t.Fatalf("receipt must not claim a relayIds fold — the whitelist is restored verbatim, warnings=%v", result.ImportWarnings)
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
	for _, k := range []string{`"regions"`, `"addFlag"`, `"chainLanding"`, `"ruleGroups"`} {
		if strings.Contains(legacy, k) {
			t.Fatalf("stored legacy options still carry %s: %s", k, legacy)
		}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(legacy), &m); err != nil {
		t.Fatalf("stored legacy options not JSON: %v (%s)", err, legacy)
	}
	var ids []int64
	if err := json.Unmarshal(m["relayIds"], &ids); err != nil || len(ids) != 2 || ids[0] != 7 || ids[1] != 9 {
		t.Fatalf("legacy relayIds [7,9] must be restored verbatim, stored %s", legacy)
	}
	if _, ok := m["lines"]; ok {
		t.Fatalf("relayIds must not fold into lines on restore, stored %s", legacy)
	}
	if got := byToken["tok-clean-sub"]; got != `{"lines":"relay"}` {
		t.Fatalf("clean subscription options must pass through untouched, got %s", got)
	}
}
