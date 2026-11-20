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
}

// R18-C2-02：downloadSpec 的 default 分支是死代码，一旦被复用即发出
// 单锚 checksumUrl（与本体同源的校验文件，一条线路即可投放自洽的
// 伪造（二进制、校验文件）对，退回旧审计 C2-02 已修复的弱锚形态）。
// 修法：default 分支 fail-closed——未配 Pinned 或 GitHubRepo 的
// Download 一律拒绝生成 spec，不再发出 checksumUrl。
// 回归钉：构造仅含 URL（+ChecksumURL）的 Download 调 downloadSpec；
// 旧代码回含 checksumUrl 的 spec、err=nil——必红；修后返回错误——变绿。
func TestR18C202DownloadSpecWithoutAnchorFailsClosed(t *testing.T) {
	for _, d := range []*Download{
		{URL: "https://example.com/x-{VERSION}.zip"},
		{URL: "https://example.com/x-{VERSION}.zip", ChecksumURL: "https://example.com/x-{VERSION}.zip.sha256"},
		{URL: "https://example.com/x-{VERSION}.zip", LatestURL: "https://example.com/x-latest.zip", LatestSum: "https://example.com/x-latest.zip.sha256"},
	} {
		spec, err := downloadSpec(d, "1.2.3", "")
		if err == nil {
			t.Fatalf("downloadSpec must fail closed for an unanchored Download, got spec=%v", spec)
		}
		if spec != nil {
			if _, weak := spec["checksumUrl"]; weak {
				t.Fatalf("unanchored Download must never yield a checksumUrl spec, got %v", spec)
			}
		}
	}
}
