package kernel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// 模板 × 信息节点组合（集成裁决的织入点）：Subscription 的模板分支以
// uniqueNamesExcept(withNotices(ApplySubOptions(...)), 模板组名) 织入
// 信息节点。此前全仓没有一处测试同时设置 Templates 与 Notices——任一
// 路改动织入顺序或模板引擎都可能静默丢占位节点。本文件逐槽位钉住：
// 两条信息节点与真实节点在六种组合下全部存活。
func TestSubscriptionTemplateWithNoticesAllSlots(t *testing.T) {
	eps := namedEndpoints("香港 01", "美国 01")
	notices := InfoNotices(time.Date(2027, 1, 2, 0, 0, 0, 0, time.Local).UnixMilli(), 1024, 4096)
	if len(notices) != 2 {
		t.Fatalf("InfoNotices must yield 2 notices, got %v", notices)
	}
	for _, tc := range []struct {
		name   string
		format string
		opts   SubOptions
	}{
		{"clash 自有模板", FormatClash, SubOptions{Templates: map[string]string{TemplateClash: clashTpl}, Notices: notices, Rules: true}},
		{"sing-box 自有模板", FormatSingbox, SubOptions{Templates: map[string]string{TemplateSingbox: singboxTpl}, Notices: notices}},
		{"surge 自有模板", FormatSurge, SubOptions{Templates: map[string]string{TemplateSurge: surgeTpl}, Notices: notices}},
		{"stash 自有模板", FormatStash, SubOptions{Templates: map[string]string{TemplateStash: clashTpl}, Notices: notices, Rules: true}},
		{"stash 继承 clash 模板", FormatStash, SubOptions{Templates: map[string]string{TemplateClash: clashTpl}, Notices: notices, Rules: true}},
		{"shadowrocket 自有模板", FormatShadowrocket, SubOptions{Templates: map[string]string{TemplateShadowrocket: clashTpl}, Notices: notices, Rules: true}},
	} {
		body, _, err := Subscription(tc.format, eps, tc.opts)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for _, want := range []string{notices[0], notices[1], "香港 01", "美国 01"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: output missing %q\n%s", tc.name, want, body)
			}
		}
	}
}

// 占位名与模板组名碰撞时，uniqueNamesExcept 必须给占位改名保命而非
// 与组同名双写：clashTpl 含组「♻️ 自动选择」，同名信息节点应以
// 「♻️ 自动选择 #2」作为代理出现，组本身保持原名。
func TestSubscriptionTemplateNoticeNameCollidingWithGroup(t *testing.T) {
	eps := namedEndpoints("香港 01")
	body, _, err := Subscription(FormatClash, eps, SubOptions{
		Templates: map[string]string{TemplateClash: clashTpl},
		Notices:   []string{"♻️ 自动选择"},
		Rules:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]interface{} `yaml:"proxies"`
		Groups  []map[string]interface{} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	proxyNames := map[string]bool{}
	for _, p := range cfg.Proxies {
		proxyNames[p["name"].(string)] = true
	}
	if !proxyNames["♻️ 自动选择 #2"] {
		t.Fatalf("colliding notice must be renamed, proxies: %v\n%s", proxyNames, body)
	}
	groupNames := map[string]bool{}
	for _, g := range cfg.Groups {
		groupNames[g["name"].(string)] = true
	}
	if !groupNames["♻️ 自动选择"] {
		t.Fatalf("template group must keep its name, groups: %v", groupNames)
	}
}

// 同名端点 × 模板的计数对照（A-sub-04 复核）：面板 NodeCount 走内置
// 计数（uniqueNames 改名保数），模板渲染走 uniqueNamesExcept——该函
// 数同样对端点间重名加后缀改名（只在预留集上与 uniqueNames 不同：
// 模板组名而非内置组名/地区名），两条路径都保数。逐槽位断言计数与
// 实得的不同名节点数相等，钉住这一口径，防未来改动把某条路径改成
// 原样双写。
func TestTemplateNodeCountMatchesDeliveredWithDuplicateNames(t *testing.T) {
	eps := namedEndpoints("同名 01", "同名 01", "美国 01")
	wantNames := map[string]bool{"同名 01": true, "同名 01 #2": true, "美国 01": true}

	clashNames := func(t *testing.T, body string) map[string]bool {
		t.Helper()
		var cfg struct {
			Proxies []map[string]interface{} `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatalf("%v\n%s", err, body)
		}
		out := map[string]bool{}
		for _, p := range cfg.Proxies {
			out[p["name"].(string)] = true
		}
		return out
	}

	for _, tc := range []struct {
		name   string
		format string
		opts   SubOptions
		names  func(t *testing.T, body string) map[string]bool
	}{
		{"clash", FormatClash, SubOptions{Templates: map[string]string{TemplateClash: clashTpl}, Rules: true}, clashNames},
		{"stash 自有", FormatStash, SubOptions{Templates: map[string]string{TemplateStash: clashTpl}, Rules: true}, clashNames},
		{"stash 继承", FormatStash, SubOptions{Templates: map[string]string{TemplateClash: clashTpl}, Rules: true}, clashNames},
		{"shadowrocket", FormatShadowrocket, SubOptions{Templates: map[string]string{TemplateShadowrocket: clashTpl}, Rules: true}, clashNames},
		{"sing-box", FormatSingbox, SubOptions{Templates: map[string]string{TemplateSingbox: singboxTpl}}, func(t *testing.T, body string) map[string]bool {
			var doc struct {
				Outbounds []map[string]interface{} `json:"outbounds"`
			}
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatalf("%v\n%s", err, body)
			}
			out := map[string]bool{}
			for _, o := range doc.Outbounds {
				tag, _ := o["tag"].(string)
				if wantNames[tag] {
					out[tag] = true
				}
			}
			return out
		}},
		{"surge", FormatSurge, SubOptions{Templates: map[string]string{TemplateSurge: surgeTpl}}, func(t *testing.T, body string) map[string]bool {
			proxy := body[strings.Index(body, "[Proxy]"):strings.Index(body, "[Proxy Group]")]
			out := map[string]bool{}
			for _, line := range strings.Split(proxy, "\n") {
				name, _, ok := strings.Cut(line, " = ")
				if ok && wantNames[name] {
					out[name] = true
				}
			}
			return out
		}},
	} {
		body, _, err := Subscription(tc.format, eps, tc.opts)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		delivered := tc.names(t, body)
		if len(delivered) != len(wantNames) {
			t.Fatalf("%s: delivered distinct node names = %v, want %v\n%s", tc.name, delivered, wantNames, body)
		}
		// 计数侧与面板同口径：countOpts 不带模板正文。
		countOpts := tc.opts
		countOpts.Templates = nil
		if got := SubscriptionNodeCount(tc.format, eps, countOpts); got != len(delivered) {
			t.Fatalf("%s: NodeCount = %d, delivered distinct = %d", tc.name, got, len(delivered))
		}
	}
}
