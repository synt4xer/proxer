package indexer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/filesystem"
)

// Refreshed counts what a delta build changed. It is reported by search so the
// caller can see the index move instead of guessing whether it is current.
type Refreshed struct {
	Added   int `json:"added"`
	Changed int `json:"changed"`
	Removed int `json:"removed"`
}

func (r Refreshed) Empty() bool {
	return r.Added == 0 && r.Changed == 0 && r.Removed == 0
}

// fileMeta is what the manifest remembers about one file: the cheap gate that
// decides whether the file has to be read at all, and the content hash that
// decides whether it has to be re-chunked once it has been read.
type fileMeta struct {
	rel     string
	size    int64
	mtimeNS int64
	hash    string
}

// Diff is the work one delta build has to apply. The four buckets are disjoint:
// a file whose size and mtime are unchanged is in none of them, a file whose
// bytes are provably identical is Touched and only its manifest row is
// refreshed, and a file whose hash moved is Changed and gets re-chunked.
type Diff struct {
	Added   []fileMeta
	Changed []fileMeta
	Touched []fileMeta
	Removed []string
}

func (d *Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Changed) == 0 && len(d.Touched) == 0 && len(d.Removed) == 0
}

// loadManifest reads the whole manifest into memory. Every walk needs it, and
// one row per file is small next to the chunks it describes.
func loadManifest(db *sqlx.DB) (map[string]fileMeta, error) {
	type manifestRow struct {
		Filepath string `db:"filepath"`
		Size     int64  `db:"size"`
		MtimeNS  int64  `db:"mtime_ns"`
		Hash     string `db:"hash"`
	}

	var rows []manifestRow
	if err := db.Select(&rows, "SELECT filepath, size, mtime_ns, hash FROM indexed_files"); err != nil {
		return nil, fmt.Errorf("failed to read the index manifest: %w", err)
	}

	manifest := make(map[string]fileMeta, len(rows))
	for _, row := range rows {
		manifest[row.Filepath] = fileMeta{
			rel:     row.Filepath,
			size:    row.Size,
			mtimeNS: row.MtimeNS,
			hash:    row.Hash,
		}
	}
	return manifest, nil
}

// computeDiff walks the tree without opening a write transaction and compares
// what it finds with the manifest. Reading and hashing happen here, before the
// lock is taken, so a file that turns out to be unchanged costs a stat and
// nothing else.
func computeDiff(db *sqlx.DB, rootPath string, ignoredDirs []string) (*Diff, error) {
	manifest, err := loadManifest(db)
	if err != nil {
		return nil, err
	}

	diff := &Diff{}

	walkErr := filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}

		if entry.IsDir() {
			// The root itself is never skipped: an ignored directory name such
			// as "build" is legitimate when it is the project being indexed.
			if path != rootPath && !filesystem.IsIndexableDir(path, info, ignoredDirs) {
				return filepath.SkipDir
			}
			return nil
		}

		if !filesystem.IsIndexableFile(info) {
			return nil
		}

		rel, err := relativePath(rootPath, path)
		if err != nil {
			return err
		}

		current := fileMeta{rel: rel, size: info.Size(), mtimeNS: info.ModTime().UnixNano()}

		stored, known := manifest[rel]
		delete(manifest, rel)

		if known && stored.size == current.size && stored.mtimeNS == current.mtimeNS {
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
				current.hash = stored.hash
				diff.Changed = append(diff.Changed, current)
				return nil
			}
			// Any other read failure is reported instead of silently keeping a
			// stale row that the caller would read as current.
			return fmt.Errorf("failed to read %s: %w", rel, err)
		}

		current.hash = ContentHash(content)
		if current.hash == stored.hash {
			// Same bytes, new timestamp: only the manifest needs updating.
			diff.Touched = append(diff.Touched, current)
			return nil
		}

		diff.Changed = append(diff.Changed, current)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	for rel := range manifest {
		diff.Removed = append(diff.Removed, rel)
	}

	sort.Slice(diff.Added, func(i, j int) bool { return diff.Added[i].rel < diff.Added[j].rel })
	sort.Slice(diff.Changed, func(i, j int) bool { return diff.Changed[i].rel < diff.Changed[j].rel })
	sort.Slice(diff.Touched, func(i, j int) bool { return diff.Touched[i].rel < diff.Touched[j].rel })
	sort.Strings(diff.Removed)

	return diff, nil
}

// applyDiff writes a computed diff in one transaction. An empty diff never
// reaches this function, which is what keeps a no-change refresh free of the
// write lock a concurrent reader would have to queue behind.
func applyDiff(db *sqlx.DB, rootPath string, diff *Diff) (Refreshed, error) {
	tx, err := db.Beginx()
	if err != nil {
		return Refreshed{}, err
	}
	defer tx.Rollback()

	writer, err := newChunkWriter(tx)
	if err != nil {
		return Refreshed{}, err
	}

	var refreshed Refreshed

	for _, rel := range diff.Removed {
		if err := removeFile(tx, rel); err != nil {
			writer.Close()
			return Refreshed{}, err
		}
		refreshed.Removed++
	}

	for _, entry := range diff.Touched {
		if err := upsertManifest(writer, entry); err != nil {
			writer.Close()
			return Refreshed{}, err
		}
	}

	for _, entry := range diff.Added {
		// An added file is absent from the manifest, so by construction it has
		// no stored chunks: skipping the delete keeps a cold build from paying
		// for a no-op statement per file.
		if err := storeCurrentFile(tx, writer, rootPath, entry, false); err != nil {
			writer.Close()
			return Refreshed{}, err
		}
		refreshed.Added++
	}

	for _, entry := range diff.Changed {
		if err := storeCurrentFile(tx, writer, rootPath, entry, true); err != nil {
			writer.Close()
			return Refreshed{}, err
		}
		refreshed.Changed++
	}

	if err := setMeta(tx, metaIndexedAt, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
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
// chunked.
//
// The size and timestamp come from the walk rather than a second stat. A write
// that lands between the walk and this read leaves the row slightly behind, so
// the next build re-reads the file and finds the same hash; that costs one read
// and never produces a wrong answer.
func storeCurrentFile(tx *sqlx.Tx, writer *chunkWriter, rootPath string, entry fileMeta, replace bool) error {
	path := filepath.Join(rootPath, filepath.FromSlash(entry.rel))

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Removed between the walk and the write: dropping it is the
			// truthful outcome, and it keeps the manifest and chunks aligned.
			return removeFile(tx, entry.rel)
		}
		return fmt.Errorf("failed to read %s: %w", entry.rel, err)
	}

	if int64(len(content)) > config.MaxFileSize {
		return removeFile(tx, entry.rel)
	}

	entry.size = int64(len(content))
	entry.hash = ContentHash(content)

	return storeFile(tx, writer, entry, string(content), replace)
}
