package kernel

import (
	"strings"
	"testing"
)

// R10 批 4（R10X-013 / R10-C-01）：Surge/Loon VMess 行 encrypt-method 补搬回归。
// 口径（R10-X-verdicts §5 + R10-FIX-PLAN §1 批 4）：
//   - parseSurgeLine vmess 分支改读 kv encrypt-method（缺省才 auto）一行；
//   - 校验复用 finishProxy 既有 ValidVMessCipher 收口，转换层不另设校验；
//   - 关闭断言分开：带 encrypt-method 落逐字值 / 缺省仍落 auto / 垃圾值逐条被拒。

const r10b4UUID = "11111111-2222-3333-4444-555555555555"

func r10b4SurgeVMess(extra string) string {
	line := "s-vm = vmess, 1.2.3.4, 443, username=" + r10b4UUID
	if extra != "" {
		line += ", " + extra
	}
	return line
}

func TestR10B4SurgeVMessEncryptMethodCarried(t *testing.T) {
	for _, cipher := range []string{"aes-128-gcm", "chacha20-poly1305", "none", "zero", "auto"} {
		nodes, errs := ParseImport(r10b4SurgeVMess("encrypt-method=" + cipher))
		if len(errs) != 0 || len(nodes) != 1 {
			t.Fatalf("encrypt-method=%s：须入库 1 节点，got nodes=%v errs=%v", cipher, nodes, errs)
		}
		if got := nodes[0].Proxy.str("cipher"); got != cipher {
			t.Fatalf("encrypt-method=%s：cipher 须逐字落库，got %q want %q", cipher, got, cipher)
		}
	}
}

func TestR10B4SurgeVMessEncryptMethodDefaultAuto(t *testing.T) {
	nodes, errs := ParseImport(r10b4SurgeVMess(""))
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("缺省 encrypt-method：须入库 1 节点，got nodes=%v errs=%v", nodes, errs)
	}
	if got := nodes[0].Proxy.str("cipher"); got != "auto" {
		t.Fatalf("缺省 encrypt-method：cipher 须落 auto，got %q", got)
	}
	// 空值形态同缺省：firstNonEmptyString 落 auto。
	nodes, errs = ParseImport(r10b4SurgeVMess("encrypt-method="))
	if len(errs) != 0 || len(nodes) != 1 {
		t.Fatalf("空 encrypt-method：须入库 1 节点，got nodes=%v errs=%v", nodes, errs)
	}
	if got := nodes[0].Proxy.str("cipher"); got != "auto" {
		t.Fatalf("空 encrypt-method：cipher 须落 auto，got %q", got)
	}
}

func TestR10B4SurgeVMessEncryptMethodGarbageRejected(t *testing.T) {
	healthy := "healthy = ss, 203.0.113.99, 8388, encrypt-method=aes-128-gcm, password=pw"
	for _, bad := range []string{"garbage-cipher", "aes-256-gcm", "aes-128-cfb"} {
		doc := r10b4SurgeVMess("encrypt-method="+bad) + "\n" + healthy
		nodes, errs := ParseImport(doc)
		if len(nodes) != 1 || nodes[0].Name != "healthy" {
			t.Fatalf("encrypt-method=%s：坏节点须逐条拒绝、健康节点不连坐，got nodes=%v errs=%v", bad, nodes, errs)
		}
		if len(errs) != 1 || !strings.Contains(errs[0], "VMess 加密方式 cipher 不受支持") {
			t.Fatalf("encrypt-method=%s：须经 finishProxy ValidVMessCipher 收口点名拒绝，got errs=%v", bad, errs)
		}
	}
}
