package kernel

import (
	"strings"
	"testing"
)

// FIX-B6 / C2-01 回归：版本校验必须活在 kernel 包内（单点），不能只靠
// handler 的 normalizeKernelVersion 与 agent 的 safeVersionPattern 两道
// 包外防线。基线实测（C2 探针）：ResolveDownload(xray, "25.1.1/../../evil")
// 拼出穿越 URL 且 err=nil——以下用例在基线上为红。
func TestFixB6KernelVersionValidatedInPackage(t *testing.T) {
	xray, _ := Lookup(TypeXray)
	bad := []string{
		"25.1.1/../../evil",     // path traversal out of the version segment
		"1.2.3?x=1",             // query injection into the URL template
		"a b",                   // whitespace
		"1..2",                  // ".." substring (the bare charset pattern admits it)
		"-1.2.3",                // must start alphanumeric
		"1.2.3/extra",           // slash
		strings.Repeat("1", 41), // over the 40-char bound
	}
	for _, v := range bad {
		if got, err := ResolveVersion(xray, v); err == nil {
			t.Fatalf("ResolveVersion(%q) = %q, nil error; want in-package rejection", v, got)
		}
		if _, err := ResolveDownload(xray, v, ""); err == nil {
			t.Fatalf("ResolveDownload(%q) succeeded; want in-package rejection", v)
		}
	}
	// downloadSpec is the funnel for ExtraDownload too: a bad version
	// must be refused even when ResolveVersion is bypassed entirely.
	sb, _ := Lookup(TypeSingbox)
	if _, err := downloadSpec(sb.Download, "1.2.3/../../evil", ""); err == nil {
		t.Fatal("downloadSpec accepted a traversal version (ExtraDownload path unguarded)")
	}
	// Legitimate shapes keep resolving exactly as before.
	for _, v := range []string{"25.1.1", "v25.1.1", "26.10.31"} {
		got, err := ResolveVersion(xray, v)
		if err != nil || got != strings.TrimPrefix(v, "v") {
			t.Fatalf("ResolveVersion(%q) = %q, %v; want the version itself", v, got, err)
		}
		if _, err := ResolveDownload(xray, v, ""); err != nil {
			t.Fatalf("ResolveDownload(%q): %v", v, err)
		}
	}
	if got, err := ResolveVersion(xray, ""); err != nil || got != "latest" {
		t.Fatalf("ResolveVersion(\"\") = %q, %v; want latest", got, err)
	}
	if got, err := ResolveVersion(xray, "latest"); err != nil || got != "latest" {
		t.Fatalf("ResolveVersion(latest) = %q, %v", got, err)
	}
}
