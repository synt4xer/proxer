package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
)

const (
	metaSchemaVersion  = "schema_version"
	metaChunkSize      = "chunk_size"
	metaOverlapSize    = "overlap_size"
	metaIndexedAt      = "indexed_at"
	metaSensitivePurge = "sensitive_purge"
	metaPurgePending   = "purge_pending"
)

// The index is four stores, not one:
//
//   - chunk_search is the FTS5 table and owns the payload an agent reads.
//   - index_chunks maps a filepath to the chunk rowids stored for it. FTS5 has
//     no secondary index, so "DELETE WHERE filepath = ?" reads the whole table;
//     deleting through this map keeps a single-file reindex proportional to the
//     file, not to the project.
//   - indexed_files is the manifest the delta build diffs the tree against.
//   - meta carries the schema version and the chunking the rows were built with,
//     so a database that no longer matches this binary is rebuilt instead of
//     queried.
var schemaStatements = []string{
	`CREATE VIRTUAL TABLE IF NOT EXISTS chunk_search USING fts5(
		id UNINDEXED,
		filepath,
		start_line UNINDEXED,
		end_line UNINDEXED,
		content,
		tails
	);`,
	`CREATE TABLE IF NOT EXISTS index_chunks (
		rowid INTEGER PRIMARY KEY,
		filepath TEXT NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS idx_index_chunks_filepath ON index_chunks(filepath);`,
	`CREATE TABLE IF NOT EXISTS indexed_files (
		filepath TEXT PRIMARY KEY,
		size INTEGER NOT NULL,
		mtime_ns INTEGER NOT NULL,
		hash TEXT NOT NULL
	);`,
	`CREATE TABLE IF NOT EXISTS meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
}

// getter is satisfied by both *sqlx.DB and *sqlx.Tx, so the meta helpers work
// inside and outside a transaction without a wrapper type.
type getter interface {
	Get(dest any, query string, args ...any) error
}

// metaValue reads one meta row. A missing key is returned as an empty string so
// that callers can treat "not written yet" and "written empty" the same way.
func metaValue(q getter, key string) (string, error) {
	var value string
	err := q.Get(&value, "SELECT value FROM meta WHERE key = ?", key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}

// setMeta writes one meta row inside the caller's transaction, so a schema
// stamp and the rows it describes become visible together.
func setMeta(tx *sqlx.Tx, key, value string) error {
	_, err := tx.Exec(
		"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	if err != nil {
		return fmt.Errorf("failed to record %s: %w", key, err)
	}
	return nil
}

// IndexedAt returns the stored timestamp of the last index change, empty when
// the index has never been built. It is a string rather than a time.Time because
// its only job is to be shown to the caller that asked whether the index moved.
func IndexedAt(db *sqlx.DB) (string, error) {
	return metaValue(db, metaIndexedAt)
}

// StampIndexedAt records that the index changed at this moment, inside the
// caller's transaction so the timestamp cannot describe a write that rolled
// back.
func StampIndexedAt(tx *sqlx.Tx) error {
	return setMeta(tx, metaIndexedAt, time.Now().UTC().Format(time.RFC3339Nano))
}

// PurgePending reports whether sensitive rows were deleted in Stage 1 and
// unallocated DB pages or WAL frames still await Stage 2 reclamation.
func PurgePending(db *sqlx.DB) (bool, error) {
	val, err := metaValue(db, metaPurgePending)
	if err != nil {
		return false, err
	}
	return val == "1", nil
}

// ResetSensitivePurge clears the O(1) purge sentinel inside a write
// transaction so that toggling index_sensitive back to false re-triggers Stage 1.
func ResetSensitivePurge(tx *sqlx.Tx) error {
	return setMeta(tx, metaSensitivePurge, "0")
}

// migrate creates every table the index needs and, when the database was built
// by a different binary, empties it so the next build starts from the tree. A
// pre-manifest database has chunks but no rowid map and no manifest: running a
// delta against it would re-insert rows without dropping the old ones, so
// wiping is the only correct starting point.
func migrate(db *sqlx.DB, chunking Chunking) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`); err != nil {
		return fmt.Errorf("failed to create the meta table: %w", err)
	}

	version, err := metaValue(db, metaSchemaVersion)
	if err != nil {
		return err
	}
	chunkSize, err := metaValue(db, metaChunkSize)
	if err != nil {
		return err
	}
	overlap, err := metaValue(db, metaOverlapSize)
	if err != nil {
		return err
	}

	current := version == SchemaVersion &&
		chunkSize == strconv.Itoa(chunking.Size) &&
		overlap == strconv.Itoa(chunking.Overlap)
	if current {
		for _, statement := range schemaStatements {
			if _, err := db.Exec(statement); err != nil {
				return fmt.Errorf("failed to create the index tables: %w", err)
			}
		}
		return nil
	}

	return reset(db, chunking)
}

// reset drops every stored table, recreates the schema, and stamps the current
// version. Dropping rather than deleting lets schema_version bumps add or
// reshape FTS5 columns cleanly, and keeping the stamp in the same transaction
// ensures an interrupted reset cannot leave a half-cleared index.
func reset(db *sqlx.DB, chunking Chunking) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, statement := range []string{
		"DROP TABLE IF EXISTS chunk_search",
		"DROP TABLE IF EXISTS index_chunks",
		"DROP TABLE IF EXISTS indexed_files",
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("failed to drop a stale index table: %w", err)
		}
	}

	for _, statement := range schemaStatements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("failed to create the index tables: %w", err)
		}
	}

	// indexed_at is cleared with the rows it described: a timestamp from a
	// database that was just emptied would claim an index that does not exist.
	for key, value := range map[string]string{
		metaSchemaVersion:  SchemaVersion,
		metaChunkSize:      strconv.Itoa(chunking.Size),
		metaOverlapSize:    strconv.Itoa(chunking.Overlap),
		metaIndexedAt:      "",
		metaSensitivePurge: "",
		metaPurgePending:   "",
	} {
		if err := setMeta(tx, key, value); err != nil {
			return err
		}
	}

	return tx.Commit()
}
