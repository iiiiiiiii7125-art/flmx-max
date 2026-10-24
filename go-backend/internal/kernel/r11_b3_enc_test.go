package kernel

import (
	"strings"
	"testing"
)

// R11 批 3（ENC 面两案，断言分开登记、不得互替）：
//
//   - R11X-004（P2）：proxyClash 下发 encryption 时取 p.str("encryption")
//     修剪形（与 URI / 链式两面同口径），落库原值不动。关闭格
//     F13 / S1 / S2 三格 Clash 面下发为修剪形、合法无空白 ENC 串
//     下发逐字不变、URI / 链式 / sing-box 三面行为不变。
//   - R11X-007（P3）：ValidVLESSEncryption 填充组解析改为组内非首位
//     空 token 忽略后计数与校验；组首空 token 与全空组仍拒；其余
//     口径（元素数 {3,4,5}、首组前三数、token 含符号长度 ≤11）
//     逐字不动。关闭格 = M5 的 99 格全表（主表 92 格取自
//     M5-samples/M5-cells.tsv + M5-threeparty.tsv、补格 S1–S7 取自
//     R11-M5-supp-panel.log / run_supp.py 逐字构造）复跑：
//     B17 / S3 / S4 / S5 转放行，定界格 B18 / S7 / B16 / S6 仍拒，
//     W-1 / W-2 / W-3 对齐格与官方 / 自产形全格不回归。
//     99 格表中 BinaryAccept 为 M5 双二进制实测正本（Xray 25.9.5
//     exit 0 且 mihomo v1.19.33 exit 0 为放行；F05/F06 的 Xray
//     panic exit 2 计拒）；F13 / S1 / S2 三格为 R11X-004 特殊格——
//     谓词直查拒原始带空白串、二进制拒原始串，但进口 / 存量面经
//     str() 修剪形放行（Pred=false、ImportStored=true），其 Clash
//     下发修剪形由 R11X-004 断言单独销账，不并入 R11X-007 的
//     三方一致计数口径（99 格中除此三格外，Pred 须与 BinaryAccept
//     逐格一致；此三格的修剪形本身在表中以 B03 等同形格计一致）。

func r11b3ClashEncryption(t *testing.T, e Endpoint) (string, bool) {
	t.Helper()
	node, ok := proxyClash(e)
	if !ok {
		return "", false
	}
	for _, f := range node {
		if f.Key == "encryption" {
			s, _ := f.Value.(string)
			return s, true
		}
	}
	return "", true // 节点在但无 encryption 键
}

// ---- R11X-004 ----

func TestR11B3X004ClashEmitsTrimmedEncryption(t *testing.T) {
	trimmed := "mlkem768x25519plus.native.0rtt.100-200-300." + r9b2Pub32
	cases := map[string]string{
		"F13-lead-space":  " " + trimmed,
		"S1-trail-space":  trimmed + " ",
		"S2-lead-tab":     "\t" + trimmed,
		"F13-trail-tab-x": trimmed + "\t",
	}
	for name, raw := range cases {
		// 进口路径（Clash 通道）：落库保留原始带空白值。
		nodes, errs := ParseImport(r9b2ClashVLESS(raw))
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s：进口须放行（修剪形校验）：nodes=%v errs=%v", name, nodes, errs)
		}
		storedRaw, _ := nodes[0].Proxy["encryption"].(string)
		if storedRaw != raw {
			t.Fatalf("%s：落库原值须不动（下发侧归一、不得改走落库归一）：stored=%q want raw %q", name, storedRaw, raw)
		}
		e := Endpoint{Name: nodes[0].Name, Host: nodes[0].Host, Port: nodes[0].Port, Direct: true, Proxy: nodes[0].Proxy}
		got, ok := r11b3ClashEncryption(t, e)
		if !ok {
			t.Fatalf("%s：Clash 面须保留节点", name)
		}
		if got != trimmed {
			t.Fatalf("%s：Clash 面下发须为修剪形：got %q want %q", name, got, trimmed)
		}
		// 直构存量路径同格：Proxy 原值带空白、proxyClash 下发修剪形。
		direct := Endpoint{Name: "stock", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: Proxy{
			"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": raw,
		}}
		got2, ok2 := r11b3ClashEncryption(t, direct)
		if !ok2 || got2 != trimmed {
			t.Fatalf("%s：存量直构 Clash 下发须为修剪形：ok=%v got %q want %q", name, ok2, got2, trimmed)
		}
		if direct.Proxy.str("encryption") != trimmed {
			t.Fatalf("%s：str() 口径自查失败", name)
		}
		// 原 Proxy map 的存储值不得被 proxyClash 就地改写（克隆面归一）。
		if rawAfter, _ := direct.Proxy["encryption"].(string); rawAfter != raw {
			t.Fatalf("%s：proxyClash 不得就地改写存储原值：after=%q want raw %q", name, rawAfter, raw)
		}
	}
}

func TestR11B3X004CleanEncryptionAndOtherFacesUnchanged(t *testing.T) {
	clean := "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333." + r9b2Pub32
	e := Endpoint{Name: "clean", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": clean, "network": "tcp",
	}}
	// 合法无空白 ENC 串：Clash 下发逐字不变。
	got, ok := r11b3ClashEncryption(t, e)
	if !ok || got != clean {
		t.Fatalf("合法 ENC 的 Clash 下发须逐字不变：ok=%v got %q want %q", ok, got, clean)
	}
	// URI 面行为不变：下发修剪形（本格即原串）且逐字含之。
	uri, uok := proxyURI(e)
	if !uok || !strings.Contains(uri, "encryption="+clean) {
		t.Fatalf("合法 ENC 的 URI 面须不变：ok=%v uri=%q", uok, uri)
	}
	// 链式面行为不变：chainTargetFromProxy 成功且 Encryption 为原串。
	ct, err := chainTargetFromProxy(e)
	if err != nil || ct.Encryption != clean {
		t.Fatalf("合法 ENC 的链式面须不变：err=%v ct.Encryption=%q", err, ct.Encryption)
	}
	// sing-box 面行为不变：ENC 节点恒略过（N-09 设计）。
	if _, ok := proxySingbox(e); ok {
		t.Fatal("合法 ENC 节点 sing-box 面须恒略过（行为不变）")
	}
	// 带空白格的三面既有行为不变（以 F13 为代表逐面复核）：
	raw := " " + "mlkem768x25519plus.native.0rtt.100-200-300." + r9b2Pub32
	trimmed := strings.TrimSpace(raw)
	we := Endpoint{Name: "ws13", Host: "1.2.3.4", Port: 443, Direct: true, Proxy: Proxy{
		"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": raw, "network": "tcp",
	}}
	uri2, uok2 := proxyURI(we)
	if !uok2 || !strings.Contains(uri2, "encryption="+trimmed) || strings.Contains(uri2, "%20mlkem") {
		t.Fatalf("F13 URI 面须维持修剪形下发：ok=%v uri=%q", uok2, uri2)
	}
	ct2, err2 := chainTargetFromProxy(we)
	if err2 != nil || ct2.Encryption != trimmed {
		t.Fatalf("F13 链式面须维持修剪形：err=%v ct.Encryption=%q want %q", err2, ct2.Encryption, trimmed)
	}
	if _, ok := proxySingbox(we); ok {
		t.Fatal("F13 sing-box 面须维持略过（行为不变）")
	}
}

// ---- R11X-007 定界与转放行格（先于全表单独点名） ----

func TestR11B3X007EmptyTokenGroupsAccepted(t *testing.T) {
	for name, pad := range map[string]string{
		"B17-trail-dash-first": "100-200-300-",
		"S3-double-dash":       "100-200-300--400",
		"S4-trail-dash-later":  "100-200-300.100-200-300-",
		"S5-trail-dash-5":      "100-200-300-400-",
	} {
		enc := r9b2EncWithPad(pad)
		if !ValidVLESSEncryption(enc) {
			t.Fatalf("%s：谓词须转放行 pad=%q", name, pad)
		}
		if nodes, errs := ParseImport(r9b2ClashVLESS(enc)); len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s：进口须转放行 pad=%q：nodes=%v errs=%v", name, pad, nodes, errs)
		}
		p := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": enc}
		if !validImportedProxyFields(p, ssFaceClash) {
			t.Fatalf("%s：存量谓词须转放行 pad=%q", name, pad)
		}
	}
}

func TestR11B3X007BoundariesStillRejected(t *testing.T) {
	for name, pad := range map[string]string{
		"B18-lead-dash":      "-100-200-300",
		"S7-dash-only-group": "100-200-300.-",
		"B16-empty-group":    "100-200-300..100-200-300",
		"S6-group-space":     "100-200-300 ",
	} {
		enc := r9b2EncWithPad(pad)
		if ValidVLESSEncryption(enc) {
			t.Fatalf("%s：定界格须仍拒 pad=%q", name, pad)
		}
		if nodes, errs := ParseImport(r9b2ClashVLESS(enc)); len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("%s：定界格进口须仍拒 pad=%q：nodes=%v errs=%v", name, pad, nodes, errs)
		}
		p := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": enc}
		if validImportedProxyFields(p, ssFaceClash) {
			t.Fatalf("%s：定界格存量谓词须仍拒 pad=%q", name, pad)
		}
	}
	// 全空组与组首空的其它形态亦不得被连带放宽。
	for name, pad := range map[string]string{
		"dash-only-solo":   "-",
		"double-dash-solo": "--",
		"lead-double-dash": "--100-200-300",
		"trail-only-two":   "100-200-",
	} {
		if ValidVLESSEncryption(r9b2EncWithPad(pad)) {
			t.Fatalf("%s：须仍拒 pad=%q", name, pad)
		}
	}
}

// ---- R11X-007 99 格全表 ----

type r11b3Cell struct {
	ID, Enc                          string
	Pred, ImportStored, BinaryAccept bool
}

var r11b3Cells99 = []r11b3Cell{
	{ID: "A01", Enc: "mlkem768x25519plus.native.0rtt.nxXrBunl3L3TglUpHVbQ3QuLltBtwDVfmCWBTurY-Ag", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "A02", Enc: "mlkem768x25519plus.native.0rtt.HJvEMnLNjpEbJuR8jHI7lIg-1gBnIwZhJ8EcBPtWmuAmGxpvvJaFOukJUHEsIQLKr7TCaLoJanRIZmEXbfMteEw-wMHKLrRwmjiSnYIcH-pHXEtz42wo2coL5HaYSomF0XZ1dcaF5celGPMVSqiUHzQHIyibJ7eNdDUVOVdxHdoC33JodGKVZRWFLKGSr2Z4ayu4H3awJNBToCaLV_F9E0GwPoOWqNW4xvhK64iTKndu-2lCcQaUayCy34OzJDHLyIYXSGQ3T2UGwaiea1RsOBVvU_DNnOy_yCKgk8uuWlC0imuof1m40NN2M_mZWHhTxgSOaNBcahO_ckltrGTMw4AThQS2eLQtqSlKCGBu0uiM92tGnGlkoPBfabEAn2gbIptLyUXCg3BoYUeL8qzL-6kYAINNVdMDsphhZHpXKjmBtACRhkOoAoGkXnk2s3B0TsQxVCaHiSh1mfyiF0OKqSi4pQxe3OOp95kMnwYgLwM1oFYFK1A2daM7aMWzJ_aKTeBJMsJEerFnH_Mm7nkzrcSMX_KV9hpFwxxgSBVud5EBcnGZmtEINUEGcrAyLJutRtKIM6icuMq6sGBzQAQx_8eQPPg2imUmQrZd4XRFjyhDoySoZJTPLZZNGDOJVoTCNanF3Amsgqm43fd7iYKniLa8Y2lZwphVxZELCBF-yCqaFxeDX2HDWSDF9IpeJTY0v1Ky6NZNiiTNYvlt62q5b1Jl8rEb5_C5wDRR0mahLvnF9odbomqNjHyrB4xY9dQFqsSlQPwP8vuxHmgeo0i7EBerstBwnAxnO_lx2ue09oZnGxS7c9V82MxqyJMPuzxGdjkit1KMb9El_CQa8PaaTzY2NPkNiypD4DxfDoE8n_UG6QBb43LJyRy1GqqOTzR10_Il1lyzKWGO71xZdnaJqcJhwZdYKQCAL2oFJ9kJTcib8gydZjHFNMjJ8tYwGKM5TAEIBUYdfBqc_cFvFWq57bF6v3wDzJLErptWKlJ2rTsiaYahcReJsfqDaza08eSYQuI1xfKRKUhzn3NipqUJnkASZZiqjbyl96et0vJobaOMpmcsPzRIQVMWm_qio3WeXaEsM5SCZga01jKFbTANgcRLcTu3ykkqG2BBD2JGKKxrzPtIjXi7cRpKGHk2zFGIE7cKc_Vo7ZxNwGZ9cKmTdhlMLqOYzKCz00aWt2SyZGq5NKauvvqBQSVn_Ysvp_psO5CYFjBiJorO1oDAvTkjuRk0Ldc3tzy68uEMCLBSeVVhqRsM3BF0-ZBqXltXcYwRXbQHhkFDBSKEcTR51YASipFwRGlXcRFCqXG9jnIgzUBKQoUbQaRVW4cuCzUKbDuvuLZGMadpy3ig_3EeOpeUOHBt5wiHbvcUNzm-YEcs-cN9LHYEspB5kseOaXYlIjUIVFAJOhwwqekmW8muAryP4CkJpSaTPXtJ_ZAo8TUUjipn8ylH1JvE73dD3mW8eDgNInKR0GN4yrXAHnBuEtpPefEOakUtxSdkzgsy2lI6lkt5yIlqIviVmQoSM7LF-DB3gGxJhwHhEpFA27zhmn42cBC1HeMg0Sdi_-q4WUZ52fw", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "A03", Enc: "mlkem768x25519plus.native.0rtt.XCfWEpd_ZpwCrCULi7CwP1YN0dOxtMcNPGNDnDNjRV4", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "A04", Enc: "mlkem768x25519plus.native.0rtt.YqJQeHsbMTIciioG8il3jHyvcjxtQcuAdjNYYibEBZhkJ8QSjsC7ylApVmC-dyMDQ7ptRoRjsfRdMoLG0ooI44ECCfMbowUTEpPOYPNXPaxWDGZKYbxHDXBDuZU3Q7QbiRlWHxS63hKV-RYgjPsigYcAXzA6syCEeGVYykTLGollQkYdN2VYIRcQgguv5tN12uxuV2MKGoM2vQc20qZILcpX1vDN4MCRezSg25NtXSDJmQzQgLuxyoile6EOu3RbqGEzuXTAhIFKvgCRVJxCWcFZodAVSuA4HKoWP5ROOfuPYRVtNUMV6AWrMPcD8uGAhtAZWtCpstcCPxl5DCBA9slD4NCoUtYVigzDi1FnSrELTLZShQec-hea16oFe1GeaFCQ06y98YW09CUJ2REUiqqCtrKn-nYFNklCHKbLeDlb2WeLR-FFodyuZTRlBRpIIBFgY7Zzn2jNhNZ2quTNw4RdQxrERFFNGkGHT7Ek8ESGFPFMh5B7aeLFb7VTsUkiKPlG0rEEELkiegCtkOB5_yCF2NJOdMYDEKFBuECauPvD2FhUaYiNwsVEL8do11Js-5EgZLW2N1c3cQWRbfFVasZg6TVkxPMBuSNQPSHMG2bHpKCdg5JRhkAIqdu0Ylu2oxsrYDoPliZuScFVlIuJkvONinW2s6u5AzqrFRxoK_ad9gCAp2Ro0btUYOARBNgSw2Brb3yi4CF-LtYK1pBUXykjM2hyZzjELNV_79HOycs3WSAiL8QwWFcT_oGvexulJPNSuTBWvda06hizh-cA5WN0QLWHYasuI-UfJ-XAbCF8h9nAuAg6R8BmREpwQPHOeDtF_8UW-JFe0wMfLKpYbTIa2HRrBFLFXwMhWstieIIu13ARXVMa3ggECLBXzgg7UbCjNxAsAwXOiZqm3UN_HIuae8EcHyRuVsZeV1uJPgEAMnlmoeO8OQQhtFUcBIt8vOuxN1LEFdbDCKyFf_utgvFXYCdFlXcNkiC8HhCOI4FRiPEZ0Cmh4pAt8TcbMhi_rWrEzig29VK_3jXMEtkEQgVqgXBCCCh1jIUlNTgWOVGIWdw_WqQ619u-CuizzVhq9wR-DNAxqUG2Aim9TBoFbbpr8pE24Zqa6DON41xYpIEyWlkTjaKb4ncSYuOMGGTOklrKkymAzoOU2sk72WVmD6JTq2qVDtRt0tTPStJAl3Mu9rYBeoM-HiO4szFv8vOVNewfadGm6Jdcric3YMB3R5ywSkgTPfhH5dURuxo-ejCKQDu6pdNeuMQOPTl2eEgmZIBDRchIDEhQF-xp4CCWXiIvkXczCON091CoOgAfjWrJOpuEzZIeVdZ8G1emt_EC1segeFcCBGNj8RrAQ3cqDPmidadm0YqkhnhGoQxmC7pLejqVmgl25weWcIc5hwQ6s8g_eLcUn5nP_wBCFjF1K_pferU4v_SYAJOEo-BMSfJ1a7i6aeNEQjZGtWabNHeoHGWEG0YgeNhZXJM4D2Mh1rSch7Sd32WKXQdO7-XHNTPLIooWA9VxGqNg1rR6jQx8sa0eVV1MKuRihTQdUdZ883Mi9nHjDgWkN0W0__73Uds", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "A05", Enc: "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "A06", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.HJvEMnLNjpEbJuR8jHI7lIg-1gBnIwZhJ8EcBPtWmuAmGxpvvJaFOukJUHEsIQLKr7TCaLoJanRIZmEXbfMteEw-wMHKLrRwmjiSnYIcH-pHXEtz42wo2coL5HaYSomF0XZ1dcaF5celGPMVSqiUHzQHIyibJ7eNdDUVOVdxHdoC33JodGKVZRWFLKGSr2Z4ayu4H3awJNBToCaLV_F9E0GwPoOWqNW4xvhK64iTKndu-2lCcQaUayCy34OzJDHLyIYXSGQ3T2UGwaiea1RsOBVvU_DNnOy_yCKgk8uuWlC0imuof1m40NN2M_mZWHhTxgSOaNBcahO_ckltrGTMw4AThQS2eLQtqSlKCGBu0uiM92tGnGlkoPBfabEAn2gbIptLyUXCg3BoYUeL8qzL-6kYAINNVdMDsphhZHpXKjmBtACRhkOoAoGkXnk2s3B0TsQxVCaHiSh1mfyiF0OKqSi4pQxe3OOp95kMnwYgLwM1oFYFK1A2daM7aMWzJ_aKTeBJMsJEerFnH_Mm7nkzrcSMX_KV9hpFwxxgSBVud5EBcnGZmtEINUEGcrAyLJutRtKIM6icuMq6sGBzQAQx_8eQPPg2imUmQrZd4XRFjyhDoySoZJTPLZZNGDOJVoTCNanF3Amsgqm43fd7iYKniLa8Y2lZwphVxZELCBF-yCqaFxeDX2HDWSDF9IpeJTY0v1Ky6NZNiiTNYvlt62q5b1Jl8rEb5_C5wDRR0mahLvnF9odbomqNjHyrB4xY9dQFqsSlQPwP8vuxHmgeo0i7EBerstBwnAxnO_lx2ue09oZnGxS7c9V82MxqyJMPuzxGdjkit1KMb9El_CQa8PaaTzY2NPkNiypD4DxfDoE8n_UG6QBb43LJyRy1GqqOTzR10_Il1lyzKWGO71xZdnaJqcJhwZdYKQCAL2oFJ9kJTcib8gydZjHFNMjJ8tYwGKM5TAEIBUYdfBqc_cFvFWq57bF6v3wDzJLErptWKlJ2rTsiaYahcReJsfqDaza08eSYQuI1xfKRKUhzn3NipqUJnkASZZiqjbyl96et0vJobaOMpmcsPzRIQVMWm_qio3WeXaEsM5SCZga01jKFbTANgcRLcTu3ykkqG2BBD2JGKKxrzPtIjXi7cRpKGHk2zFGIE7cKc_Vo7ZxNwGZ9cKmTdhlMLqOYzKCz00aWt2SyZGq5NKauvvqBQSVn_Ysvp_psO5CYFjBiJorO1oDAvTkjuRk0Ldc3tzy68uEMCLBSeVVhqRsM3BF0-ZBqXltXcYwRXbQHhkFDBSKEcTR51YASipFwRGlXcRFCqXG9jnIgzUBKQoUbQaRVW4cuCzUKbDuvuLZGMadpy3ig_3EeOpeUOHBt5wiHbvcUNzm-YEcs-cN9LHYEspB5kseOaXYlIjUIVFAJOhwwqekmW8muAryP4CkJpSaTPXtJ_ZAo8TUUjipn8ylH1JvE73dD3mW8eDgNInKR0GN4yrXAHnBuEtpPefEOakUtxSdkzgsy2lI6lkt5yIlqIviVmQoSM7LF-DB3gGxJhwHhEpFA27zhmn42cBC1HeMg0Sdi_-q4WUZ52fw", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "A07", Enc: "mlkem768x25519plus.xorpub.0rtt.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "A08", Enc: "mlkem768x25519plus.random.1rtt.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "A09", Enc: "none", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "A10", Enc: "", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "B01", Enc: "mlkem768x25519plus.native.0rtt.100.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B02", Enc: "mlkem768x25519plus.native.0rtt.100-200.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B03", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "B04", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-400.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "B05", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-400-500.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "B06", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-400-500-600.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B07", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-400-500-600-700.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B08", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-400-500-600-700-800-900.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B09", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-400-500-600-700-800-900-1000-1100-1200.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B10", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.100-200-300-400.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "B11", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.100-200-300-400-500.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "B12", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.100-200.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B13", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.100-200-300-400-500-600.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B14", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.100.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B15", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "B16", Enc: "mlkem768x25519plus.native.0rtt.100-200-300..100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "B17", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "B18", Enc: "mlkem768x25519plus.native.0rtt.-100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "C01", Enc: "mlkem768x25519plus.native.0rtt.9999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "C02", Enc: "mlkem768x25519plus.native.0rtt.99999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "C03", Enc: "mlkem768x25519plus.native.0rtt.999999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "C04", Enc: "mlkem768x25519plus.native.0rtt.9999999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "C05", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.999999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "C06", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.99999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "C07", Enc: "mlkem768x25519plus.native.0rtt.100-999999999999-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "C08", Enc: "mlkem768x25519plus.native.0rtt.00000000100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "C09", Enc: "mlkem768x25519plus.native.0rtt.000000000100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "C10", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.00000000000-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "C11", Enc: "mlkem768x25519plus.native.0rtt.4294967295-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "C12", Enc: "mlkem768x25519plus.native.0rtt.4294967296-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D01", Enc: "mlkem768x25519plus.native.0rtt.0-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "D02", Enc: "mlkem768x25519plus.native.0rtt.100-0-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "D03", Enc: "mlkem768x25519plus.native.0rtt.100-200-0.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "D04", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-0-500.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D05", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-400-0.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D06", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-0.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D07", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-50.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D08", Enc: "mlkem768x25519plus.native.0rtt.100-0-300-400-500.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "D09", Enc: "mlkem768x25519plus.native.0rtt.100-200-0-400.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "D10", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.0-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D11", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.75-0-111.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D12", Enc: "mlkem768x25519plus.native.0rtt.99-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "D13", Enc: "mlkem768x25519plus.native.0rtt.99-200-300-400-500.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "D14", Enc: "mlkem768x25519plus.native.0rtt.99-200-300-400.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "D15", Enc: "mlkem768x25519plus.native.0rtt.100-50-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D16", Enc: "mlkem768x25519plus.native.0rtt.100-200-50.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D17", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.50-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "D18", Enc: "mlkem768x25519plus.native.0rtt.099-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "E01", Enc: "mlkem768x25519plus.native.0rtt.+100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "E02", Enc: "mlkem768x25519plus.native.0rtt.+9999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "E03", Enc: "mlkem768x25519plus.native.0rtt.+99999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "E04", Enc: "mlkem768x25519plus.native.0rtt.+999999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "E05", Enc: "mlkem768x25519plus.native.0rtt.+00000000100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "E06", Enc: "mlkem768x25519plus.native.0rtt.+0000000100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "E07", Enc: "mlkem768x25519plus.native.0rtt.+4294967296-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "E08", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.+99999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "E09", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.+9999999999-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "E10", Enc: "mlkem768x25519plus.native.0rtt.+99-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "E11", Enc: "mlkem768x25519plus.native.0rtt.100-+200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "E12", Enc: "mlkem768x25519plus.native.0rtt.100-200-+300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "E13", Enc: "mlkem768x25519plus.native.0rtt.++100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "E14", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.+200-300-400.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "F01", Enc: "mlkem768x25519plus.garbage.0rtt.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F02", Enc: "mlkem768x25519plus.native.2rtt.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F03", Enc: "mlkem768x25519plus.native.600s.MAb7BhvDpV2Uf77P_JMa3Y6YYHZ8Z53tdCU9R9YeqVw", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F04", Enc: "mlkem768x25519plus.native.600s.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F05", Enc: "mlkem768x25519plus.native.0rtt.AAAA", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F06", Enc: "mlkem768x25519plus.native.0rtt.!!!", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F07", Enc: "mlkem768x25519plus.native.0rtt", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F08", Enc: "mlkem768x25519plus.native.0rtt.garbage.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F09", Enc: "totally-bogus-enc", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F10", Enc: "MLKEM768X25519PLUS.NATIVE.0RTT.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F11", Enc: "mlkem768x25519plus.native.0rtt.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8A", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F12", Enc: "mlkem768x25519plus.native.0rtt.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F13", Enc: " mlkem768x25519plus.native.0rtt.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: true, BinaryAccept: false},
	{ID: "F14", Enc: "mlkem768x25519plus.native.0rtt.100-abc-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F15", Enc: "mlkem768x25519plus.native.0rtt.100_200_300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F16", Enc: "mlkem768x25519plus.native.0rtt.0x100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F17", Enc: "mlkem768x25519plus.native.0RTT.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F18", Enc: "mlkem768x25519plus.NATIVE.0rtt.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F19", Enc: "mlkem768x25519plus.native.0rtt.100.5-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "F20", Enc: "mlkem768x25519plus.native.0rtt.100 -200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "S1", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8 ", Pred: false, ImportStored: true, BinaryAccept: false},
	{ID: "S2", Enc: "\tmlkem768x25519plus.native.0rtt.100-200-300.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: true, BinaryAccept: false},
	{ID: "S3", Enc: "mlkem768x25519plus.native.0rtt.100-200-300--400.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "S4", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.100-200-300-.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "S5", Enc: "mlkem768x25519plus.native.0rtt.100-200-300-400-.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: true, ImportStored: true, BinaryAccept: true},
	{ID: "S6", Enc: "mlkem768x25519plus.native.0rtt.100-200-300 .AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
	{ID: "S7", Enc: "mlkem768x25519plus.native.0rtt.100-200-300.-.AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", Pred: false, ImportStored: false, BinaryAccept: false},
}

func TestR11B3X007Full99Table(t *testing.T) {
	if len(r11b3Cells99) != 99 {
		t.Fatalf("99 格全表条数须为 99，got %d", len(r11b3Cells99))
	}
	for _, c := range r11b3Cells99 {
		if got := ValidVLESSEncryption(c.Enc); got != c.Pred {
			t.Errorf("%s：谓词=%v want %v enc=%q", c.ID, got, c.Pred, c.Enc)
		}
		nodes, errs := ParseImport(r9b2ClashVLESS(c.Enc))
		imp := len(errs) == 0 && len(nodes) == 1
		if imp != c.ImportStored {
			t.Errorf("%s：进口=%v want %v enc=%q nodes=%v errs=%v", c.ID, imp, c.ImportStored, c.Enc, nodes, errs)
		}
		p := Proxy{"type": "vless", "server": "1.2.3.4", "port": 443, "uuid": r8b1UUID, "encryption": c.Enc}
		if got := validImportedProxyFields(p, ssFaceClash); got != c.ImportStored {
			t.Errorf("%s：存量谓词=%v want %v enc=%q", c.ID, got, c.ImportStored, c.Enc)
		}
		// 三方一致口径：除 F13/S1/S2（R11X-004 空白特殊格，谓词直查
		// 与二进制同拒原始串、进口/存量经修剪形放行）外，面板谓词
		// 须与双二进制实测正本逐格一致。
		if c.ID != "F13" && c.ID != "S1" && c.ID != "S2" && c.Pred != c.BinaryAccept {
			t.Errorf("%s：表内口径错误：Pred=%v 与 BinaryAccept=%v 不一致（非空白特殊格）", c.ID, c.Pred, c.BinaryAccept)
		}
		if (c.ID == "F13" || c.ID == "S1" || c.ID == "S2") && (c.Pred || c.BinaryAccept || !c.ImportStored) {
			t.Errorf("%s：空白特殊格口径错误：Pred=%v BinaryAccept=%v ImportStored=%v", c.ID, c.Pred, c.BinaryAccept, c.ImportStored)
		}
	}
}
