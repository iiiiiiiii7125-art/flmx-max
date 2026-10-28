package kernel

import (
	"strings"
	"testing"
)

// F-01 / F-02 后端修复回归（AUDIT-FINAL，manual-entry 修复轮后端 lane）。
// 先红后绿：本文件只钉后端 ParseImport / ValidateImportedProxy 口径，
// 不动 manual_node_import_test.go（测试 lane 的文件）。
//
// F-01：VLESS flow 须有 TLS 或真实 ENC。Vision+security=none
//（无 TLS 且无 ENC）须拒绝；flow+TLS（含 Reality）、flow+真实
// ENC 串须放行。只查 TLS/ENC 耦合，不查 network——Vision+WS/gRPC
// +TLS 在后端放行、由前端按 visionAllowed 设防，见测试内注释。
// K-01（1.7.1 发版后审计）：上述分工被推翻——前端只在手动表单
// 的 flow 选择框设防，粘贴/文件/订阅源三路进口无任何设防，
// Vision+WS/gRPC 与 Reality+WS/gRPC 经 ParseImport 落库并原样进
// 订阅。后端进口谓词自此补 transport x security 耦合列
// （transportSecurityViolation），Vision+WS/gRPC 与 Reality+WS
// 在后端即拒（Reality+gRPC/XHTTP 按 capabilities 矩阵合法，不拒）；
// 下述两例自此由放行改为拒绝，旧分工不再钉死。
// F-02：带 reality-opts 的节点须有 SNI（vless/vmess 用 servername，
// trojan 用 sni），否则拒绝。

const manualFixPBK = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 43 个 A = 32 字节 RawURL base64

const manualFixENC = "mlkem768x25519plus.xorpub.1rtt.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestManualNodeFixF01FlowRequiresTLSOrENC(t *testing.T) {
	reject := map[string]string{
		// 审计复现组合中属于 TLS/ENC 耦合维的：Vision+security=none
		//（无 TLS 且 encryption 为 none），须在后端被拒。
		"vision-security-none": "vless://" + manualUUID +
			"@203.0.113.10:443?type=tcp&encryption=none&security=none&flow=xtls-rprx-vision#v",
		// K-01：Vision+WS/gRPC+TLS 在后端亦拒（transport x security
		// 耦合列）。旧分工（后端放行、前端设防）已被推翻，见文件头注。
		"vision-ws-tls": "vless://" + manualUUID +
			"@203.0.113.10:443?type=ws&path=%2Fws&host=example.com&encryption=none&security=tls&flow=xtls-rprx-vision#v",
		"vision-grpc-tls": "vless://" + manualUUID +
			"@203.0.113.10:443?type=grpc&serviceName=grpc&encryption=none&security=tls&flow=xtls-rprx-vision#v",
	}
	for name, link := range reject {
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("%s：须拒绝，got nodes=%d errs=%v", name, len(nodes), errs)
		}
		if !strings.Contains(errs[0], "VLESS flow") {
			t.Fatalf("%s：错误须点名 flow/TLS 耦合，got %q", name, errs[0])
		}
	}

	accept := map[string]string{
		"vision-tls-tcp": "vless://" + manualUUID +
			"@203.0.113.10:443?type=tcp&encryption=none&security=tls&flow=xtls-rprx-vision#v",
		"vision-reality": "vless://" + manualUUID +
			"@203.0.113.10:443?type=tcp&encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=" + manualFixPBK + "&flow=xtls-rprx-vision#v",
		"vision-enc-none": "vless://" + manualUUID +
			"@203.0.113.10:443?type=tcp&encryption=" + manualFixENC + "&security=none&flow=xtls-rprx-vision#v",
	}
	for name, link := range accept {
		nodes, errs := ParseImport(link)
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s：须放行，got nodes=%d errs=%v", name, len(nodes), errs)
		}
		if got := nodes[0].Proxy.str("flow"); got != "xtls-rprx-vision" {
			t.Fatalf("%s：flow 须原样落库，got %q", name, got)
		}
	}
}

func TestManualNodeFixF02RealityRequiresSNI(t *testing.T) {
	reject := map[string]string{
		"vless-reality-no-sni": "vless://" + manualUUID +
			"@203.0.113.10:443?type=tcp&encryption=none&security=reality&pbk=" + manualFixPBK + "#v",
		"trojan-reality-no-sni": "trojan://pw" +
			"@203.0.113.10:443?security=reality&pbk=" + manualFixPBK + "#t",
	}
	for name, link := range reject {
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("%s：须拒绝，got nodes=%d errs=%v", name, len(nodes), errs)
		}
		if !strings.Contains(errs[0], "Reality") || !strings.Contains(errs[0], "SNI") {
			t.Fatalf("%s：错误须点名 Reality SNI，got %q", name, errs[0])
		}
	}

	accept := map[string]string{
		"vless-reality-with-sni": "vless://" + manualUUID +
			"@203.0.113.10:443?type=tcp&encryption=none&security=reality&sni=www.microsoft.com&pbk=" + manualFixPBK + "#v",
		"trojan-reality-with-sni": "trojan://pw" +
			"@203.0.113.10:443?security=reality&sni=www.microsoft.com&pbk=" + manualFixPBK + "#t",
	}
	for name, link := range accept {
		nodes, errs := ParseImport(link)
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("%s：须放行，got nodes=%d errs=%v", name, len(nodes), errs)
		}
	}

	// 横切校验的字段名覆盖：vmess 存 servername、trojan 存 sni。
	// 直接调 ValidateImportedProxy（与备份恢复面共用的同一谓词）。
	vmessNoSNI := Proxy{"type": "vmess", "server": "1.2.3.4", "port": 443, "uuid": manualUUID, "tls": true,
		"reality-opts": map[string]interface{}{"public-key": manualFixPBK, "short-id": "ab12"}}
	if err := ValidateImportedProxy(vmessNoSNI); err == nil {
		t.Fatal("vmess reality 无 SNI 须拒绝")
	} else if !strings.Contains(err.Error(), "SNI") {
		t.Fatalf("vmess reality 无 SNI 错误须点名 SNI，got %q", err)
	}
	vmessWithSNI := Proxy{"type": "vmess", "server": "1.2.3.4", "port": 443, "uuid": manualUUID, "tls": true,
		"servername":   "www.microsoft.com",
		"reality-opts": map[string]interface{}{"public-key": manualFixPBK, "short-id": "ab12"}}
	if err := ValidateImportedProxy(vmessWithSNI); err != nil {
		t.Fatalf("vmess reality 有 servername 须放行，got %v", err)
	}
}
