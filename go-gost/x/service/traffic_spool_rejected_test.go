package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// R2-038 instance 3: quarantined batch archives (.rejected-*.json)
// were excluded from the orphan-spool scan by name and left to
// manual disposal only, so a node with frequent stream conflicts
// accumulated them forever with no signal. They are now aged out at
// spool configuration time: older than the retention window, or
// beyond the newest-N cap. Orphan spools themselves are still only
// named, never deleted — unconfirmed traffic must not be destroyed
// by a sweep.

func writeSpoolDirFile(t *testing.T, dir, name string, age time.Duration, now time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := now.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat err = %v", path, err)
	}
}

func TestSweepRejectedTrafficArchivesByAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := writeSpoolDirFile(t, dir, "flmx_traffic_a.json.rejected-stream-1.json", 31*24*time.Hour, now)
	fresh := writeSpoolDirFile(t, dir, "flmx_traffic_a.json.rejected-stream-2.json", time.Hour, now)
	orphan := writeSpoolDirFile(t, dir, "flmx_traffic_b.json", 90*24*time.Hour, now)
	other := writeSpoolDirFile(t, dir, "notes.txt", 90*24*time.Hour, now)

	removed := sweepRejectedTrafficArchives(dir, now)
	if len(removed) != 1 || removed[0] != "flmx_traffic_a.json.rejected-stream-1.json" {
		t.Fatalf("only the aged-out archive may be removed, got %v", removed)
	}
	mustNotExist(t, old)
	mustExist(t, fresh)
	mustExist(t, orphan) // orphan spools are never swept, however old
	mustExist(t, other)
}

func TestSweepRejectedTrafficArchivesByCount(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	total := rejectedArchiveMaxCount + 5
	paths := make([]string, total)
	for i := 0; i < total; i++ {
		// All inside the age window; index 0 is the oldest.
		paths[i] = writeSpoolDirFile(t, dir, fmt.Sprintf("flmx_traffic_a.json.rejected-s-%03d.json", i), time.Duration(total-i)*time.Minute, now)
	}
	removed := sweepRejectedTrafficArchives(dir, now)
	if len(removed) != 5 {
		t.Fatalf("cap %d with %d archives must remove exactly the 5 oldest, removed %v", rejectedArchiveMaxCount, total, removed)
	}
	for i := 0; i < 5; i++ {
		mustNotExist(t, paths[i])
	}
	for i := 5; i < total; i++ {
		mustExist(t, paths[i])
	}
}

func TestSweepRejectedTrafficArchivesEmptyDir(t *testing.T) {
	if removed := sweepRejectedTrafficArchives(t.TempDir(), time.Now()); len(removed) != 0 {
		t.Fatalf("empty dir must remove nothing, got %v", removed)
	}
	if removed := sweepRejectedTrafficArchives(filepath.Join(t.TempDir(), "absent"), time.Now()); len(removed) != 0 {
		t.Fatalf("missing dir must remove nothing, got %v", removed)
	}
}
