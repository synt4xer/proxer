package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/filesystem"
)

// Chunking is the window this build chunks with. It is handed to database.Open
// so a database built with another window is rebuilt instead of served.
func Chunking() database.Chunking {
	return database.Chunking{Size: config.ChunkSize, Overlap: config.OverlapSize}
}

// IndexProject brings the index in line with the tree. It is a delta build:
// files whose size and modification time still match the manifest are skipped,
// files whose bytes hash the same are only re-stamped, and rows for paths the
// walk no longer sees are dropped. A build with nothing to do returns a zero
// Refreshed and never opens a write transaction, which is what makes it cheap
// enough to run before a search.
func IndexProject(db *sqlx.DB, rootPath string, ignoredDirs []string) (Refreshed, error) {
	diff, err := computeDiff(db, rootPath, ignoredDirs)
	if err != nil {
		return Refreshed{}, err
	}

	if diff.Empty() {
		return Refreshed{}, nil
	}

	return applyDiff(db, rootPath, diff)
}

// ReindexFile replaces the stored chunks and manifest row of a single file.
// Editing a file only shifts line numbers inside that file, so every other row
// in the index stays valid and never has to be rebuilt.
func ReindexFile(db *sqlx.DB, rootPath, path string, ignoredDirs []string) error {
	rel, err := relativePath(rootPath, path)
	if err != nil {
		return err
	}

	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	writer, err := database.NewWriter(tx, Chunking())
	if err != nil {
		return err
	}

	if err := reindexFile(tx, writer, rel, path, ignoredDirs); err != nil {
		writer.Close()
		return err
	}

	// The chunk rows and the manifest row must land together: a manifest that
	// disagrees with the chunks makes the next delta re-read or skip a file
	// that has not actually moved.
	if err := writer.Close(); err != nil {
		return err
	}

	return tx.Commit()
}

// reindexFile is the body of ReindexFile, split out so every early return still
// goes through the single commit above.
func reindexFile(tx *sqlx.Tx, writer *database.Writer, rel, path string, ignoredDirs []string) error {
	if filesystem.PathHasIgnoredDir(rel, ignoredDirs) {
		// The file lives under a directory the project ignores, so editing it
		// directly must not smuggle it back into the index.
		return database.Remove(tx, rel)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// The file is gone, so dropping its rows is the whole job.
			return database.Remove(tx, rel)
		}
		return fmt.Errorf("failed to read %s: %w", rel, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", rel, err)
	}
	if !filesystem.IsIndexableFile(info) {
		// The file grew past the size limit or lost an indexable extension:
		// it must no longer be reachable through search.
		return database.Remove(tx, rel)
	}

	entry := database.FileRecord{
		Path:    rel,
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
		Hash:    ContentHash(content),
	}

	return writer.Store(entry, string(content), true)
}

// relativePath keys a file by its location inside the project. Absolute keys
// would break as soon as the project moves, and mixed absolute and relative
// keys would prevent single-file reindexing from finding its own rows.
func relativePath(rootPath, path string) (string, error) {
	if rootPath == "" {
		rootPath = "."
	}

	rel, err := filepath.Rel(rootPath, path)
	if err != nil {
		return "", fmt.Errorf("failed to relativize %s against %s: %w", path, rootPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// A "../" key describes a file the next walk of the root will never
		// reach, so the row would survive every rebuild that follows.
		return "", fmt.Errorf("%s is outside the project root %s", path, rootPath)
	}
	return filepath.ToSlash(rel), nil
}
