package repo

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gorm.io/gorm"
)

type DatabaseStorageSummary struct {
	DBType            string `json:"dbType"`
	DatabaseSizeBytes int64  `json:"databaseSizeBytes"`
	DatabaseSizeText  string `json:"databaseSizeText"`
}

func (r *Repository) DatabaseStorageSummary() (DatabaseStorageSummary, error) {
	if r == nil || r.db == nil {
		return DatabaseStorageSummary{}, errors.New("repository not initialized")
	}

	switch r.db.Dialector.Name() {
	case "sqlite":
		size, err := sqliteDatabaseFileSize(r.dbPath)
		if err != nil {
			return DatabaseStorageSummary{}, err
		}
		return DatabaseStorageSummary{DBType: "sqlite", DatabaseSizeBytes: size, DatabaseSizeText: formatDatabaseSize(size)}, nil
	case "postgres":
		var size int64
		if err := r.db.Raw("SELECT pg_database_size(current_database())").Scan(&size).Error; err != nil {
			return DatabaseStorageSummary{}, err
		}
		return DatabaseStorageSummary{DBType: "postgres", DatabaseSizeBytes: size, DatabaseSizeText: formatDatabaseSize(size)}, nil
	default:
		return DatabaseStorageSummary{}, fmt.Errorf("unsupported database dialect %q", r.db.Dialector.Name())
	}
}

func sqliteDatabaseFileSize(path string) (int64, error) {
	if path == "" || path == ":memory:" {
		return 0, nil
	}

	var total int64
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		if !info.IsDir() {
			total += info.Size()
		}
	}
	return total, nil
}

// SQLiteSpaceStats is the free-page accounting behind the
// package K disk reclaim: row prunes bound row counts but never
// shrink the database file (deleted pages go to the freelist),
// so without a vacuum path gost.db's on-disk high-water mark
// grows monotonically on exactly the small disks that can least
// afford it. AutoVacuum reports PRAGMA auto_vacuum (0=NONE,
// 1=FULL, 2=INCREMENTAL).
type SQLiteSpaceStats struct {
	IsSQLite      bool
	PageCount     int64
	FreelistCount int64
	PageSize      int64
	AutoVacuum    int64
}

// ReclaimableBytes is the file space a vacuum can return.
func (s SQLiteSpaceStats) ReclaimableBytes() int64 {
	return s.FreelistCount * s.PageSize
}

func (r *Repository) SQLiteSpaceStats() (SQLiteSpaceStats, error) {
	if r == nil || r.db == nil {
		return SQLiteSpaceStats{}, errors.New("repository not initialized")
	}
	if r.db.Dialector.Name() != "sqlite" {
		// Postgres has its own autovacuum; nothing to reclaim here.
		return SQLiteSpaceStats{IsSQLite: false}, nil
	}
	var stats SQLiteSpaceStats
	stats.IsSQLite = true
	for _, q := range []struct {
		sql string
		dst *int64
	}{
		{"PRAGMA page_count", &stats.PageCount},
		{"PRAGMA freelist_count", &stats.FreelistCount},
		{"PRAGMA page_size", &stats.PageSize},
		{"PRAGMA auto_vacuum", &stats.AutoVacuum},
	} {
		if err := r.db.Raw(q.sql).Scan(q.dst).Error; err != nil {
			return SQLiteSpaceStats{}, fmt.Errorf("%s: %w", q.sql, err)
		}
	}
	return stats, nil
}

// IncrementalVacuumSQLite returns up to pages freelist pages to
// the OS. Only effective when AutoVacuum == 2 (INCREMENTAL);
// callers gate on SQLiteSpaceStats. With this driver/SQLite
// build each pragma call frees exactly one page regardless of
// its argument (verified by test: freelist 115 -> 0 took one
// call per page), so the reclaim is a bounded loop of
// single-page calls; calls past an empty freelist are no-ops.
// The per-call cap bounds one reclaim pass to ~256MB of returns
// (4K pages); larger backlogs drain over successive prune
// cycles.
func (r *Repository) IncrementalVacuumSQLite(pages int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	const maxPagesPerPass = 65536
	if pages > maxPagesPerPass {
		pages = maxPagesPerPass
	}
	for i := int64(0); i < pages; i++ {
		if err := r.db.Exec("PRAGMA incremental_vacuum(1)").Error; err != nil {
			return err
		}
	}
	return nil
}

// VacuumSQLite fully rewrites the database file, returning all
// freelist space. It first requests INCREMENTAL auto_vacuum on
// the connection, so the rewrite also migrates a legacy
// auto_vacuum=NONE file to INCREMENTAL (the mode takes effect
// through the VACUUM itself). It briefly blocks the single
// connection; callers gate it on a large reclaimable amount and
// a minimum interval (see the ingestion prune cycle).
func (r *Repository) VacuumSQLite() error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if err := r.db.Exec("PRAGMA auto_vacuum=INCREMENTAL").Error; err != nil {
		return err
	}
	return r.db.Exec("VACUUM").Error
}

// ensureSQLiteIncrementalVacuum puts a brand-new database file
// into INCREMENTAL auto_vacuum mode, so prune-freed pages can be
// returned incrementally instead of accumulating on the
// freelist. The pragma only takes effect through a VACUUM, and
// the DSN _pragma form is silently ineffective with this driver
// (verified: the value reads back 0), hence the explicit pair —
// instant on an empty file. Best effort: failure is logged, and
// the ingestion reclaim's gated full VACUUM remains as the
// fallback that also migrates the mode.
func ensureSQLiteIncrementalVacuum(db *gorm.DB) {
	var mode int64
	if err := db.Raw("PRAGMA auto_vacuum").Scan(&mode).Error; err != nil {
		log.Printf("sqlite auto_vacuum check failed err=%v", err)
		return
	}
	if mode == 2 {
		return
	}
	if err := db.Exec("PRAGMA auto_vacuum=INCREMENTAL").Error; err != nil {
		log.Printf("sqlite auto_vacuum set failed err=%v", err)
		return
	}
	if err := db.Exec("VACUUM").Error; err != nil {
		log.Printf("sqlite auto_vacuum vacuum failed err=%v", err)
	}
}

// DataDirectory returns the directory holding the panel's data
// files (the SQLite database directory), or "" when it cannot be
// determined (Postgres mode, in-memory database).
func (r *Repository) DataDirectory() string {
	if r == nil || r.db == nil || r.db.Dialector.Name() != "sqlite" {
		return ""
	}
	if r.dbPath == "" || r.dbPath == ":memory:" {
		return ""
	}
	return filepath.Dir(r.dbPath)
}

func formatDatabaseSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(bytes) / 1024
	for _, unit := range units {
		if value < 1024 || unit == "TB" {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%d B", bytes)
}
