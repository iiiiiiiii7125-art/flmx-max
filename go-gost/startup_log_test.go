package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GARBAGE-AUDIT A2: agent-startup.log was append-only with no cap.
// Appends must keep accumulating under the cap and truncate the
// file back before writing once it has grown past the cap, so the
// newest startup failure is always recorded while the file stays
// bounded.

func TestAppendStartupLogLineAccumulatesUnderCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-startup.log")
	if err := appendStartupLogLine(path, "first\n"); err != nil {
		t.Fatal(err)
	}
	if err := appendStartupLogLine(path, "second\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\nsecond\n" {
		t.Fatalf("under-cap appends must accumulate, got %q", got)
	}
}

func TestAppendStartupLogLineTruncatesOverCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-startup.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", startupLogMaxBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := appendStartupLogLine(path, "latest failure\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "latest failure\n" {
		t.Fatalf("over-cap log must be truncated before the new line is written, got %d bytes", len(got))
	}
}
