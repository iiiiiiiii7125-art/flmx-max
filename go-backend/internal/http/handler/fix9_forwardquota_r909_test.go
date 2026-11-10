package handler

import (
	"strings"
	"testing"
	"unicode"
)

// 修复轮 9 路 forwardquota：R9-09（I-F3-01，低）＋I-F3-03（信息→裁定落字）。
//
// R9-09：零目标谓词按归一前段集计数，消费侧 splitRemoteTargets→
// processServerAddress→normalizeServerAddressInput 剥 scheme、首个
// /?# 截断——"#"/"http://"/"#frag" 过全部写面门而消费得零有效目
// 标、"1.2.3.4:80,#" 渲染空节点。台账裁定 §5.6：零目标判改按归
// 一后有效段集计，四写面（创建/更新/管理/恢复）共用同一定义，管
// 理员 netguard 豁免不动。I-F3-03 同批裁定：纯 Unicode 格式符
// （Cf）段不计入有效段（与 R8-01 主题值轴先例同口径），forward.md
// 与谓词头注同落。
//
// 基线（acb03403）红点：validateRemoteAddrHasTargets("#") = nil；
// splitRemoteTargets("1.2.3.4:80,#") = ["1.2.3.4:80", ""]；管理员
// 创建 remoteAddr="#" code=0 落库。

// ---- 谓词单元：归一后有效段集口径（与 repo 镜像表逐行对齐） ----

func TestFix9R909HasTargetsUsesNormalizedEffectiveSet(t *testing.T) {
	reject := []string{
		// R7-04 原全集（归一前段集为空）不回归。
		"", ",,,", " , , ", "\n,\r\n,", "  ",
		// R9-09 最小反例全集：段集非空、归一后零有效目标。
		"#", "/", "?", "#frag", "/path", "?q=1",
		"http://", "https://", "a://", "http://,https://", "#,?",
		"#\nhttp://\r#x",
		// I-F3-03：纯 Cf 段不计入有效段（单段与混排全死同判）。
		"\u200b", "\u200b\u2060", "\ufeff", "\u00ad", " \u200b ", "#,\u200b",
	}
	for _, addr := range reject {
		if err := validateRemoteAddrHasTargets(addr); err == nil {
			t.Errorf("validateRemoteAddrHasTargets(%q) must reject (no effective target after normalization)", addr)
		} else if !strings.Contains(err.Error(), "目标地址不能为空") {
			t.Errorf("validateRemoteAddrHasTargets(%q) error must be the empty-target family, got %v", addr, err)
		}
	}
	accept := []string{
		"203.0.113.5:443",
		"203.0.113.5:443,198.51.100.7:443",
		"1.2.3.4:80,#",       // 混排：一个有效目标即非零目标（空段由消费侧丢弃）
		"1.2.3.4:80,\u200b",  // 混排 Cf 死段同上
		"http://:8080",       // 归一为 ":8080" 非空——按有效计（可拨性归段校验/豁免口径）
		"://",                // 归一为 ":" 非空，同上（定义边界钉格）
		"example.com:8080/p", // 路径在消费侧丢弃（文档口径），段本身有效
		"\u200b203.0.113.5:443",
		"a..b:443", // 空标签是另一道门，本谓词不判
	}
	for _, addr := range accept {
		if err := validateRemoteAddrHasTargets(addr); err != nil {
			t.Errorf("validateRemoteAddrHasTargets(%q) must accept (has effective target), got %v", addr, err)
		}
	}
	// IsSafeRemoteAddr 本体同口径：死形态在段校验之前被格式判拒绝
	// （成员面此前靠 DNS 失败挡下，报错口径自此统一为零目标族）。
	old := DisableSafeRemoteAddrCheckForTesting
	DisableSafeRemoteAddrCheckForTesting = false
	defer func() { DisableSafeRemoteAddrCheckForTesting = old }()
	for _, addr := range []string{"#", "http://", "#frag", "\u200b"} {
		err := IsSafeRemoteAddr(addr)
		if err == nil || !strings.Contains(err.Error(), "目标地址不能为空") {
			t.Errorf("IsSafeRemoteAddr(%q) must reject via the zero-target format gate, got %v", addr, err)
		}
	}
}

// ---- 消费侧：有效段过滤（空节点钉格） ----

func TestFix9R909SplitRemoteTargetsDropsDeadParts(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"1.2.3.4:80,#", []string{"1.2.3.4:80"}}, // 钉格：不再渲染空节点
		{"#", nil},
		{"http://", nil},
		{"#frag", nil},
		{"\u200b", nil},
		{"203.0.113.5:443\n#", []string{"203.0.113.5:443"}},
		{"#,203.0.113.5:443,http://", []string{"203.0.113.5:443"}},
		{"example.com:8080/p", []string{"example.com:8080"}}, // 路径丢弃与 forward.md 口径一致
		{"example.com:8080?q=1", []string{"example.com:8080"}},
		{"example.com:8080#f", []string{"example.com:8080"}},
		{"http://example.com:8080/p", []string{"example.com:8080"}},
		// R8-06 逗号/换行全集不回归（逐字同 fix8 表）。
		{"203.0.113.5:443,198.51.100.7:443", []string{"203.0.113.5:443", "198.51.100.7:443"}},
		{"203.0.113.5:443\n198.51.100.7:443", []string{"203.0.113.5:443", "198.51.100.7:443"}},
		{"2001:db8::1:443", []string{"[2001:db8::1]:443"}},
		{",,,", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := splitRemoteTargets(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitRemoteTargets(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitRemoteTargets(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
			if got[i] == "" {
				t.Errorf("splitRemoteTargets(%q) rendered an empty node", c.in)
			}
		}
	}
	// 诊断面：混排死段不挡真目标；全死显式收口。
	targets, err := resolveDiagnosisTargets("1.2.3.4:80,#")
	if err != nil || len(targets) != 1 {
		t.Errorf("resolveDiagnosisTargets(1.2.3.4:80,#) = %v, %v; want 1 target", targets, err)
	}
	if _, err := resolveDiagnosisTargets("#"); err == nil {
		t.Errorf("resolveDiagnosisTargets(#) must fail explicitly on zero effective targets")
	}
}

// testPartDead 是有效性分类的独立推导（不调用被测实现）：剥 scheme
// （首个 "://" 后余量）、首个 /?# 截断，再判是否只剩空白/Cf。
// 仅用于下方定向语料——语料中的 scheme 形态均选 Host 与朴素推导
// 一致者（空余量或 host:port 形），url.Parse 的 Host 优先分支在
// 这些形态上与朴素推导同值。
func testPartDead(part string) bool {
	s := strings.TrimSpace(part)
	if idx := strings.Index(s, "://"); idx > 0 {
		s = s[idx+3:]
	}
	if idx := strings.IndexAny(s, "/?#"); idx >= 0 {
		s = s[:idx]
	}
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			continue
		}
		return false
	}
	return true
}

// I-F3 性质面定向复证（28 万语料的定向子集＋关键钉格）：切分统一
// 在有效段上成立、多重集口径不变。
func TestFix9R909PropertySubsetReproof(t *testing.T) {
	corpus := []string{
		"203.0.113.5:443",
		"203.0.113.5:443,198.51.100.7:443",
		"203.0.113.5:443\n198.51.100.7:443",
		"203.0.113.5:443\r\n198.51.100.7:443\r203.0.113.9:443",
		" 203.0.113.5:443 , \t198.51.100.7:443 ",
		"203.0.113.5:443,,198.51.100.7:443",
		"[2001:db8::1]:443,203.0.113.5:443",
		"2001:db8::1:443",
		"example.com:8080/p,203.0.113.5:443",
		"http://example.com:8080/p",
		"http://203.0.113.5:443",
		"1.2.3.4:80,#",
		"#,203.0.113.5:443",
		"203.0.113.5:443\n#frag",
		"1.2.3.4:80,\u200b",
		"\u200b,203.0.113.5:443",
		"#", "http://", "\u200b", ",,,", "", " ",
		"203.0.113.5:443,203.0.113.5:443",
		strings.Repeat("203.0.113.5:443,", 200) + "198.51.100.7:443", // 长段串
	}
	for _, in := range corpus {
		parts := splitRemoteParts(in)
		var want []string
		dead := 0
		for _, p := range parts {
			if testPartDead(p) {
				dead++
				continue
			}
			want = append(want, processServerAddress(p))
		}
		got := splitRemoteTargets(in)
		if len(got) != len(want) {
			t.Errorf("corpus %q: splitRemoteTargets count %d != independent expectation %d (%v vs %v)", in, len(got), len(want), got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("corpus %q: target[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
		// 切分统一不变量在其有效域上成立：无死段时消费计数与校验
		// 计数恒等（R8-06/I-F3 性质 1）；有死段时差恰为死段数。
		if len(got) != len(parts)-dead {
			t.Errorf("corpus %q: count %d != parts %d - dead %d", in, len(got), len(parts), dead)
		}
		// 谓词与独立分类对撞：全死 ⇔ 拒绝。
		err := validateRemoteAddrHasTargets(in)
		if dead == len(parts) && err == nil {
			t.Errorf("corpus %q: all parts dead but predicate accepted", in)
		}
		if dead < len(parts) && err != nil {
			t.Errorf("corpus %q: has live parts but predicate rejected: %v", in, err)
		}
	}

	// 多重集口径关键钉格（I-F3 性质 3 的定向子集，实现未动、口径
	// 复证）：重排/分隔符形态变换/可裁填充变换＝未变更；去重/增重
	// ＝变更。
	multiset := []struct {
		a, b string
		want bool
	}{
		{"10.9.9.9:443,10.9.9.10:443", "10.9.9.10:443,10.9.9.9:443", false},
		{"10.9.9.9:443,10.9.9.10:443", "10.9.9.9:443\n10.9.9.10:443", false},
		{"10.9.9.9:443,10.9.9.10:443", "10.9.9.9:443\r10.9.9.10:443", false},
		{" 10.9.9.9:443 , 10.9.9.10:443 ", "10.9.9.9:443,10.9.9.10:443", false},
		{"10.9.9.9:443,10.9.9.9:443", "10.9.9.9:443", true},
		{"10.9.9.9:443", "10.9.9.9:443,10.9.9.9:443", true},
	}
	for _, c := range multiset {
		if got := remoteAddrTargetsChanged(c.a, c.b); got != c.want {
			t.Errorf("remoteAddrTargetsChanged(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// ---- 写面旅程：创建/更新/管理三面＋渲染 ----

func TestFix9R909WriteSurfacesRejectNormalizedZeroTargets(t *testing.T) {
	fx := setupFix7ForwardVal(t)
	fx.setSwitch(t, false)

	// 管理员创建 "#"：基线 code=0 落库，修复后格式判拒绝、不落库
	// （管理员豁免只及 netguard 段校验，不及零有效目标格式判）。
	code, msg := fx.callCreate(t, 1, "admin", 0, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f9r9-admin-hash", "remoteAddr": "#", "inPort": 15601,
	})
	if code == 0 {
		t.Fatalf("admin create remoteAddr=\"#\" must be rejected, got success")
	}
	if !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("admin rejection msg must be the empty-target family, got %q", msg)
	}
	if n := fx.forwardCountNamed(t, "f9r9-admin-hash"); n != 0 {
		t.Fatalf("rejected admin create leaked a forward row: %d", n)
	}

	// 管理员创建 "http://" 同拒。
	code, msg = fx.callCreate(t, 1, "admin", 0, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f9r9-admin-scheme", "remoteAddr": "http://", "inPort": 15602,
	})
	if code == 0 || !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("admin create \"http://\" must be rejected with empty-target msg, got code=%d msg=%q", code, msg)
	}

	// 普通用户开关关创建 "#frag"：格式判先于段校验命中（不再靠
	// DNS 失败兜底），报错为零目标族。
	code, msg = fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f9r9-member-frag", "remoteAddr": "#frag", "inPort": 15603,
	})
	if code == 0 || !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("member create \"#frag\" must be rejected by the format gate, got code=%d msg=%q", code, msg)
	}

	// 开关开启时同拒（格式判与 netguard 开关无关）。
	fx.setSwitch(t, true)
	code, msg = fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f9r9-member-on", "remoteAddr": "http://", "inPort": 15604,
	})
	if code == 0 || !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("member create \"http://\" with switch on must be rejected, got code=%d msg=%q", code, msg)
	}

	// 混排正对照：一个有效目标＋死段，管理员与开关开成员均可建、
	// 原文落库（死段只在消费侧丢弃）。
	code, msg = fx.callCreate(t, 1, "admin", 0, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f9r9-admin-mixed", "remoteAddr": "1.2.3.4:80,#", "inPort": 15605,
	})
	if code != 0 {
		t.Fatalf("admin create mixed \"1.2.3.4:80,#\" must succeed, got code=%d msg=%s", code, msg)
	}
	code, msg = fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f9r9-member-mixed", "remoteAddr": "1.2.3.4:80,#", "inPort": 15606,
	})
	if code != 0 {
		t.Fatalf("member (switch on) create mixed must succeed, got code=%d msg=%s", code, msg)
	}
	fx.setSwitch(t, false)

	// 更新面：成员把存量目标改为 "#frag"、管理员改为 "http://"
	// 均拒且存量不动。
	id := fx.seedMemberForward(t, "f9r9-upd", "203.0.113.5:443", 15607)
	code, msg = fx.callUpdate(t, 2, "member", 1, map[string]interface{}{"id": id, "remoteAddr": "#frag"})
	if code == 0 || !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("member update to \"#frag\" must be rejected, got code=%d msg=%q", code, msg)
	}
	code, msg = fx.callUpdate(t, 1, "admin", 0, map[string]interface{}{"id": id, "remoteAddr": "http://"})
	if code == 0 || !strings.Contains(msg, "目标地址不能为空") {
		t.Fatalf("admin update to \"http://\" must be rejected, got code=%d msg=%q", code, msg)
	}
	if got := fx.storedRemoteAddr(t, id); got != "203.0.113.5:443" {
		t.Fatalf("rejected updates must not persist, stored = %q", got)
	}
}

// 渲染钉格：存量 "1.2.3.4:80,#" 行 gost 渲染恰 1 个真节点、无空
// 地址节点（基线红点：2 节点、其一 addr=""）。
func TestFix9R909StoredMixedRowRendersNoEmptyNode(t *testing.T) {
	fx := setupFix7ForwardVal(t)
	id := fx.seedMemberForward(t, "f9r9-render", "1.2.3.4:80,#", 15611)
	fwd, err := fx.h.getForwardRecord(id)
	if err != nil {
		t.Fatalf("get forward record: %v", err)
	}
	node := &nodeRecord{TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	svcs := buildForwardServiceConfigs("f9r9", fwd, nil, node, 15611, "", forwardRuntimeLimiters{}, false)
	if len(svcs) != 2 {
		t.Fatalf("expected tcp+udp services, got %d", len(svcs))
	}
	for _, svc := range svcs {
		nodes := svc["forwarder"].(map[string]interface{})["nodes"].([]map[string]interface{})
		if len(nodes) != 1 {
			t.Fatalf("service %v must render exactly 1 node, got %d (%v)", svc["name"], len(nodes), nodes)
		}
		if nodes[0]["addr"] != "1.2.3.4:80" {
			t.Fatalf("rendered node addr = %v, want 1.2.3.4:80", nodes[0]["addr"])
		}
	}
}
