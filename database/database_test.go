package database_test

import (
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
)

var window = database.Chunking{Size: 4, Overlap: 1}

func open(t *testing.T, path string, chunking database.Chunking) *sqlx.DB {
	t.Helper()

	db, err := database.Open(path, chunking)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	return db
}

// store writes one file through the same path the indexer uses, so the test
// covers the chunk rows, the rowid map, and the manifest together.
func store(t *testing.T, db *sqlx.DB, chunking database.Chunking, rec database.FileRecord, content string, replace bool) {
	t.Helper()

	tx, err := db.Beginx()
	require.NoError(t, err)
	t.Cleanup(func() { tx.Rollback() })

	writer, err := database.NewWriter(tx, chunking)
	require.NoError(t, err)

	require.NoError(t, writer.Store(rec, content, replace))
	require.NoError(t, writer.Close())
	require.NoError(t, tx.Commit())
}

func count(t *testing.T, db *sqlx.DB, query string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t, db.Get(&n, query, args...))
	return n
}

func TestOpenStampsAnEmptySchema(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "nested", "index.db"), window)

	for _, table := range []string{"chunk_search", "index_chunks", "indexed_files", "meta"} {
		assert.Equal(t, 1, count(t, db, "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", table), "missing table %s", table)
	}

	var version string
	require.NoError(t, db.Get(&version, "SELECT value FROM meta WHERE key = 'schema_version'"))
	assert.Equal(t, database.SchemaVersion, version)

	indexedAt, err := database.IndexedAt(db)
	require.NoError(t, err)
	assert.Empty(t, indexedAt, "a database with no rows has no index timestamp")
}

func TestOpenRebuildsWhenTheChunkWindowChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	first := open(t, path, window)
	store(t, first, window, database.FileRecord{Path: "a.go", Size: 8, Hash: "h"}, "one\ntwo\n", false)
	tx, err := first.Beginx()
	require.NoError(t, err)
	require.NoError(t, database.StampIndexedAt(tx))
	require.NoError(t, tx.Commit())

	// Closing releases the write lock the single connection holds.
	require.NoError(t, first.Close())

	second := open(t, path, database.Chunking{Size: 60, Overlap: 15})

	assert.Zero(t, count(t, second, "SELECT COUNT(*) FROM chunk_search"))
	assert.Zero(t, count(t, second, "SELECT COUNT(*) FROM index_chunks"))
	assert.Zero(t, count(t, second, "SELECT COUNT(*) FROM indexed_files"))

	var chunkSize, overlap string
	require.NoError(t, second.Get(&chunkSize, "SELECT value FROM meta WHERE key = 'chunk_size'"))
	require.NoError(t, second.Get(&overlap, "SELECT value FROM meta WHERE key = 'overlap_size'"))
	assert.Equal(t, "60", chunkSize)
	assert.Equal(t, "15", overlap)
}

func TestStoreReplacesChunksWithoutOrphanRows(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "index.db"), window)

	// Long enough to chunk more than once with a four-line window.
	before := "one\ntwo\nthree\nfour\nfive\nsix\nseven\n"
	store(t, db, window, database.FileRecord{Path: "a.go", Size: int64(len(before)), Hash: "before"}, before, false)

	chunks := count(t, db, "SELECT COUNT(*) FROM chunk_search")
	require.Greater(t, chunks, 1, "the fixture must span several chunks")

	assert.Equal(t, chunks, count(t, db, "SELECT COUNT(*) FROM index_chunks"),
		"every stored chunk must be reachable through the rowid map")

	store(t, db, window, database.FileRecord{Path: "a.go", Size: 8, Hash: "after"}, "after\n", true)

	assert.Equal(t, 1, count(t, db, "SELECT COUNT(*) FROM chunk_search"), "the old chunks must be gone")
	assert.Equal(t, 1, count(t, db, "SELECT COUNT(*) FROM index_chunks"))
	assert.Equal(t, 1, count(t, db, "SELECT COUNT(*) FROM indexed_files"), "a replace must not duplicate the manifest row")

	var content string
	require.NoError(t, db.Get(&content, "SELECT content FROM chunk_search WHERE filepath = ?", "a.go"))
	assert.Contains(t, content, "after")
}

func TestRemoveDropsBothStores(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "index.db"), window)

	store(t, db, window, database.FileRecord{Path: "a.go", Size: 8, Hash: "h"}, "one\ntwo\n", false)
	store(t, db, window, database.FileRecord{Path: "b.go", Size: 8, Hash: "h"}, "three\nfour\n", false)

	tx, err := db.Beginx()
	require.NoError(t, err)
	require.NoError(t, database.Remove(tx, "a.go"))
	require.NoError(t, tx.Commit())

	assert.Zero(t, count(t, db, "SELECT COUNT(*) FROM chunk_search WHERE filepath = 'a.go'"))
	assert.Zero(t, count(t, db, "SELECT COUNT(*) FROM index_chunks WHERE filepath = 'a.go'"))
	assert.Zero(t, count(t, db, "SELECT COUNT(*) FROM indexed_files WHERE filepath = 'a.go'"))

	manifest, err := database.Files(db)
	require.NoError(t, err)
	assert.Len(t, manifest, 1, "the surviving file must still be in the manifest")
	assert.Equal(t, database.FileRecord{Path: "b.go", Size: 8, Hash: "h"}, manifest["b.go"])
}
