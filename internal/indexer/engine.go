package indexer

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/internal/filesystem"

	_ "modernc.org/sqlite"
)

// dsn builds the SQLite URI for dbPath. Two settings cannot be expressed by a
// plain filename: a busy timeout on every connection, and BEGIN IMMEDIATE for
// write transactions.
//
// BEGIN IMMEDIATE matters because an FTS5 DELETE reads the index before it
// writes the shadow tables. A deferred transaction therefore opens a read
// snapshot first, and the later upgrade fails with SQLITE_BUSY_SNAPSHOT, which
// the busy handler must not retry. Taking the write lock up front removes that
// window, so concurrent writers queue instead of failing.
func dsn(dbPath string) string {
	// EscapedPath keeps the separators but escapes "?" and "#", which SQLite's
	// URI parser would otherwise read as delimiters.
	escaped := (&url.URL{Path: filepath.ToSlash(dbPath)}).EscapedPath()

	params := url.Values{}
	params.Set("_txlock", "immediate")
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "synchronous(NORMAL)")

	return "file:" + escaped + "?" + params.Encode()
}

// InitDB opens the index database at dbPath and creates the tables when they
// are missing. The path is a parameter because the index belongs to a project
// root, not to whatever directory the process happens to run in. A database
// written by an older schema is emptied here, so nothing that follows has to
// handle a layout it does not understand.
func InitDB(dbPath string) (*sqlx.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create the index directory: %w", err)
		}
	}

	db, err := sqlx.Connect("sqlite", dsn(dbPath))
	if err != nil {
		return nil, err
	}

	// One connection serializes this process' own writers, which is all a single
	// index file can support, and keeps the connection-scoped pragmas in force
	// for every statement.
	db.SetMaxOpenConns(1)

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
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

	writer, err := newChunkWriter(tx)
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
func reindexFile(tx *sqlx.Tx, writer *chunkWriter, rel, path string, ignoredDirs []string) error {
	if filesystem.PathHasIgnoredDir(rel, ignoredDirs) {
		// The file lives under a directory the project ignores, so editing it
		// directly must not smuggle it back into the index.
		return removeFile(tx, rel)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// The file is gone, so dropping its rows is the whole job.
			return removeFile(tx, rel)
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
		return removeFile(tx, rel)
	}

	entry := fileMeta{
		rel:     rel,
		size:    info.Size(),
		mtimeNS: info.ModTime().UnixNano(),
		hash:    ContentHash(content),
	}

	return storeFile(tx, writer, entry, string(content), true)
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
