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

const (
	vacuumMinFreelistBytes = 64 * 1024
	vacuumFreelistRatio    = 0.05
	vacuumChurnWrites      = 50
	vacuumChurnRatio       = 0.02
)

type ScrubResult struct {
	Attempted    bool
	Completed    bool
	PurgePending bool
	Warning      string
}

type MaintainOptions struct {
	HadDiff     bool
	ForceVacuum bool
}

type MaintainResult struct {
	Optimized    bool
	Vacuumed     bool
	PurgePending bool
	Warning      string
}

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

	if _, err := db.Exec(`INSERT INTO chunk_search(chunk_search) VALUES('optimize')`); err != nil {
		res.Warning = fmt.Sprintf("disk scrub FTS5 optimize failed (%v); re-run proxer index to reclaim purged pages", err)
		return res, nil
	}

	vacuumed, stillPending, warning, err := runVacuumAndCheckpoint(db, dbPath, true)
	res.Warning = warning
	res.PurgePending = stillPending
	res.Completed = vacuumed && !stillPending
	return res, err
}

// Maintain runs gated FTS5 optimize, PRAGMA optimize, and disk-checked VACUUM +
// wal_checkpoint(TRUNCATE) for explicit `proxer index` and `proxer status --vacuum`.
// Clean no-op runs (no diff, writes_since_optimize == 0, purge_pending == false,
// forceVacuum == false) skip optimize and VACUUM completely.
func Maintain(db *sqlx.DB, dbPath string, opts MaintainOptions) (MaintainResult, error) {
	pending, err := PurgePending(db)
	if err != nil {
		return MaintainResult{}, err
	}
	writes, err := WritesSinceOptimize(db)
	if err != nil {
		return MaintainResult{}, err
	}

	res := MaintainResult{
		PurgePending: pending,
	}

	if !opts.ForceVacuum && !opts.HadDiff && writes == 0 && !pending {
		return res, nil
	}

	if _, err := db.Exec(`INSERT INTO chunk_search(chunk_search) VALUES('optimize')`); err != nil {
		res.Warning = fmt.Sprintf("FTS5 optimize failed (%v); re-run proxer index to reclaim pages", err)
		return res, nil
	}
	res.Optimized = true

	// Reset writes_since_optimize as soon as FTS5 optimize completes.
	if writes > 0 {
		tx, err := db.Beginx()
		if err != nil {
			return res, err
		}
		if err := setMeta(tx, metaWritesSinceOptimize, "0"); err != nil {
			tx.Rollback()
			return res, err
		}
		if err := tx.Commit(); err != nil {
			return res, err
		}
	}

	_, _ = db.Exec(`PRAGMA optimize`)

	needVacuum := opts.ForceVacuum || pending
	if !needVacuum {
		var pageCount, freelistCount, pageSize int64
		if err := db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err == nil &&
			db.QueryRow(`PRAGMA freelist_count`).Scan(&freelistCount) == nil &&
			db.QueryRow(`PRAGMA page_size`).Scan(&pageSize) == nil {
			needVacuum = shouldVacuum(pageCount, freelistCount, pageSize, writes)
		}
	}

	if !needVacuum {
		return res, nil
	}

	vacuumed, stillPending, warning, err := runVacuumAndCheckpoint(db, dbPath, pending)
	res.Vacuumed = vacuumed
	res.PurgePending = stillPending
	res.Warning = warning
	return res, err
}

func shouldVacuum(pageCount, freelistCount, pageSize int64, writesSinceOptimize int) bool {
	if pageCount <= 0 || freelistCount <= 0 || pageSize <= 0 {
		return false
	}
	if freelistCount*pageSize < vacuumMinFreelistBytes {
		return false
	}
	ratio := float64(freelistCount) / float64(pageCount)
	if ratio >= vacuumFreelistRatio {
		return true
	}
	return writesSinceOptimize >= vacuumChurnWrites && ratio >= vacuumChurnRatio
}

func runVacuumAndCheckpoint(db *sqlx.DB, dbPath string, purgePending bool) (vacuumed bool, stillPending bool, warning string, err error) {
	stillPending = purgePending

	if dbPath != "" {
		if info, statErr := os.Stat(dbPath); statErr == nil && info.Size() > 0 {
			required := uint64(info.Size()) * 2
			if avail, diskErr := availableDiskBytes(filepath.Dir(dbPath)); diskErr == nil && avail < required {
				return false, stillPending, fmt.Sprintf(
					"skipped VACUUM: need at least %d bytes free (2x DB size), have %d bytes; free disk space and re-run proxer index",
					required, avail,
				), nil
			}
		}
	}

	if _, execErr := db.Exec(`VACUUM`); execErr != nil {
		return false, stillPending, fmt.Sprintf("VACUUM failed (%v); re-run proxer index to reclaim pages", execErr), nil
	}
	vacuumed = true

	var busy, logFrames, checkpointed int
	if cpErr := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); cpErr != nil {
		if dbPath != "" {
			_ = ChmodDBFiles(dbPath)
		}
		return vacuumed, stillPending, fmt.Sprintf("WAL checkpoint failed (%v); close other readers and re-run proxer index", cpErr), nil
	}

	if dbPath != "" {
		if chmodErr := ChmodDBFiles(dbPath); chmodErr != nil {
			return vacuumed, stillPending, "", chmodErr
		}
	}

	if busy != 0 || logFrames != 0 {
		return vacuumed, stillPending, fmt.Sprintf(
			"WAL checkpoint incomplete (busy=%d, log=%d); close other readers and re-run proxer index to clear residual WAL frames",
			busy, logFrames,
		), nil
	}

	if purgePending {
		tx, txErr := db.Beginx()
		if txErr != nil {
			return vacuumed, stillPending, "", txErr
		}
		if metaErr := setMeta(tx, metaPurgePending, "0"); metaErr != nil {
			tx.Rollback()
			return vacuumed, stillPending, "", metaErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return vacuumed, stillPending, "", commitErr
		}

		// Best-effort second truncate so the meta update itself does not linger in -wal.
		_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		if dbPath != "" {
			if chmodErr := ChmodDBFiles(dbPath); chmodErr != nil {
				return vacuumed, false, "", chmodErr
			}
		}
		stillPending = false
	}

	return vacuumed, stillPending, "", nil
}
