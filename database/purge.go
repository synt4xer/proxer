package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/internal/filesystem"
)

// availableDiskBytes is overridden in tests to exercise low-disk-space paths.
var availableDiskBytes = platformAvailableDiskBytes

// ScrubResult summarizes Stage 2 plaintext disk reclamation.
type ScrubResult struct {
	Attempted    bool
	Completed    bool
	PurgePending bool
	Warning      string
}

// ChmodDBFiles enforces 0600 owner-only permissions on dbPath and its WAL/SHM
// siblings if present on disk.
func ChmodDBFiles(dbPath string) error {
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to tighten permissions on %s: %w", p, err)
		}
	}
	return nil
}

// purgeSensitiveIfNeeded implements Stage 1 of the leaked-DB upgrade contract:
// when indexSensitive is false, it uses an O(1) meta sentinel to skip scanning
// indexed_files on steady-state opens. When the sentinel is absent or "0", it
// removes any sensitive file rows in a single fast transaction and records
// meta.purge_pending = "1" so Stage 2 (explicit index / status --vacuum) can
// reclaim unallocated DB pages and WAL frames without stalling search.
func purgeSensitiveIfNeeded(db *sqlx.DB, indexSensitive bool) error {
	if indexSensitive {
		return nil
	}

	sentinel, err := metaValue(db, metaSensitivePurge)
	if err != nil {
		return err
	}
	if sentinel == "1" {
		return nil
	}

	var paths []string
	if err := db.Select(&paths, "SELECT filepath FROM indexed_files"); err != nil {
		return fmt.Errorf("failed to scan manifest for sensitive files: %w", err)
	}

	var sensitive []string
	for _, rel := range paths {
		if filesystem.IsSensitivePath(rel) {
			sensitive = append(sensitive, rel)
		}
	}

	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, rel := range sensitive {
		if err := Remove(tx, rel); err != nil {
			return err
		}
	}
	if len(sensitive) > 0 {
		if err := setMeta(tx, metaPurgePending, "1"); err != nil {
			return err
		}
	}
	if err := setMeta(tx, metaSensitivePurge, "1"); err != nil {
		return err
	}

	return tx.Commit()
}

// ScrubPurgedData implements Stage 2 plaintext disk scrub: when
// meta.purge_pending is "1", it verifies >= 2x DB free disk space, runs FTS5
// optimize + VACUUM + PRAGMA wal_checkpoint(TRUNCATE), re-applies 0600 file
// permissions, and clears meta.purge_pending only when busy == 0 && log == 0.
func ScrubPurgedData(db *sqlx.DB, dbPath string) (ScrubResult, error) {
	pending, err := PurgePending(db)
	if err != nil {
		return ScrubResult{}, err
	}
	if !pending {
		return ScrubResult{}, nil
	}

	res := ScrubResult{
		Attempted:    true,
		PurgePending: true,
	}

	if info, err := os.Stat(dbPath); err == nil && info.Size() > 0 {
		required := uint64(info.Size()) * 2
		if avail, err := availableDiskBytes(filepath.Dir(dbPath)); err == nil && avail < required {
			res.Warning = fmt.Sprintf(
				"skipped disk scrub: need at least %d bytes free (2x DB size), have %d bytes; free disk space and re-run proxer index",
				required, avail,
			)
			return res, nil
		}
	}

	if _, err := db.Exec(`INSERT INTO chunk_search(chunk_search) VALUES('optimize')`); err != nil {
		res.Warning = fmt.Sprintf("disk scrub FTS5 optimize failed (%v); re-run proxer index to reclaim purged pages", err)
		return res, nil
	}

	if _, err := db.Exec(`VACUUM`); err != nil {
		res.Warning = fmt.Sprintf("disk scrub VACUUM failed (%v); re-run proxer index to reclaim purged pages", err)
		return res, nil
	}

	var busy, logFrames, checkpointed int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		res.Warning = fmt.Sprintf("disk scrub WAL checkpoint failed (%v); close other readers and re-run proxer index", err)
		_ = ChmodDBFiles(dbPath)
		return res, nil
	}

	if err := ChmodDBFiles(dbPath); err != nil {
		return res, err
	}

	if busy != 0 || logFrames != 0 {
		res.Warning = fmt.Sprintf(
			"WAL checkpoint incomplete (busy=%d, log=%d); close other readers and re-run proxer index to clear residual WAL frames",
			busy, logFrames,
		)
		return res, nil
	}

	tx, err := db.Beginx()
	if err != nil {
		return res, err
	}
	if err := setMeta(tx, metaPurgePending, "0"); err != nil {
		tx.Rollback()
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}

	// Best-effort second truncate so the meta update itself does not linger in -wal.
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := ChmodDBFiles(dbPath); err != nil {
		return res, err
	}

	res.Completed = true
	res.PurgePending = false
	return res, nil
}
