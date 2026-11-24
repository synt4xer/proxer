package indexer

import (
	"context"
	"database/sql/driver"
	json "encoding/json/v2"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/filesystem"
)

// Refreshed counts what a delta build changed. It is reported by search so the
// caller can see the index move instead of guessing whether it is current.
// Skipped counts skipped files only; IgnoredDirs counts pruned directory roots
// separately.
type Refreshed struct {
	Added           int                   `json:"added"`
	Changed         int                   `json:"changed"`
	Removed         int                   `json:"removed"`
	Skipped         int                   `json:"skipped"`
	IgnoredDirs     int                   `json:"ignored_dirs,omitempty,omitzero"`
	SkippedByReason *domain.SkippedCounts `json:"skipped_by_reason,omitempty"`
}

func (r Refreshed) Empty() bool {
	return r.Added == 0 && r.Changed == 0 && r.Removed == 0 && r.Skipped == 0 && r.IgnoredDirs == 0
}

func (r Refreshed) ToSummary() domain.RefreshSummary {
	return domain.RefreshSummary{
		Added:           r.Added,
		Changed:         r.Changed,
		Removed:         r.Removed,
		Skipped:         r.Skipped,
		IgnoredDirs:     r.IgnoredDirs,
		SkippedByReason: r.SkippedByReason,
	}
}

func (r *Refreshed) recordSkip(reason filesystem.SkipReason) {
	if reason == filesystem.SkipNone {
		return
	}
	if r.SkippedByReason == nil {
		r.SkippedByReason = &domain.SkippedCounts{}
	}
	r.Skipped++
	switch reason {
	case filesystem.SkipIgnoredFile:
		r.SkippedByReason.IgnoredFiles++
	case filesystem.SkipExtension:
		r.SkippedByReason.Extension++
	case filesystem.SkipSensitive:
		r.SkippedByReason.Sensitive++
	case filesystem.SkipMinified:
		r.SkippedByReason.Minified++
	case filesystem.SkipSize:
		r.SkippedByReason.Size++
	}
}

// ChangedFile pairs a changed manifest record with its already-read bytes so
// applying the diff avoids re-reading and re-hashing the file.
type ChangedFile struct {
	Record  database.FileRecord
	Content []byte
	Removed bool
}

// Diff is the work one delta build has to apply. The four buckets are disjoint:
// a file whose size and mtime are unchanged is in none of them, a file whose
// bytes are provably identical is Touched and only its manifest row is
// refreshed, and a file whose hash moved is Changed and gets re-chunked.
type Diff struct {
	Added           []database.FileRecord
	Changed         []ChangedFile
	Touched         []database.FileRecord
	Removed         []string
	Skipped         int
	IgnoredDirs     int
	SkippedByReason *domain.SkippedCounts
}

func (d *Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Changed) == 0 && len(d.Touched) == 0 && len(d.Removed) == 0
}

func (d *Diff) recordSkip(reason filesystem.SkipReason) {
	if reason == filesystem.SkipNone {
		return
	}
	if d.SkippedByReason == nil {
		d.SkippedByReason = &domain.SkippedCounts{}
	}
	d.Skipped++
	switch reason {
	case filesystem.SkipIgnoredFile:
		d.SkippedByReason.IgnoredFiles++
	case filesystem.SkipExtension:
		d.SkippedByReason.Extension++
	case filesystem.SkipSensitive:
		d.SkippedByReason.Sensitive++
	case filesystem.SkipMinified:
		d.SkippedByReason.Minified++
	case filesystem.SkipSize:
		d.SkippedByReason.Size++
	}
}

func (d *Diff) refreshedBase() Refreshed {
	var counts *domain.SkippedCounts
	if d.SkippedByReason != nil {
		copied := *d.SkippedByReason
		counts = &copied
	}
	return Refreshed{
		Skipped:         d.Skipped,
		IgnoredDirs:     d.IgnoredDirs,
		SkippedByReason: counts,
	}
}

func isInternalDBFile(rel string) bool {
	switch rel {
	case config.DBFile, config.DBFile + "-wal", config.DBFile + "-shm", config.DBFile + "-journal":
		return true
	}
	return false
}

// computeDiff walks the tree without opening a write transaction and compares
// what it finds with the manifest. Reading and hashing happen here, before the
// lock is taken, so a file that turns out to be unchanged costs a stat and
// nothing else.
func computeDiff(db *sqlx.DB, rootPath string, opts IndexOptions) (*Diff, error) {
	manifest, err := database.Files(db)
	if err != nil {
		return nil, err
	}

	diff := &Diff{}
	matcher := filesystem.NewIgnoreMatcher(rootPath, opts.IgnoredDirs)

	walkErr := filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if entry.IsDir() {
			// The root itself is never skipped: an ignored directory name such
			// as ".cache" is legitimate when it is the project being indexed.
			if path == rootPath {
				return nil
			}
			rel, err := relativePath(rootPath, path)
			if err != nil {
				return err
			}
			if matcher.IsDirIgnored(rel) {
				diff.IgnoredDirs++
				return filepath.SkipDir
			}
			matcher.LoadDir(rel)
			return nil
		}

		rel, err := relativePath(rootPath, path)
		if err != nil {
			return err
		}

		if isInternalDBFile(rel) {
			return nil
		}

		if matcher.IsFileIgnored(rel) {
			diff.recordSkip(filesystem.SkipIgnoredFile)
			return nil
		}

		if ok, reason := filesystem.IsIndexableFileName(entry.Name(), opts.IndexSensitive); !ok {
			diff.recordSkip(reason)
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Size() > config.MaxFileSize {
			diff.recordSkip(filesystem.SkipSize)
			return nil
		}

		current := database.FileRecord{Path: rel, Size: info.Size(), MTimeNS: info.ModTime().UnixNano()}

		stored, known := manifest[rel]
		delete(manifest, rel)

		if known && stored.Size == current.Size && stored.MTimeNS == current.MTimeNS {
			// The cheap gate says the file has not moved since it was indexed.
			return nil
		}

		if !known {
			// A new file needs no hash here: the delta reads it once, when it
			// stores it.
			diff.Added = append(diff.Added, current)
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// The file disappeared mid-walk; treating it as a change lets
				// the delta drop rows that describe something no longer there.
				current.Hash = stored.Hash
				diff.Changed = append(diff.Changed, ChangedFile{Record: current, Removed: true})
				return nil
			}
			// Any other read failure is reported instead of silently keeping a
			// stale row that the caller would read as current.
			return fmt.Errorf("failed to read %s: %w", rel, err)
		}

		current.Hash = ContentHash(content)
		if current.Hash == stored.Hash {
			// Same bytes, new timestamp: only the manifest needs updating.
			diff.Touched = append(diff.Touched, current)
			return nil
		}

		diff.Changed = append(diff.Changed, ChangedFile{Record: current, Content: content})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	for rel := range manifest {
		diff.Removed = append(diff.Removed, rel)
	}

	sort.Slice(diff.Added, func(i, j int) bool { return diff.Added[i].Path < diff.Added[j].Path })
	sort.Slice(diff.Changed, func(i, j int) bool { return diff.Changed[i].Record.Path < diff.Changed[j].Record.Path })
	sort.Slice(diff.Touched, func(i, j int) bool { return diff.Touched[i].Path < diff.Touched[j].Path })
	sort.Strings(diff.Removed)

	return diff, nil
}

// applyDiff writes a computed diff in one transaction. An empty diff never
// reaches this function, which is what keeps a no-change refresh free of the
// write lock a concurrent reader would have to queue behind.
func applyDiff(db *sqlx.DB, rootPath string, diff *Diff, opts IndexOptions) (Refreshed, error) {
	var tx *sqlx.Tx
	var err error
	if opts.BusyTimeoutMS > 0 {
		ctx := opts.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		conn, cerr := db.Connx(ctx)
		if cerr != nil {
			return Refreshed{}, cerr
		}
		defer conn.Close()

		if _, cerr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", opts.BusyTimeoutMS)); cerr != nil {
			return Refreshed{}, cerr
		}
		defer func() {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			if _, rerr := conn.ExecContext(restoreCtx, fmt.Sprintf("PRAGMA busy_timeout = %d", database.DefaultBusyTimeoutMS)); rerr != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}()

		tx, err = conn.BeginTxx(ctx, nil)
	} else {
		tx, err = db.Beginx()
	}
	if err != nil {
		return Refreshed{}, err
	}
	defer tx.Rollback()

	writer, err := database.NewWriter(tx, Chunking())
	if err != nil {
		return Refreshed{}, err
	}

	refreshed := diff.refreshedBase()
	resetPurgeSentinel := opts.IndexSensitive

	for _, rel := range diff.Removed {
		if err := database.Remove(tx, rel); err != nil {
			writer.Close()
			return Refreshed{}, err
		}
		refreshed.Removed++
	}

	for _, entry := range diff.Touched {
		if filesystem.IsSensitivePath(entry.Path) {
			resetPurgeSentinel = true
		}
		if err := writer.StoreManifest(entry); err != nil {
			writer.Close()
			return Refreshed{}, err
		}
	}

	for _, entry := range diff.Added {
		if filesystem.IsSensitivePath(entry.Path) {
			resetPurgeSentinel = true
		}
		stored, skipReason, err := storeCurrentFile(tx, writer, rootPath, entry, nil, true)
		if err != nil {
			writer.Close()
			return Refreshed{}, err
		}
		if stored {
			refreshed.Added++
		} else if skipReason != filesystem.SkipNone {
			refreshed.recordSkip(skipReason)
		}
	}

	for _, changed := range diff.Changed {
		if changed.Removed {
			if err := database.Remove(tx, changed.Record.Path); err != nil {
				writer.Close()
				return Refreshed{}, err
			}
			continue
		}
		if filesystem.IsSensitivePath(changed.Record.Path) {
			resetPurgeSentinel = true
		}
		stored, skipReason, err := storeCurrentFile(tx, writer, rootPath, changed.Record, changed.Content, true)
		if err != nil {
			writer.Close()
			return Refreshed{}, err
		}
		if stored {
			refreshed.Changed++
		} else if skipReason != filesystem.SkipNone {
			refreshed.recordSkip(skipReason)
		}
	}

	if resetPurgeSentinel {
		if err := database.ResetSensitivePurge(tx); err != nil {
			writer.Close()
			return Refreshed{}, err
		}
	}

	if refreshed.Added > 0 || refreshed.Changed > 0 || refreshed.Removed > 0 {
		if err := database.IncrementWritesSinceOptimize(tx); err != nil {
			writer.Close()
			return Refreshed{}, err
		}
	}

	if err := database.SetRootPath(tx, rootPath); err != nil {
		writer.Close()
		return Refreshed{}, err
	}
	skippedJSON := "{}"
	if refreshed.SkippedByReason != nil {
		if b, jerr := json.Marshal(refreshed.SkippedByReason); jerr == nil {
			skippedJSON = string(b)
		}
	}
	if err := database.SetWalkStats(tx, refreshed.IgnoredDirs, skippedJSON); err != nil {
		writer.Close()
		return Refreshed{}, err
	}

	if err := database.StampIndexedAt(tx); err != nil {
		writer.Close()
		return Refreshed{}, err
	}

	if err := writer.Close(); err != nil {
		return Refreshed{}, err
	}

	return refreshed, tx.Commit()
}

// storeCurrentFile reads the file as it is now and stores its chunks plus its
// manifest row. Reading inside the transaction rather than carrying the bytes
// from the walk keeps depth bounded: only the files in the diff are ever in
// memory, and the hash that is stored describes exactly the bytes that were
// chunked. Files whose lines exceed config.MaxLineWidth are recorded in the
// manifest but skipped from chunk storage, returning stored = false.
//
// The size and timestamp come from the walk rather than a second stat. A write
// that lands between the walk and this read leaves the row slightly behind, so
// the next build re-reads the file and finds the same hash; that costs one read
// and never produces a wrong answer.
func storeCurrentFile(tx *sqlx.Tx, writer *database.Writer, rootPath string, entry database.FileRecord, content []byte, replace bool) (bool, filesystem.SkipReason, error) {
	if content == nil {
		path := filepath.Join(rootPath, filepath.FromSlash(entry.Path))

		var err error
		content, err = os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// Removed between the walk and the write: dropping it is the
				// truthful outcome, and it keeps the manifest and chunks aligned.
				return false, filesystem.SkipNone, database.Remove(tx, entry.Path)
			}
			return false, filesystem.SkipNone, fmt.Errorf("failed to read %s: %w", entry.Path, err)
		}

		if int64(len(content)) > config.MaxFileSize {
			return false, filesystem.SkipSize, database.Remove(tx, entry.Path)
		}

		entry.Size = int64(len(content))
		entry.Hash = ContentHash(content)
	}

	lines, ok := PrepareContentForIndex(content, config.MaxLineWidth)
	if !ok {
		if replace {
			if err := database.DeleteChunks(tx, entry.Path); err != nil {
				return false, filesystem.SkipNone, err
			}
		}
		if err := writer.StoreManifest(entry); err != nil {
			return false, filesystem.SkipNone, err
		}
		return false, filesystem.SkipMinified, nil
	}

	if err := writer.StoreLines(entry, lines, replace); err != nil {
		return false, filesystem.SkipNone, err
	}
	return true, filesystem.SkipNone, nil
}
