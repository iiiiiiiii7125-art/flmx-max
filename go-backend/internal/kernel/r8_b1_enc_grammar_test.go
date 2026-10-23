package kernel

import (
	"strings"
	"testing"
)

// R8 批 1（R8X-002）：ValidVLESSEncryption 文法段数下限放宽至 4 段。
// 样本逐值取自 Xray 官方 `vlessenc`（25.9.5）在本批施工前置硬门复测的
// 默认输出（无填充链 4 段串），原始日志
// audit-out-132/r8/R8-FIX-B1-version-bisect.log（VLESSENC_DEFAULT 行）。
// 既有样本集教训：TestR5B1EncryptionGrammarAntiOverfit 从未覆盖零填充
// 形态，本文件专补零填充格，并以 4 段垃圾格锁死防过修。

const r8b1UUID = "11111111-2222-3333-4444-555555555555"

// xray vlessenc 25.9.5 默认输出（x25519 认证，4 段、无填充链）。
const r8b1Enc4X25519 = "mlkem768x25519plus.native.0rtt.nxXrBunl3L3TglUpHVbQ3QuLltBtwDVfmCWBTurY-Ag"

// xray vlessenc 25.9.5 默认输出（mlkem768 认证，4 段、无填充链）。
const r8b1Enc4MLKEM = "mlkem768x25519plus.native.0rtt.HJvEMnLNjpEbJuR8jHI7lIg-1gBnIwZhJ8EcBPtWmuAmGxpvvJaFOukJUHEsIQLKr7TCaLoJanRIZmEXbfMteEw-wMHKLrRwmjiSnYIcH-pHXEtz42wo2coL5HaYSomF0XZ1dcaF5celGPMVSqiUHzQHIyibJ7eNdDUVOVdxHdoC33JodGKVZRWFLKGSr2Z4ayu4H3awJNBToCaLV_F9E0GwPoOWqNW4xvhK64iTKndu-2lCcQaUayCy34OzJDHLyIYXSGQ3T2UGwaiea1RsOBVvU_DNnOy_yCKgk8uuWlC0imuof1m40NN2M_mZWHhTxgSOaNBcahO_ckltrGTMw4AThQS2eLQtqSlKCGBu0uiM92tGnGlkoPBfabEAn2gbIptLyUXCg3BoYUeL8qzL-6kYAINNVdMDsphhZHpXKjmBtACRhkOoAoGkXnk2s3B0TsQxVCaHiSh1mfyiF0OKqSi4pQxe3OOp95kMnwYgLwM1oFYFK1A2daM7aMWzJ_aKTeBJMsJEerFnH_Mm7nkzrcSMX_KV9hpFwxxgSBVud5EBcnGZmtEINUEGcrAyLJutRtKIM6icuMq6sGBzQAQx_8eQPPg2imUmQrZd4XRFjyhDoySoZJTPLZZNGDOJVoTCNanF3Amsgqm43fd7iYKniLa8Y2lZwphVxZELCBF-yCqaFxeDX2HDWSDF9IpeJTY0v1Ky6NZNiiTNYvlt62q5b1Jl8rEb5_C5wDRR0mahLvnF9odbomqNjHyrB4xY9dQFqsSlQPwP8vuxHmgeo0i7EBerstBwnAxnO_lx2ue09oZnGxS7c9V82MxqyJMPuzxGdjkit1KMb9El_CQa8PaaTzY2NPkNiypD4DxfDoE8n_UG6QBb43LJyRy1GqqOTzR10_Il1lyzKWGO71xZdnaJqcJhwZdYKQCAL2oFJ9kJTcib8gydZjHFNMjJ8tYwGKM5TAEIBUYdfBqc_cFvFWq57bF6v3wDzJLErptWKlJ2rTsiaYahcReJsfqDaza08eSYQuI1xfKRKUhzn3NipqUJnkASZZiqjbyl96et0vJobaOMpmcsPzRIQVMWm_qio3WeXaEsM5SCZga01jKFbTANgcRLcTu3ykkqG2BBD2JGKKxrzPtIjXi7cRpKGHk2zFGIE7cKc_Vo7ZxNwGZ9cKmTdhlMLqOYzKCz00aWt2SyZGq5NKauvvqBQSVn_Ysvp_psO5CYFjBiJorO1oDAvTkjuRk0Ldc3tzy68uEMCLBSeVVhqRsM3BF0-ZBqXltXcYwRXbQHhkFDBSKEcTR51YASipFwRGlXcRFCqXG9jnIgzUBKQoUbQaRVW4cuCzUKbDuvuLZGMadpy3ig_3EeOpeUOHBt5wiHbvcUNzm-YEcs-cN9LHYEspB5kseOaXYlIjUIVFAJOhwwqekmW8muAryP4CkJpSaTPXtJ_ZAo8TUUjipn8ylH1JvE73dD3mW8eDgNInKR0GN4yrXAHnBuEtpPefEOakUtxSdkzgsy2lI6lkt5yIlqIviVmQoSM7LF-DB3gGxJhwHhEpFA27zhmn42cBC1HeMg0Sdi_-q4WUZ52fw"

func TestR8B1ENC4PartOfficialDefaultsAccepted(t *testing.T) {
	for name, enc := range map[string]string{
		"x25519":   r8b1Enc4X25519,
		"mlkem768": r8b1Enc4MLKEM,
	} {
		if n := len(strings.Split(enc, ".")); n != 4 {
			t.Fatalf("%s：样本须为 4 段官方默认形态，got %d 段", name, n)
		}
		if !ValidVLESSEncryption(enc) {
			t.Fatalf("%s：官方 vlessenc 默认 4 段串须合法：%s", name, enc)
		}
		p := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443,
			"uuid": r8b1UUID, "encryption": enc}
		if !validImportedProxyFields(p, ssFaceSingbox) || !validImportedProxyFields(p, ssFaceClash) {
			t.Fatalf("%s：双面 validImportedProxyFields 须放行 4 段串", name)
		}
		e := Endpoint{Name: "r8b1", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: p}
		if _, ok := proxyClash(e); !ok {
			t.Fatalf("%s：Clash 渲染面须保留 4 段 ENC 节点", name)
		}
		if _, err := chainTargetFromProxy(e); err != nil {
			t.Fatalf("%s：链式面须放行 4 段串：%v", name, err)
		}
		doc := "proxies:\n  - {name: ok, type: vless, server: 1.2.3.4, port: 443, uuid: " + r8b1UUID + ", encryption: '" + enc + "', udp: true}\n"
		if nodes, errs := ParseImport(doc); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s：Clash 进口须入库：nodes=%v errs=%v", name, nodes, errs)
		}
		uri := "vless://" + r8b1UUID + "@1.2.3.4:443?encryption=" + enc + "&type=tcp#ok"
		if nodes, errs := ParseImport(uri); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s：URI 进口须入库：nodes=%v errs=%v", name, nodes, errs)
		}
	}
	// 填充形对照（面板自产形态）不回归。
	pub32 := "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	padded := "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333." + pub32
	if !ValidVLESSEncryption(padded) {
		t.Fatal("填充形合法串不得回归被拒")
	}
}

// 防过修对照：4 段化后垃圾格仍须逐格被拒（坏 mode / 坏 rtt / 服务端形 /
// 坏填充 / 坏认证段，既有格与 4 段新格并列）。
func TestR8B1ENCGrammarGarbageStillRejected(t *testing.T) {
	pub32 := "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	pad := "100-111-1111.75-0-111.50-0-3333"
	bad := []string{
		"mlkem768x25519plus.garbage.0rtt." + pub32,        // 4 段坏 mode
		"mlkem768x25519plus.native.2rtt." + pub32,         // 4 段坏 rtt
		"mlkem768x25519plus.native.600s." + pub32,         // 4 段服务端形（ticket 占 rtt 位）
		"mlkem768x25519plus.native.0rtt.AAAA",             // 4 段坏认证段（长度）
		"mlkem768x25519plus.native.0rtt.!!!",              // 4 段坏认证段（非 base64）
		"mlkem768x25519plus.native.0rtt",                  // 3 段缺认证段
		"mlkem768x25519plus.native.0rtt.garbage." + pub32, // 5 段坏填充（既有格）
		"mlkem768x25519plus.native.0rtt." + pad + ".AAAA", // 既有格：坏认证段长度
		"mlkem768x25519plus.native.600s." + pad + "." + pub32, // 既有格：服务端形 ticket 段
		Settings{EncryptionAuth: EncryptionAuthX25519, EncryptionSecret: mustSecret(t)}.DecryptionString(), // 服务端形整串
	}
	for _, enc := range bad {
		if ValidVLESSEncryption(enc) {
			t.Fatalf("谓词层：encryption=%q 不得报合法", enc)
		}
		doc := "proxies:\n  - {name: bad, type: vless, server: 1.2.3.4, port: 443, uuid: " + r8b1UUID + ", encryption: '" + enc + "', udp: true}\n"
		if nodes, errs := ParseImport(doc); len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("encryption=%q 须进口拒绝：nodes=%v errs=%v", enc, nodes, errs)
		}
	}
}
