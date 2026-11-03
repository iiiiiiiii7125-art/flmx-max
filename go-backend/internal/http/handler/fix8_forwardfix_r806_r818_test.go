package handler

import (
	"strings"
	"testing"
)

// 修复轮 8 路 forwardfix：R8-06（H-F3-01，低）＋R8-18（H-F3-03，信息→裁定）。
//
// R8-06：消费侧共享切分器 splitRemoteTargets 只切逗号，与校验侧
// splitRemoteParts（逗号/换行/CR；forward.md 明文承诺「逗号或换行
// 分隔」）口径分歧——换行双目标创建两面 code=0、逐段校验通过，渲
// 染却成 1 个含 \n 的不可拨号节点、双目标静默全灭。台账裁定 4：以
// 校验侧为准统一，splitRemoteTargets 直接复用 splitRemoteParts；
// realm 策略门的目标计数同族对齐（此前只切逗号、换行双目标误计
// 单目标放行 fifo，渲染仍是双目标 roundrobin）。存量换行形态行在
// 新口径下自动按多目标渲染（复活属预期、无需数据迁移）。
//
// R8-18：实测重复段在消费侧非惰性（gost round/rand/hash 按节点条
// 目计权、realm 重复段排等权 roundrobin 条目、诊断逐条目探测），
// 变更判定保留多重集语义（去重/增重＝变更），口径写入 forward.md
// 与谓词头注，本文件钉住去重编辑仍触发复检的行为格。
//
// 基线（e7dac59b）红点：splitRemoteTargets(换行双目标) ＝ 1 段含
// \n；存量换行行 gost 渲染 nodes ＝ 1；realm 门对换行双目标＋fifo
// 返回 nil。

// ---- R8-06 切分器单元：逗号全集回归＋换行族新口径 ----

func TestFix8R806SplitRemoteTargetsUnifiedCaliber(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// 逗号形态全集（修复前语义逐字不变）。
		{"203.0.113.5:443", []string{"203.0.113.5:443"}},
		{"203.0.113.5:443,198.51.100.7:443", []string{"203.0.113.5:443", "198.51.100.7:443"}},
		{"203.0.113.5:443,,198.51.100.7:443", []string{"203.0.113.5:443", "198.51.100.7:443"}},
		{",203.0.113.5:443,", []string{"203.0.113.5:443"}},
		{" 203.0.113.5:443 , 198.51.100.7:443 ", []string{"203.0.113.5:443", "198.51.100.7:443"}},
		{"[2001:db8::1]:443,203.0.113.5:443", []string{"[2001:db8::1]:443", "203.0.113.5:443"}},
		{"2001:db8::1:443", []string{"[2001:db8::1]:443"}},
		{",,,", nil},
		{"", nil},
		{"  ", nil},
		// 换行/CR/CRLF 形态（修复点：与校验侧同作分隔符）。
		{"203.0.113.5:443\n198.51.100.7:443", []string{"203.0.113.5:443", "198.51.100.7:443"}},
		{"203.0.113.5:443\r\n198.51.100.7:443", []string{"203.0.113.5:443", "198.51.100.7:443"}},
		{"203.0.113.5:443\r198.51.100.7:443", []string{"203.0.113.5:443", "198.51.100.7:443"}},
		{"203.0.113.5:443,198.51.100.7:443\n203.0.113.9:443", []string{"203.0.113.5:443", "198.51.100.7:443", "203.0.113.9:443"}},
		{"203.0.113.5:443\n", []string{"203.0.113.5:443"}},
		{"\n203.0.113.5:443", []string{"203.0.113.5:443"}},
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
			if strings.ContainsAny(got[i], "\n\r") {
				t.Errorf("splitRemoteTargets(%q)[%d] still carries a separator: %q", c.in, i, got[i])
			}
		}
		// 恒等不变量：消费段集与校验段集逐段对应（渲染归一后可拨号解析）。
		if len(got) != len(splitRemoteParts(c.in)) {
			t.Errorf("splitRemoteTargets(%q) count %d != splitRemoteParts count %d", c.in, len(got), len(splitRemoteParts(c.in)))
		}
	}
	// 渲染出的每段都必须可被 parseTargetAddress 解析（修复前换行形
	// 整体一段在此直接失败＝不可拨号的实证面）。
	for _, target := range splitRemoteTargets("203.0.113.5:443\n198.51.100.7:443") {
		if _, _, err := parseTargetAddress(target); err != nil {
			t.Errorf("rendered target %q must be dialable-parseable, got %v", target, err)
		}
	}
}

// ---- R8-06 旅程：存量换行行四消费面渲染正确 ----

func TestFix8R806StoredNewlineForwardRendersEverywhere(t *testing.T) {
	fx := setupFix7ForwardVal(t)
	fx.setSwitch(t, false)

	// API 创建面：换行双目标逐段校验通过、落库（修复前亦 code=0，
	// 坏的是其后的渲染——本格钉住创建→渲染全链）。
	code, msg := fx.callCreate(t, 2, "member", 1, map[string]interface{}{
		"tunnelId": fx.tunnelID, "name": "f8r6-newline", "remoteAddr": "203.0.113.5:443\n198.51.100.7:443", "inPort": 15301,
	})
	if code != 0 {
		t.Fatalf("member create newline dual-target must succeed, got code=%d msg=%s", code, msg)
	}
	var createdID int64
	if err := fx.r.DB().Raw(`SELECT id FROM forward WHERE name = 'f8r6-newline'`).Scan(&createdID).Error; err != nil {
		t.Fatalf("lookup created forward: %v", err)
	}
	if got := fx.storedRemoteAddr(t, createdID); !strings.Contains(got, "\n") {
		t.Fatalf("fixture premise: stored value must keep the newline form, got %q", got)
	}

	// gost 渲染面：两协议服务各 2 个节点、地址恰为两目标、无 \n。
	fwd, err := fx.h.getForwardRecord(createdID)
	if err != nil {
		t.Fatalf("get forward record: %v", err)
	}
	node := &nodeRecord{TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	svcs := buildForwardServiceConfigs("f8r6", fwd, nil, node, 15301, "", forwardRuntimeLimiters{})
	if len(svcs) != 2 {
		t.Fatalf("expected tcp+udp services, got %d", len(svcs))
	}
	for _, svc := range svcs {
		nodes := svc["forwarder"].(map[string]interface{})["nodes"].([]map[string]interface{})
		if len(nodes) != 2 {
			t.Fatalf("service %v must render 2 forwarder nodes, got %d (%v)", svc["name"], len(nodes), nodes)
		}
		if nodes[0]["addr"] != "203.0.113.5:443" || nodes[1]["addr"] != "198.51.100.7:443" {
			t.Fatalf("rendered node addrs wrong: %v", nodes)
		}
	}

	// realm 端点面：Remotes 恰两段。
	remotes := splitRemoteTargets(fwd.RemoteAddr)
	if len(remotes) != 2 {
		t.Fatalf("realm remotes must be 2, got %v", remotes)
	}

	// 诊断面：两目标都可解析成行。
	targets, err := resolveDiagnosisTargets(fwd.RemoteAddr)
	if err != nil {
		t.Fatalf("diagnosis targets must resolve, got %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("diagnosis must see 2 targets, got %v", targets)
	}

	// 中转延迟面：取首目标，须为第一段而非含 \n 的整串。
	if first := splitRemoteTargets(fwd.RemoteAddr)[0]; first != "203.0.113.5:443" {
		t.Fatalf("relay latency first target = %q", first)
	}

	// 对照：逗号存量行渲染逐字不变（回归）。
	idComma := fx.seedMemberForward(t, "f8r6-comma", "203.0.113.5:443,198.51.100.7:443", 15302)
	fwdComma, err := fx.h.getForwardRecord(idComma)
	if err != nil {
		t.Fatal(err)
	}
	svcsComma := buildForwardServiceConfigs("f8r6", fwdComma, nil, node, 15302, "", forwardRuntimeLimiters{})
	nodesComma := svcsComma[0]["forwarder"].(map[string]interface{})["nodes"].([]map[string]interface{})
	if len(nodesComma) != 2 || nodesComma[0]["addr"] != "203.0.113.5:443" || nodesComma[1]["addr"] != "198.51.100.7:443" {
		t.Fatalf("comma-form rendering regressed: %v", nodesComma)
	}
}

// ---- R8-06 realm 策略门计数对齐 ----

func TestFix8R806RealmStrategyGateCountsNewlineTargets(t *testing.T) {
	realmTunnel := &tunnelRecord{Engine: tunnelEngineRealm}
	gostTunnel := &tunnelRecord{Engine: ""}

	// 换行双目标＋fifo：修复前误计单目标放行（渲染却是双目标
	// roundrobin），修复后与逗号双目标同判拒绝。
	if err := validateRealmForwardStrategy(realmTunnel, "203.0.113.5:443\n198.51.100.7:443", "fifo"); err == nil {
		t.Errorf("realm gate must reject fifo for newline-separated dual targets")
	}
	if err := validateRealmForwardStrategy(realmTunnel, "203.0.113.5:443,198.51.100.7:443", "fifo"); err == nil {
		t.Errorf("realm gate must keep rejecting fifo for comma dual targets")
	}
	// round/hash 放行、单目标不拦、非 realm 隧道不拦。
	if err := validateRealmForwardStrategy(realmTunnel, "203.0.113.5:443\n198.51.100.7:443", "round"); err != nil {
		t.Errorf("realm gate must allow round for newline dual targets, got %v", err)
	}
	if err := validateRealmForwardStrategy(realmTunnel, "203.0.113.5:443", "fifo"); err != nil {
		t.Errorf("realm gate must not block single-target fifo, got %v", err)
	}
	if err := validateRealmForwardStrategy(gostTunnel, "203.0.113.5:443\n198.51.100.7:443", "fifo"); err != nil {
		t.Errorf("realm gate must not apply to gost tunnels, got %v", err)
	}
}

// ---- R8-18 多重集口径钉格 ----

func TestFix8R818TargetsChangedKeepsMultisetSemantics(t *testing.T) {
	// 谓词级：去重/增重＝变更（实测依据见 security_utils.go 头注），
	// 重排/逐字相同＝未变更。
	cases := []struct {
		a, b string
		want bool
	}{
		{"10.9.9.9:443,10.9.9.9:443", "10.9.9.9:443", true}, // 去重
		{"10.9.9.9:443", "10.9.9.9:443,10.9.9.9:443", true}, // 增重
		{"10.9.9.9:443,10.9.9.9:443", "10.9.9.9:443,10.9.9.9:443", false},
		{"10.9.9.9:443,10.9.9.10:443", "10.9.9.10:443,10.9.9.9:443", false}, // 重排
	}
	for _, c := range cases {
		if got := remoteAddrTargetsChanged(c.a, c.b); got != c.want {
			t.Errorf("remoteAddrTargetsChanged(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}

	// handler 级：开关关＋存量内网重复目标，所有者去重编辑 → 判变更
	// → 复检 403、存量不动（多重集口径的冻结格按裁定保留并文档化，
	// 不是缺陷）；重排编辑 → 未变更 code=0。
	fx := setupFix7ForwardVal(t)
	fx.setSwitch(t, false)

	idDup := fx.seedMemberForward(t, "f8r8-dup", "10.9.9.9:443,10.9.9.9:443", 15401)
	code, msg := fx.callUpdate(t, 2, "member", 1, map[string]interface{}{"id": idDup, "remoteAddr": "10.9.9.9:443"})
	if code != 403 || !strings.Contains(msg, "resolves to internal IP") {
		t.Fatalf("dedup edit of internal target must be treated as change and rechecked (403), got code=%d msg=%s", code, msg)
	}
	if got := fx.storedRemoteAddr(t, idDup); got != "10.9.9.9:443,10.9.9.9:443" {
		t.Fatalf("rejected dedup edit must not persist, stored = %q", got)
	}

	idReorder := fx.seedMemberForward(t, "f8r8-reorder", "10.9.9.9:443,10.9.9.10:443", 15402)
	code, msg = fx.callUpdate(t, 2, "member", 1, map[string]interface{}{"id": idReorder, "remoteAddr": "10.9.9.10:443,10.9.9.9:443"})
	if code != 0 {
		t.Fatalf("reorder-only edit must stay un-rechecked and succeed, got code=%d msg=%s", code, msg)
	}
}
