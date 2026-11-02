package main

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Program-level tests for the agent's root package (R2-013 /
// BD-03). These could not exist while flag.Parse lived in an init
// function: the test binary died in init on the -test.* flags
// before any test ran, so the package had zero test files and its
// startup logic (flag surface, core-sweep directory derivation)
// was covered only by helper-package tests and binary-level runs.

// TestAgentFlagSetAndDefaults pins the command-line surface: the
// exact flag set, defaults and usages the agent has always taken.
// Moving flag.Parse out of init must not change any of it.
func TestAgentFlagSetAndDefaults(t *testing.T) {
	want := map[string]struct{ def, usage string }{
		"L":       {"[]", "service list"},
		"F":       {"[]", "chain node list"},
		"C":       {"", "configuration file"},
		"V":       {"false", "print version"},
		"O":       {"", "output format, one of yaml|json format"},
		"D":       {"false", "debug mode"},
		"DD":      {"false", "trace mode"},
		"api":     {"", "api service address"},
		"metrics": {"", "metrics service address"},
	}
	got := map[string]flag.Flag{}
	flag.VisitAll(func(f *flag.Flag) {
		if len(f.Name) >= 5 && f.Name[:5] == "test." {
			return // testing's own flags, not the agent's surface
		}
		got[f.Name] = *f
	})
	var names []string
	for name := range got {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(got) != len(want) {
		t.Fatalf("agent flag set = %v, want exactly %d flags", names, len(want))
	}
	for name, w := range want {
		f, ok := got[name]
		if !ok {
			t.Fatalf("flag -%s missing (have %v)", name, names)
		}
		if f.DefValue != w.def {
			t.Errorf("flag -%s default = %q, want %q", name, f.DefValue, w.def)
		}
		if f.Usage != w.usage {
			t.Errorf("flag -%s usage = %q, want %q", name, f.Usage, w.usage)
		}
	}
}

// TestCoreSweepDirs pins the startup core-sweep directory
// derivation: the working directory, the binary's directory and
// every kernel run directory under the kernel base — and nothing
// derived from non-directory entries or a missing base.
func TestCoreSweepDirs(t *testing.T) {
	base := t.TempDir()
	for _, k := range []string{"xray", "singbox"} {
		if err := os.MkdirAll(filepath.Join(base, k, "run"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "a-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLMX_KERNEL_DIR", base)
	t.Setenv("FLVX_KERNEL_DIR", "")

	dirs := coreSweepDirs()
	set := map[string]bool{}
	for _, d := range dirs {
		if set[d] {
			t.Fatalf("duplicate sweep dir %q in %v", d, dirs)
		}
		set[d] = true
	}
	if !set["."] {
		t.Fatalf("working directory must be swept: %v", dirs)
	}
	if exe, err := os.Executable(); err == nil && !set[filepath.Dir(exe)] {
		t.Fatalf("binary directory must be swept: %v", dirs)
	}
	for _, k := range []string{"xray", "singbox"} {
		if want := filepath.Join(base, k, "run"); !set[want] {
			t.Fatalf("kernel run dir %q must be swept: %v", want, dirs)
		}
	}
	if set[filepath.Join(base, "a-file", "run")] {
		t.Fatalf("a regular file must not yield a sweep dir: %v", dirs)
	}

	// Missing base: only the working and binary directories remain.
	t.Setenv("FLMX_KERNEL_DIR", filepath.Join(base, "missing"))
	dirs = coreSweepDirs()
	if len(dirs) != 2 {
		t.Fatalf("missing kernel base must leave exactly the cwd and binary dirs, got %v", dirs)
	}
}
