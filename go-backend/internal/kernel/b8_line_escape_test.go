package kernel

import (
	"strings"
	"testing"
)

// F-081 (audit FINAL §5-B8 / A6-006): 行式格式（逗号分字段、首个等号
// 分键值）的凭据转义不一致——proxySurge 已全程 surgeEscape，但自建入站
// SurgeLine 与 QX 的 password/psk 直填未转义，凭据含逗号时字段错位。
// 口径（金快照 p2 系列为据）：劈字段的是逗号/换行；值内的 = 合法且
// 必须保留——SS 2022 密钥是 base64、自带 == 填充，转义不得动它。
// 修复前本测试失败：渲染行里出现未转义的 "a,b=c"。
func TestSurgeLineEscapesCredentialsF081(t *testing.T) {
	ss := Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
		Settings: Settings{Method: "aes-128-gcm", Password: "a,b=c"}}
	if err := Normalize(TypeXray, &ss); err != nil {
		t.Fatal(err)
	}
	line, ok := SurgeLine(Endpoint{Name: "ss 节点", Host: "1.2.3.4", Port: 8388, Inbound: ss, Direct: true})
	if !ok {
		t.Fatal("ss endpoint must render a Surge line")
	}
	if strings.Contains(line, "a,b=c") {
		t.Fatalf("raw credential leaked into line format: %s", line)
	}
	if !strings.Contains(line, "password=a b=c") {
		t.Fatalf("password comma must be escaped, equals preserved: %s", line)
	}
	if n := strings.Count(line, ", "); n != 5 {
		t.Fatalf("field count must stay stable (6 fields), got %d separators: %s", n, line)
	}

	// Snell PSK 在保存期有字符集约束（Normalize 拒绝逗号），故自建路径
	// 的逗号 PSK 只能来自存量/直写行——渲染层仍须兜底转义，直构 struct
	// 模拟该行（与 F-022 的渲染兜底用例同理）。
	snell := Inbound{Protocol: ProtocolSnell, Port: 8389,
		Settings: Settings{Password: "p,k=1", SnellVersion: 5}}
	line, ok = SurgeLine(Endpoint{Name: "snell 节点", Host: "1.2.3.4", Port: 8389, Inbound: snell, Direct: true})
	if !ok || strings.Contains(line, "p,k=1") || !strings.Contains(line, "psk=p k=1") {
		t.Fatalf("snell psk must be escaped: %q", line)
	}
}

func TestLineFormatsPreserveSS2022KeyPaddingF081(t *testing.T) {
	// SS 2022 密钥的 == 填充在 SurgeLine（自建）与 proxySurge（导入）两条
	// 路径都必须原样保留。proxySurge 路径修复前失败：旧 surgeEscape 把
	// = 替换成空格，导入的 2022 节点密码被改坏。
	const key = "AAAAAAAAAAAAAAAAAAAAAA=="
	self := Inbound{Protocol: ProtocolShadowsocks, Port: 8388,
		Settings: Settings{Method: DefaultSSMethod, Password: key}}
	if err := Normalize(TypeSingbox, &self); err != nil {
		t.Fatal(err)
	}
	line, ok := SurgeLine(Endpoint{Name: "ss2022", Host: "1.2.3.4", Port: 8388, Inbound: self, Direct: true})
	if !ok || !strings.Contains(line, "password="+key) {
		t.Fatalf("SurgeLine must preserve the 2022 key verbatim: %q", line)
	}
	eps := []Endpoint{{Name: "imp2022", Host: "5.6.7.8", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "5.6.7.8", "port": 8388,
			"cipher": DefaultSSMethod, "password": key}}}
	body, _, err := Subscription(FormatSurge, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "password="+key) {
		t.Fatalf("proxySurge must preserve the 2022 key verbatim: %s", body)
	}
	body, _, err = Subscription(FormatQX, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "password="+key) {
		t.Fatalf("QX must preserve the 2022 key verbatim: %s", body)
	}
}

func TestQXLineEscapesPasswordF081(t *testing.T) {
	eps := []Endpoint{{Name: "机场 ss", Host: "5.6.7.8", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "5.6.7.8", "port": 8388,
			"cipher": "aes-128-gcm", "password": "a,b=c"}}}
	body, _, err := Subscription(FormatQX, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "a,b=c") || !strings.Contains(body, "password=a b=c") {
		t.Fatalf("QX password must be escaped (comma only, equals preserved): %s", body)
	}
}

func TestProxySurgeEscapesSnellPSKF081(t *testing.T) {
	// 变体：proxySurge 其余协议都过 surgeEscape，唯 Snell psk 直填。
	eps := []Endpoint{{Name: "snell", Host: "5.6.7.8", Port: 8389, Direct: true,
		Proxy: Proxy{"type": "snell", "server": "5.6.7.8", "port": 8389,
			"psk": "p,k=1", "version": 5}}}
	body, _, err := Subscription(FormatSurge, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "p,k=1") || !strings.Contains(body, "psk=p k=1") {
		t.Fatalf("proxySurge snell psk must be escaped: %s", body)
	}
}

func TestLoonLineEscapesShadowTLSPasswordF081(t *testing.T) {
	// 变体：loonLine 的 shadow-tls-password 未走 loonEscape 也未 %q 引号。
	eps := []Endpoint{{Name: "stls ss", Host: "5.6.7.8", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "5.6.7.8", "port": 8388,
			"cipher": "aes-128-gcm", "password": "pw",
			"plugin": "shadow-tls",
			"plugin-opts": map[string]interface{}{"password": "s,k=1", "host": "h.example.com", "version": 3}}}}
	body, _, err := Subscription(FormatLoon, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "s,k=1") || !strings.Contains(body, "shadow-tls-password=s k=1") {
		t.Fatalf("loon shadow-tls password must be escaped: %s", body)
	}
}
