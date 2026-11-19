package kernel

import "testing"

// 发现 1：fork 构建与上游构建的区分——IsForkVersion 只认 "-flmx*"
// 后缀；裸底版（回滚用的上游构建）不是 fork 版。
func TestIsForkVersion(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"1.14.4-flmx1", true},
		{"v1.14.4-flmx1", true},
		{"1.14.4", false},
		{"1.14.4-rc1", false},
		{"1.14.5-flmx1", true},
		{"latest", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsForkVersion(c.version); got != c.want {
			t.Errorf("IsForkVersion(%q) = %v, want %v", c.version, got, c.want)
		}
	}
}

// F-LB-01：caps 的 sing-box 默认版本必须是已核实的 fork 版，不得
// 为 latest（后端会逐字拒绝 sing-box 的 latest）；Xray 等其余
// 内核无默认版本，仍走 latest。
func TestSingboxCatalogDefaultVersion(t *testing.T) {
	info, ok := Lookup(TypeSingbox)
	if !ok {
		t.Fatal("sing-box must be in the catalog")
	}
	if info.DefaultVersion != SingboxForkVersion {
		t.Errorf("sing-box DefaultVersion = %q, want fork version %q", info.DefaultVersion, SingboxForkVersion)
	}
	if info.DefaultVersion == "latest" {
		t.Error("sing-box DefaultVersion must never be \"latest\"")
	}
	for _, info := range Catalog() {
		if info.Type == TypeXray && info.DefaultVersion != "" {
			t.Errorf("xray DefaultVersion = %q, want empty (latest default unaffected)", info.DefaultVersion)
		}
	}
}
