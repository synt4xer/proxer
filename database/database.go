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
	"strconv"

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

// OpenReadOnly opens an existing index database in read-only mode without
// creating directories, setting WAL mode, executing DDL, or modifying file
// permissions. It tries ?mode=ro first and falls back to ?mode=ro&immutable=1
// when a read-only mount prevents SQLite from opening WAL/SHM files.
func OpenReadOnly(dbPath string, chunking Chunking) (*sqlx.DB, error) {
	if !IsReadableFile(dbPath) {
		return nil, fmt.Errorf("index database %q is not readable; set PROXER_DB=<writable-path> to use a writable database location", dbPath)
	}

	var lastErr error
	for _, immutable := range []bool{false, true} {
		db, err := sqlx.Connect("sqlite", readOnlyDSN(dbPath, immutable))
		if err != nil {
			lastErr = err
			if !immutable && IsReadOnlyError(err) {
				continue
			}
			return nil, fmt.Errorf("failed to open read-only index database %q: %w", dbPath, err)
		}
		db.SetMaxOpenConns(1)
		if err := validateReadOnlySchema(db, chunking); err != nil {
			db.Close()
			lastErr = err
			if !immutable && IsReadOnlyError(err) {
				continue
			}
			return nil, err
		}
		return db, nil
	}
	return nil, fmt.Errorf("failed to open read-only index database %q: %w", dbPath, lastErr)
}

func validateReadOnlySchema(db *sqlx.DB, chunking Chunking) error {
	version, err := metaValue(db, metaSchemaVersion)
	if err != nil {
		return fmt.Errorf("failed to read schema_version from read-only database: %w", err)
	}
	chunkSize, err := metaValue(db, metaChunkSize)
	if err != nil {
		return fmt.Errorf("failed to read chunk_size from read-only database: %w", err)
	}
	overlap, err := metaValue(db, metaOverlapSize)
	if err != nil {
		return fmt.Errorf("failed to read overlap_size from read-only database: %w", err)
	}
	if version != SchemaVersion ||
		chunkSize != strconv.Itoa(chunking.Size) ||
		overlap != strconv.Itoa(chunking.Overlap) {
		return fmt.Errorf("read-only database schema (%q, %s/%s) does not match this binary (%q, %d/%d)",
			version, chunkSize, overlap, SchemaVersion, chunking.Size, chunking.Overlap)
	}
	return nil
}

// IsReadableFile reports whether dbPath exists and can be opened for reading by
// the current UID.
func IsReadableFile(dbPath string) bool {
	info, err := os.Stat(dbPath)
	if err != nil || info.IsDir() {
		return false
	}
	f, err := os.Open(dbPath)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// ReadRecordedRoot reads meta.root_path from dbPath in read-only mode without
// requiring schema version parity, so orphan detection works across versions.
func ReadRecordedRoot(dbPath string) (string, error) {
	if !IsReadableFile(dbPath) {
		return "", os.ErrNotExist
	}
	db, err := sqlx.Connect("sqlite", readOnlyDSN(dbPath, true))
	if err != nil {
		return "", err
	}
	defer db.Close()
	return metaValue(db, metaRootPath)
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

func readOnlyDSN(dbPath string, immutable bool) string {
	escaped := (&url.URL{Path: filepath.ToSlash(dbPath)}).EscapedPath()
	params := url.Values{}
	params.Set("mode", "ro")
	if immutable {
		params.Set("immutable", "1")
	}
	return "file:" + escaped + "?" + params.Encode()
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
