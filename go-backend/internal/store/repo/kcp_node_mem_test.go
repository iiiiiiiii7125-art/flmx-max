package repo

import "testing"

// FIX-KCPMEM（P1-04）：节点内存声明口归一化与读面口径。
// 写入面归一为紧凑升序 JSON；读面复检同口径，脏 spec 整体作废。

func TestNormalizeKCPNodeMemMB(t *testing.T) {
	for in, want := range map[string]string{
		"":                 "",
		"  ":               "",
		`{"2":8192}`:       `{"2":8192}`,
		`{"10":512,"2":1}`: `{"2":1,"10":512}`,
	} {
		got, err := NormalizeKCPNodeMemMB(in)
		if err != nil || got != want {
			t.Fatalf("NormalizeKCPNodeMemMB(%q): got %q err %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		`not-json`, `[]`, `{"x":512}`, `{"0":512}`, `{"-1":512}`,
		`{"2":0}`, `{"2":-1}`, `{"2":1048577}`, `{"2":1.5}`, `{"2":"512"}`,
	} {
		if _, err := NormalizeKCPNodeMemMB(bad); err == nil {
			t.Fatalf("NormalizeKCPNodeMemMB(%q) must be rejected", bad)
		}
	}
}

func TestKCPNodeMemMB(t *testing.T) {
	if got := KCPNodeMemMB(`{"2":8192,"3":300}`, 2); got != 8192 {
		t.Fatalf("node 2: got %d", got)
	}
	if got := KCPNodeMemMB(`{"2":8192}`, 3); got != 0 {
		t.Fatalf("undeclared node: got %d", got)
	}
	// 脏 spec（绕过写入面直接落库的形态）整体作废，不许部分生效。
	for _, dirty := range []string{`{"2":8192`, `{"2":8192}junk`, `{"2":-1}`, `garbage`} {
		if got := KCPNodeMemMB(dirty, 2); got != 0 {
			t.Fatalf("dirty spec %q must yield 0, got %d", dirty, got)
		}
	}
	if got := KCPNodeMemMB("", 2); got != 0 {
		t.Fatalf("empty spec: got %d", got)
	}
}
