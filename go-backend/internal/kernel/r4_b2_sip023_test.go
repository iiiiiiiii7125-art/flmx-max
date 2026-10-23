package kernel

import (
	"encoding/base64"
	"strings"
	"testing"
)

// R4 批 2（裁定项 R4-SIP023-R，verdicts §4）：解除 sing-box 面对
// SIP023 多段 SS2022 节点的略过——proxySingbox 改逐段校验（与导入
// 期同一谓词 ValidateSS2022Password）→ 逐段归一为带填充 Std 渲染
// （复用 NormalizedSS2022PasswordSegments，与 Clash 面同 helper）；
// 任一段不合规整节点仍略过（存量坏 key 纵深兜底不变）；链式
// sing-box 出站经 singboxOutbounds 同函数同步解除、计数
// SubscriptionNodeCount 同口径；自建入站服务端面不动。
// 硬条件：严禁原样直出、严禁放宽 ValidSS2022SingleKey 一刀切。

func r4b2Endpoint(cipher, pw string) Endpoint {
	return Endpoint{Name: "x", Host: "203.0.113.10", Port: 8388, Direct: true,
		Proxy: Proxy{"type": "ss", "server": "203.0.113.10", "port": 8388,
			"cipher": cipher, "password": pw}}
}

// TestR4B2SingboxMultiSegmentReleased：多段合法节点在 sing-box 面
// 由略过改为逐段规范化后下发（混合编码段、2 段与 3 段、双 cipher）。
func TestR4B2SingboxMultiSegmentReleased(t *testing.T) {
	raw16, raw32 := r3b2RawKey(16), r3b2RawKey(32)
	std16 := base64.StdEncoding.EncodeToString(raw16)
	std32 := base64.StdEncoding.EncodeToString(raw32)
	for _, tc := range []struct {
		name, cipher, pw, want string
	}{
		{"multi2-mixed-enc-256", r3b2C256,
			std32 + ":" + base64.RawURLEncoding.EncodeToString(raw32), std32 + ":" + std32},
		{"multi2-std-url-256", r3b2C256,
			base64.URLEncoding.EncodeToString(raw32) + ":" + base64.RawStdEncoding.EncodeToString(raw32), std32 + ":" + std32},
		{"multi3-128", r3b2C128,
			std16 + ":" + base64.RawStdEncoding.EncodeToString(raw16) + ":" + base64.URLEncoding.EncodeToString(raw16),
			std16 + ":" + std16 + ":" + std16},
		{"multi2-already-std", r3b2C256, std32 + ":" + std32, std32 + ":" + std32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outs, ok := proxySingbox(r4b2Endpoint(tc.cipher, tc.pw))
			if !ok || len(outs) != 1 {
				t.Fatalf("多段合法节点须在 sing-box 面下发：ok=%v outs=%v", ok, outs)
			}
			if outs[0]["password"] != tc.want {
				t.Fatalf("须逐段归一为带填充 Std：got %q want %q", outs[0]["password"], tc.want)
			}
			if outs[0]["method"] != tc.cipher || outs[0]["type"] != "shadowsocks" {
				t.Fatalf("其余字段不变：%v", outs[0])
			}
		})
	}
}

// TestR4B2SingboxMultiBadStillSkipped：任一段不合规整节点略过——
// 存量坏 key 纵深兜底口径与单段完全一致，不得因解除多段而放行。
func TestR4B2SingboxMultiBadStillSkipped(t *testing.T) {
	raw32 := r3b2RawKey(32)
	std32 := base64.StdEncoding.EncodeToString(raw32)
	for _, tc := range []struct{ name, cipher, pw string }{
		{"multi-bad-second", r3b2C256, std32 + ":plainpassword"},
		{"multi-bad-first", r3b2C256, "plainpassword:" + std32},
		{"multi-empty-segment", r3b2C256, std32 + ":"},
		{"multi-wrong-len-segment", r3b2C256, std32 + ":" + base64.StdEncoding.EncodeToString(r3b2RawKey(16))},
		{"single-plaintext", r3b2C256, "plainpassword"},
		{"single-short", r3b2C128, base64.StdEncoding.EncodeToString(r3b2RawKey(8))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if outs, ok := proxySingbox(r4b2Endpoint(tc.cipher, tc.pw)); ok {
				t.Fatalf("不合规节点须整节点略过：outs=%v", outs)
			}
		})
	}
}

// TestR4B2SingboxUnchangedFaces：回归锁定——单段 / legacy 渲染与
// 解除前逐字节不变（单段经 forEndpoint + 本函数归一为 Std；legacy
// 原样；ValidSS2022SingleKey 一刀切实现不放宽，多段仍报 false，
// 其 false 不再被 sing-box 面用作略过据）。
func TestR4B2SingboxUnchangedFaces(t *testing.T) {
	raw32 := r3b2RawKey(32)
	std32 := base64.StdEncoding.EncodeToString(raw32)
	// 单段四形态 → 带填充 Std（与 GATE-F1 既有断言同值）。
	for _, f := range gateF1Forms {
		outs, ok := proxySingbox(r4b2Endpoint(r3b2C256, f.enc.EncodeToString(raw32)))
		if !ok || len(outs) != 1 || outs[0]["password"] != std32 {
			t.Fatalf("单段 %s 渲染须不变（Std）：ok=%v outs=%v", f.name, ok, outs)
		}
	}
	// legacy 明文原样。
	outs, ok := proxySingbox(r4b2Endpoint("aes-256-gcm", "plainpassword"))
	if !ok || outs[0]["password"] != "plainpassword" {
		t.Fatalf("legacy 渲染须不变：ok=%v outs=%v", ok, outs)
	}
	// 一刀切谓词不放宽：多段合法密码在 ValidSS2022SingleKey 下仍为否。
	multi := std32 + ":" + std32
	if ValidSS2022SingleKey(r3b2C256, multi) {
		t.Fatal("ValidSS2022SingleKey 不得放宽：多段须仍报 false")
	}
	if err := ValidateSS2022Password(r3b2C256, multi); err != nil {
		t.Fatalf("导入期逐段谓词须接受多段合法密码：%v", err)
	}
}

// TestR4B2SingboxChainAndCount：链式 sing-box 出站随同函数同步
// 解除、计数同口径；坏段链式仍失败。
func TestR4B2SingboxChainAndCount(t *testing.T) {
	raw32 := r3b2RawKey(32)
	std32 := base64.StdEncoding.EncodeToString(raw32)
	multi := std32 + ":" + base64.RawURLEncoding.EncodeToString(raw32)
	e := r4b2Endpoint(r3b2C256, multi)

	obs, err := singboxChainOutbounds(e, "via", 0, false)
	if err != nil || len(obs) != 1 || obs[0].(obj)["password"] != std32+":"+std32 {
		t.Fatalf("链式 sing-box 出站须同步解除并逐段归一：err=%v obs=%v", err, obs)
	}
	if n := SubscriptionNodeCount(FormatSingbox, []Endpoint{e}, SubOptions{}); n != 1 {
		t.Fatalf("计数须同口径计入多段节点：got %d want 1", n)
	}
	body, _, err := Subscription(FormatSingbox, []Endpoint{e}, SubOptions{})
	if err != nil || !strings.Contains(body, std32+":"+std32) {
		t.Fatalf("整份 sing-box 订阅须含逐段归一多段节点：err=%v body=%q", err, body)
	}

	bad := r4b2Endpoint(r3b2C256, std32+":plainpassword")
	if _, err := singboxChainOutbounds(bad, "via", 0, false); err == nil {
		t.Fatal("坏段节点链式须失败（整节点略过同口径）")
	}
	if n := SubscriptionNodeCount(FormatSingbox, []Endpoint{bad}, SubOptions{}); n != 0 {
		t.Fatalf("坏段节点计数须为 0：got %d", n)
	}
}

// TestR4B2BuiltinInboundUntouched：自建入站服务端面不动——自建 SS
// 入站（singboxOutbound）的多段存量密码维持既有口径：经单段 helper
// 原样渲染、绝不略过、也不逐段归一。
func TestR4B2BuiltinInboundUntouched(t *testing.T) {
	raw32 := r3b2RawKey(32)
	std32 := base64.StdEncoding.EncodeToString(raw32)
	multi := std32 + ":" + base64.RawURLEncoding.EncodeToString(raw32)
	e := Endpoint{Name: "b1", Host: "203.0.113.10", Port: 8388, Direct: true,
		Inbound: Inbound{ID: 9, Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: r3b2C256, Password: multi}}}
	o, ok := singboxOutbound(e)
	if !ok {
		t.Fatal("自建入站不得被略过")
	}
	if o["password"] != multi {
		t.Fatalf("自建入站面须维持原样口径：got %q want %q", o["password"], multi)
	}
}
