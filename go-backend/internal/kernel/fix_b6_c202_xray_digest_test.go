package kernel

import (
	"testing"
)

// FIX-B6 / C2-02 回归：Xray 二进制的完整性锚从「与本体同源的 .dgst
// 校验文件」升级为 GitHub 发布 API 的资产 digest（与 sing-box 同型，
// agent resolveGitHub 取 digest 并跨线路复核；API 不给 digest 时
// fail-closed 拒装）。基线：xray spec 只有 checksumUrl、无 github——红。
//
// 普查口径：catalog 里每个带下载面的内核，发出的 spec 必须带强锚
// （sha256 钉死或 github API digest），且不得再出现仅靠同源校验
// 文件的 checksumUrl 锚。
func TestFixB6XrayUsesGitHubDigestAnchor(t *testing.T) {
	xray, _ := Lookup(TypeXray)
	d, err := ResolveDownload(xray, "25.1.1", "")
	if err != nil {
		t.Fatal(err)
	}
	gh, ok := d["github"].(map[string]interface{})
	if !ok {
		t.Fatalf("xray spec has no github digest anchor: %v", d)
	}
	if gh["repo"] != "XTLS/Xray-core" || gh["asset"] != "Xray-linux-{ARCH}.zip" || gh["tag"] != "v25.1.1" {
		t.Fatalf("xray github anchor = %v", gh)
	}
	if _, weak := d["checksumUrl"]; weak {
		t.Fatalf("xray spec still carries the co-located checksum-file anchor: %v", d)
	}
	latest, err := ResolveDownload(xray, "latest", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := latest["github"].(map[string]interface{}); !ok {
		t.Fatalf("xray latest spec must defer to the agent's GitHub resolution: %v", latest)
	}
	if _, weak := latest["checksumUrl"]; weak {
		t.Fatalf("xray latest spec still carries the checksum-file anchor: %v", latest)
	}
}

func TestFixB6EveryKernelSpecHasStrongAnchor(t *testing.T) {
	for _, info := range Catalog() {
		if info.Download == nil {
			continue
		}
		spec, err := ResolveDownload(info, "latest", "")
		if err != nil {
			t.Fatalf("%s: %v", info.Type, err)
		}
		_, pinned := spec["sha256"]
		_, gh := spec["github"]
		if !pinned && !gh {
			t.Fatalf("%s spec has no strong anchor (pinned sha256 or GitHub digest): %v", info.Type, spec)
		}
		if _, weak := spec["checksumUrl"]; weak {
			t.Fatalf("%s spec relies on a co-located checksum file: %v", info.Type, spec)
		}
	}
	st, err := ExtraDownload(ShadowTLSDownload(), ShadowTLSVersion, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st["sha256"]; !ok {
		t.Fatalf("shadow-tls spec lost its pinned anchor: %v", st)
	}
}
