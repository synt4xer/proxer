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

type IndexOptions struct {
	IgnoredDirs    []string
	IndexSensitive bool
}

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
	return IndexProjectWithOptions(db, rootPath, IndexOptions{IgnoredDirs: ignoredDirs})
}

func IndexProjectWithOptions(db *sqlx.DB, rootPath string, opts IndexOptions) (Refreshed, error) {
	diff, err := computeDiff(db, rootPath, opts)
	if err != nil {
		return Refreshed{}, err
	}

	if diff.Empty() {
		return Refreshed{}, nil
	}

	return applyDiff(db, rootPath, diff, opts)
}

// ReindexFile replaces the stored chunks and manifest row of a single file.
// Editing a file only shifts line numbers inside that file, so every other row
// in the index stays valid and never has to be rebuilt.
func ReindexFile(db *sqlx.DB, rootPath, path string, ignoredDirs []string) error {
	return reindexFileWithContent(db, rootPath, path, nil, false, IndexOptions{IgnoredDirs: ignoredDirs})
}

// ReindexFileContent replaces the stored chunks and manifest row of a single
// file using already-known file bytes so callers that just wrote the file do
// not re-read it from disk.
func ReindexFileContent(db *sqlx.DB, rootPath, path string, content []byte, ignoredDirs []string) error {
	return reindexFileWithContent(db, rootPath, path, content, true, IndexOptions{IgnoredDirs: ignoredDirs})
}

func ReindexFileContentWithOptions(db *sqlx.DB, rootPath, path string, content []byte, opts IndexOptions) error {
	return reindexFileWithContent(db, rootPath, path, content, true, opts)
}

func reindexFileWithContent(db *sqlx.DB, rootPath, path string, content []byte, hasContent bool, opts IndexOptions) error {
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

	if err := reindexFile(tx, writer, rel, path, content, hasContent, opts); err != nil {
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
func reindexFile(tx *sqlx.Tx, writer *database.Writer, rel, path string, content []byte, hasContent bool, opts IndexOptions) error {
	if filesystem.PathHasIgnoredDir(rel, opts.IgnoredDirs) {
		// The file lives under a directory the project ignores, so editing it
		// directly must not smuggle it back into the index.
		return database.Remove(tx, rel)
	}

	if !hasContent {
		var err error
		content, err = os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// The file is gone, so dropping its rows is the whole job.
				return database.Remove(tx, rel)
			}
			return fmt.Errorf("failed to read %s: %w", rel, err)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return database.Remove(tx, rel)
		}
		return fmt.Errorf("failed to stat %s: %w", rel, err)
	}
	if ok, _ := filesystem.IsIndexableFile(info, opts.IndexSensitive); !ok {
		// The file grew past the size limit, is sensitive, or lost an indexable
		// extension: it must no longer be reachable through search.
		return database.Remove(tx, rel)
	}

	if opts.IndexSensitive || filesystem.IsSensitivePath(rel) {
		if err := database.ResetSensitivePurge(tx); err != nil {
			return err
		}
	}

	entry := database.FileRecord{
		Path:    rel,
		Size:    info.Size(),
		MTimeNS: info.ModTime().UnixNano(),
		Hash:    ContentHash(content),
	}

	lines, ok := PrepareContentForIndex(content, config.MaxLineWidth)
	if !ok {
		if err := database.DeleteChunks(tx, rel); err != nil {
			return err
		}
		return writer.StoreManifest(entry)
	}

	return writer.StoreLines(entry, lines, true)
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
