package repo

import (
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	gsqlite "github.com/glebarez/sqlite"
)

// Package K: pruned rows must not leave the SQLite file at its
// high-water mark forever. These tests pin the space accounting
// and both reclaim paths (incremental for new databases, full
// VACUUM for legacy auto_vacuum=NONE ones).

func growAndFreeScratch(t *testing.T, r *Repository, rows int) {
	t.Helper()
	if err := r.db.Exec("CREATE TABLE IF NOT EXISTS k_scratch (id INTEGER PRIMARY KEY, blob TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("x", 512)
	for i := 0; i < rows; i++ {
		if err := r.db.Exec("INSERT INTO k_scratch (blob) VALUES (?)", blob).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := r.db.Exec("DELETE FROM k_scratch").Error; err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteReclaimIncremental(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "k-inc.db"))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := r.SQLiteSpaceStats()
	if err != nil {
		t.Fatal(err)
	}
	if !stats.IsSQLite {
		t.Fatal("stats.IsSQLite = false for a sqlite repository")
	}
	if stats.AutoVacuum != 2 {
		t.Fatalf("new database auto_vacuum = %d, want 2 (INCREMENTAL from the DSN pragma)", stats.AutoVacuum)
	}

	growAndFreeScratch(t, r, 800)
	stats, err = r.SQLiteSpaceStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.FreelistCount == 0 {
		t.Fatal("freelist = 0 after delete-all; scratch growth did not free pages")
	}
	if err := r.IncrementalVacuumSQLite(stats.FreelistCount); err != nil {
		t.Fatal(err)
	}
	after, err := r.SQLiteSpaceStats()
	if err != nil {
		t.Fatal(err)
	}
	if after.FreelistCount != 0 {
		t.Fatalf("freelist = %d after incremental_vacuum, want 0", after.FreelistCount)
	}
	if after.PageCount >= stats.PageCount {
		t.Fatalf("page_count %d -> %d, want shrink", stats.PageCount, after.PageCount)
	}
	if got := r.DataDirectory(); got == "" {
		t.Fatal("DataDirectory empty for sqlite repository")
	}
}

func TestSQLiteReclaimVacuumLegacyNoneMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k-legacy.db")
	// A legacy database: created without the auto_vacuum pragma,
	// so its mode is NONE and incremental_vacuum cannot help it.
	gdb, err := gorm.Open(gsqlite.Open("file:"+path+"?_pragma=journal_mode(WAL)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	r := &Repository{db: gdb, dbPath: path}

	stats, err := r.SQLiteSpaceStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.AutoVacuum != 0 {
		t.Fatalf("legacy database auto_vacuum = %d, want 0 (NONE)", stats.AutoVacuum)
	}
	growAndFreeScratch(t, r, 800)
	stats, err = r.SQLiteSpaceStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.FreelistCount == 0 {
		t.Fatal("freelist = 0 after delete-all on legacy database")
	}
	if err := r.VacuumSQLite(); err != nil {
		t.Fatal(err)
	}
	after, err := r.SQLiteSpaceStats()
	if err != nil {
		t.Fatal(err)
	}
	if after.FreelistCount != 0 {
		t.Fatalf("freelist = %d after VACUUM, want 0", after.FreelistCount)
	}
	// The full VACUUM also migrates the legacy file to
	// INCREMENTAL, so later prunes take the cheap path.
	if after.AutoVacuum != 2 {
		t.Fatalf("auto_vacuum = %d after VACUUM, want 2 (migrated to INCREMENTAL)", after.AutoVacuum)
	}
}
