package kernel

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// R6-09 regression gates: kernel bin dir retention. Install used
// to replace only the same-version directory and nothing ever
// enumerated the version dirs, so every successful version change
// left one more generation (tens of MB plus a geodata copy) on
// disk forever. The policy now enforced at every confirmed Apply:
// the current generation's dirs and the previous generation's
// (the intra-Apply rollback source) survive; everything older is
// removed.

func binDirNames(t *testing.T, binRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(binRoot)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func TestIsManagedBinDirName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// Main version dirs FLMX creates.
		{"1.0.0", true},
		{"1.14.4-flmx1", true},
		{"2.9.6", true},
		{"v1.3.3", true},
		{"latest", true},
		// FLMX's own swap leftovers keep their old behaviour.
		{"1.0.0.old", true},
		{"1.0.0.staging", true},
		// Extra dirs FLMX creates ("extra-"+Name+"-"+Version).
		{"extra-geo-1", true},
		{"extra-geo-1.0.0", true},
		{"extra-shadow-tls-0.2.25", true},
		{"extra-my-geo-1.0.0", true},
		// Foreign / user data: never prunable.
		{"backup", false},
		{"my-stuff", false},
		{"data", false},
		{"gost", false},
		{"1.0.0_old", false},
		{"extra-geo", false},
		{"extra--1.0.0", false},
		{"extra-geo-", false},
		{"extra-geo-1..0", false},
		{"extra-geo..x-1.0.0", false},
		{"a..b", false},
		{"-1.0.0", false},
		{".1.0.0", false},
		{"1", false},
		{"2024", false},
		{"", false},
		{"extra-", false},
	}
	for _, tc := range cases {
		if got := isManagedBinDirName(tc.name); got != tc.want {
			t.Errorf("isManagedBinDirName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPruneStaleBinDirsKeepsCurrentAndPreviousGenerations(t *testing.T) {
	base := t.TempDir()
	m := NewManager(base)
	binRoot := filepath.Join(base, "xray", "bin")
	gens := []string{"1.0.0", "2.0.0", "3.0.0", "extra-geo-1", "extra-geo-2", "v0.9.0"}
	for _, g := range gens {
		dir := filepath.Join(binRoot, g)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), []byte(g), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Foreign directories under the shared bin root: not FLMX
	// version dirs, must survive the sweep untouched (P1-2 guard).
	foreign := []string{"backup", "my-stuff", "data", "1.0.0_old", "extra-geo", "extra--1.0.0"}
	for _, f := range foreign {
		dir := filepath.Join(binRoot, f)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload"), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A stray non-directory entry is not a version dir and must
	// survive the sweep.
	if err := os.WriteFile(filepath.Join(binRoot, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	keep := map[string]bool{
		filepath.Join(binRoot, "3.0.0"):       true,
		filepath.Join(binRoot, "2.0.0"):       true,
		filepath.Join(binRoot, "extra-geo-2"): true,
	}
	removed, err := m.pruneStaleBinDirs("xray", keep)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(removed) != 3 || removed[0] != "1.0.0" || removed[1] != "extra-geo-1" || removed[2] != "v0.9.0" {
		t.Fatalf("removed = %v, want [1.0.0 extra-geo-1 v0.9.0]", removed)
	}
	got := binDirNames(t, binRoot)
	want := []string{"1.0.0_old", "2.0.0", "3.0.0", "backup", "data", "extra--1.0.0", "extra-geo", "extra-geo-2", "my-stuff"}
	if len(got) != len(want) {
		t.Fatalf("remaining dirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("remaining dirs = %v, want %v", got, want)
		}
	}
	// Kept generations keep their content; the stray file survives.
	for _, g := range want {
		data, err := os.ReadFile(filepath.Join(binRoot, g, "payload"))
		if err != nil || string(data) != g {
			t.Fatalf("kept dir %s content = %q, %v", g, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(binRoot, "stray.txt")); err != nil {
		t.Fatalf("stray file must survive: %v", err)
	}
}

func TestPruneStaleBinDirsMissingRootAndNothingStale(t *testing.T) {
	base := t.TempDir()
	m := NewManager(base)
	removed, err := m.pruneStaleBinDirs("xray", map[string]bool{})
	if err != nil || len(removed) != 0 {
		t.Fatalf("missing bin root must be a no-op, got removed=%v err=%v", removed, err)
	}

	binRoot := filepath.Join(base, "xray", "bin")
	if err := os.MkdirAll(filepath.Join(binRoot, "1.0.0"), 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err = m.pruneStaleBinDirs("xray", map[string]bool{filepath.Join(binRoot, "1.0.0"): true})
	if err != nil || len(removed) != 0 {
		t.Fatalf("nothing stale must remove nothing, got removed=%v err=%v", removed, err)
	}
}

// End-to-end through Apply: three successive version changes must
// leave exactly the current and previous generations on disk.
func TestApplyPrunesSupersededKernelGenerations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as fake kernel")
	}
	stubNft(t)
	srv, _ := serveKernel(t, zipWith(t, map[string]string{
		"xray":      fakeKernel,
		"geoip.dat": "geo",
	}))
	m := NewManager(t.TempDir())

	applyVersion := func(version, config string) {
		t.Helper()
		req := applyRequest(srv, config, true)
		req.Version = version
		st, err := m.Apply(req)
		if err != nil {
			t.Fatalf("apply %s: %v", version, err)
		}
		if !st.Running {
			t.Fatalf("apply %s: kernel not running (state %+v)", version, st)
		}
	}
	applyVersion("1.0.0", `{"v":1}`)
	applyVersion("2.0.0", `{"v":2}`)
	applyVersion("3.0.0", `{"v":3}`)

	binRoot := filepath.Join(m.baseDir, "xray", "bin")
	got := binDirNames(t, binRoot)
	if len(got) != 2 || got[0] != "2.0.0" || got[1] != "3.0.0" {
		t.Fatalf("bin dirs after three version changes = %v, want [2.0.0 3.0.0]", got)
	}
}
