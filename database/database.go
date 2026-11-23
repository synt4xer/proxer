// Package database owns the SQLite store behind the index: the connection, the
// schema stamp, the chunk rows, and the file manifest. It knows nothing about
// walking a tree; the indexing decisions live in internal/indexer, which hands
// this package the records it should keep.
package database

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/jmoiron/sqlx"

	_ "modernc.org/sqlite"
)

// SchemaVersion identifies the on-disk layout. Bump it whenever the tables, the
// chunker, or the meaning of a stored row changes: a database stamped with
// another value is cleared and rebuilt from the tree, which is cheaper and
// safer than migrating rows whose provenance is unknown.
const SchemaVersion = "2"

// Chunking is the window the stored chunks were built with. It is recorded in
// the meta table because a database chunked differently cannot answer the same
// queries: Open clears such a database instead of serving it, and the caller
// therefore has to state the window it is about to use.
type Chunking struct {
	Size    int
	Overlap int
}

// OpenOptions configures database.Open.
type OpenOptions struct {
	Chunking       Chunking
	IndexSensitive bool
}

// Open opens the index database at dbPath and brings the schema in line with
// this binary. The path is a parameter because the index belongs to a project
// root, not to whatever directory the process happens to run in. A database
// written by an older schema or another chunk window is emptied here, so
// nothing that follows has to handle a layout it does not understand.
func Open(dbPath string, opts OpenOptions) (*sqlx.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			if IsReadOnlyError(err) {
				return nil, fmt.Errorf("failed to create the index directory %q (%w); set PROXER_DB=<writable-path> to use a writable database location", dir, err)
			}
			return nil, fmt.Errorf("failed to create the index directory: %w", err)
		}
	}

	db, err := sqlx.Connect("sqlite", dsn(dbPath))
	if err != nil {
		if IsReadOnlyError(err) || isUnreadableExistingDB(dbPath) {
			return nil, fmt.Errorf("failed to open index database %q (%w); set PROXER_DB=<writable-path> to use a writable database location", dbPath, err)
		}
		return nil, err
	}

	// One connection serializes this process' own writers, which is all a single
	// index file can support, and keeps the connection-scoped pragmas in force
	// for every statement.
	db.SetMaxOpenConns(1)

	if err := migrate(db, opts.Chunking); err != nil {
		db.Close()
		if IsReadOnlyError(err) {
			return nil, fmt.Errorf("index database %q is not writable (%w); set PROXER_DB=<writable-path> to use a writable database location", dbPath, err)
		}
		return nil, err
	}

	if err := purgeSensitiveIfNeeded(db, opts.IndexSensitive); err != nil {
		db.Close()
		if IsReadOnlyError(err) {
			return nil, fmt.Errorf("index database %q is not writable (%w); set PROXER_DB=<writable-path> to use a writable database location", dbPath, err)
		}
		return nil, err
	}

	if err := ChmodDBFiles(dbPath); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func isUnreadableExistingDB(dbPath string) bool {
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	if err != nil {
		return os.IsPermission(err) || IsReadOnlyError(err)
	}
	f.Close()
	return false
}

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
