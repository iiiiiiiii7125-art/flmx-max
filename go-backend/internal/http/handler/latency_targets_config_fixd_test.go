package handler

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// FIX-D / A2-01=A7-01：latency_targets 保存必须真正走
// normalizeLatencyTargetsConfig 校验。此前 normalizeAndValidateConfigValue
// 对该键走 default 原样放行：非法 JSON / 超过 30 个 / 名称超长等坏值保存
// 成功，随后 onLatencyConfigChanged 以空池同步、把每个节点的自选测试点
// 清空且全部延迟测量静默停摆。

const fixdValidTarget = `{"name":"p1","host":"203.0.113.1","port":80,"enabled":true}`

func fixdTargetsJSON(n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"pt-%d","host":"203.0.113.%d","port":80,"enabled":true}`, i, i%250+1)
	}
	b.WriteString("]")
	return b.String()
}

func TestLatencyTargetsConfigValidationWired(t *testing.T) {
	router, r := setupConfigAccessTestRouter(t)
	adminToken := mustGenerateConfigAccessToken(t, 1, "admin_user", 0)
	now := time.Now().UnixMilli()

	// 既有合法配置 + 一个带自选测试点的节点。
	seedConfigValue(t, r, "latency_targets", "["+fixdValidTarget+"]")
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, port, created_time, updated_time, status, latency_targets)
		VALUES('fixd-node', 'fixd-secret', '203.0.113.9', '1000-2000', ?, ?, 1, '{"points":["p1"],"pinned":[]}')
	`, now, now).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	storedConfig := func() string {
		cfg, err := r.GetConfigByName("latency_targets")
		if err != nil || cfg == nil {
			return ""
		}
		return cfg.Value
	}
	nodeChoice := func() string {
		var v string
		if err := r.DB().Raw(`SELECT latency_targets FROM node WHERE name = 'fixd-node'`).Scan(&v).Error; err != nil {
			t.Fatalf("read node choice: %v", err)
		}
		return v
	}

	// 用例逐个直调（值经 JSON 转义后提交）。
	cases := []struct {
		name  string
		value string
	}{
		{"invalid json", "not-json"},
		{"31 targets", fixdTargetsJSON(31)},
		{"name 21 runes", `[{"name":"` + strings.Repeat("测", 21) + `","host":"203.0.113.1","port":80,"enabled":true}]`},
		{"empty host", `[{"name":"p1","host":"","port":80,"enabled":true}]`},
		{"duplicate name", `[{"name":"p1","host":"203.0.113.1","port":80},{"name":"p1","host":"203.0.113.2","port":80}]`},
	}
	for _, tc := range cases {
		body := `{"latency_targets":` + quoteJSONString(tc.value) + `}`
		resp := postG3(t, router, "/api/v1/config/update", adminToken, body)
		assertHandlerCodeMsgContains(t, resp, "测试点")
		if got := storedConfig(); !strings.Contains(got, `"p1"`) || strings.Contains(got, "pt-0") {
			t.Fatalf("%s: stored config mutated after rejected save: %q", tc.name, got)
		}
		if got := nodeChoice(); got != `{"points":["p1"],"pinned":[]}` {
			t.Fatalf("%s: node choice cleared by rejected save: %q", tc.name, got)
		}
	}

	// update-single 路径同样拒绝。
	resp := postG3(t, router, "/api/v1/config/update-single", adminToken,
		`{"name":"latency_targets","value":"not-json"}`)
	assertHandlerCodeMsgContains(t, resp, "测试点")
	if got := storedConfig(); !strings.Contains(got, `"p1"`) {
		t.Fatalf("single: stored config mutated: %q", got)
	}

	// 合法值（恰好 30 个）应保存成功并规范化落库。
	resp = postG3(t, router, "/api/v1/config/update", adminToken,
		`{"latency_targets":`+quoteJSONString(fixdTargetsJSON(30))+`}`)
	assertHandlerCode(t, resp, 0)
	pool, err := parseLatencyTargets(storedConfig())
	if err != nil || len(pool) != 30 {
		t.Fatalf("stored 30 targets not parseable: %v (%q)", err, storedConfig())
	}

	// 合法单点经 update-single 保存成功。
	resp = postG3(t, router, "/api/v1/config/update-single", adminToken,
		`{"name":"latency_targets","value":`+quoteJSONString("["+fixdValidTarget+"]")+`}`)
	assertHandlerCode(t, resp, 0)
	pool, err = parseLatencyTargets(storedConfig())
	if err != nil || len(pool) != 1 || pool[0].Name != "p1" {
		t.Fatalf("stored single target: %v (%q)", err, storedConfig())
	}
}

// quoteJSONString 把字符串包成 JSON 字符串字面量（测试拼 body 用）。
func quoteJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
