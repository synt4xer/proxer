package database

import (
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var innerWindow = Chunking{Size: 4, Overlap: 1}

func innerDB(t *testing.T) *sqlx.DB {
	t.Helper()

	db, err := Open(filepath.Join(t.TempDir(), "index.db"), OpenOptions{Chunking: innerWindow})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	return db
}

// deadTx returns a transaction that is already finished, so every statement
// issued through it fails with sql.ErrTxDone. The writer's error paths are
// otherwise unreachable, because a healthy transaction accepts every statement.
func deadTx(t *testing.T, db *sqlx.DB) *sqlx.Tx {
	t.Helper()

	tx, err := db.Beginx()
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	return tx
}

func TestNewWriterFailsWhenStatementsCannotBePrepared(t *testing.T) {
	db := innerDB(t)

	writer, err := NewWriter(deadTx(t, db), innerWindow)

	require.Error(t, err)
	assert.Nil(t, writer, "a writer that cannot prepare its statements must not be returned")
}

func TestWriterCloseIsIdempotent(t *testing.T) {
	db := innerDB(t)
	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()

	writer, err := NewWriter(tx, innerWindow)
	require.NoError(t, err)

	require.NoError(t, writer.Close())
	assert.NoError(t, writer.Close(), "Close must be safe to call twice so deferred cleanup cannot fail loudly")
}

func TestWriterCloseToleratesNilStatements(t *testing.T) {
	require.NoError(t, (&Writer{}).Close(), "a zero-value writer has nothing to close")
}

func TestWriteLinesRejectsNonPositiveStep(t *testing.T) {
	db := innerDB(t)
	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()

	writer, err := NewWriter(tx, Chunking{Size: 2, Overlap: 2})
	require.NoError(t, err)
	defer writer.Close()

	err = writer.writeLines("a.go", []string{"package a"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must exceed overlap")
}

func TestStoreLinesPropagatesDeleteFailure(t *testing.T) {
	db := innerDB(t)
	tx, err := db.Beginx()
	require.NoError(t, err)

	writer, err := NewWriter(tx, innerWindow)
	require.NoError(t, err)
	defer writer.Close()

	// Prepare the statements on a live transaction, then finish it so the
	// delete inside StoreLines is the statement that fails.
	require.NoError(t, tx.Rollback())

	err = writer.StoreLines(FileRecord{Path: "a.go"}, []string{"package a"}, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to drop stale chunks")
}

func TestStoreManifestReportsFailure(t *testing.T) {
	db := innerDB(t)
	tx, err := db.Beginx()
	require.NoError(t, err)
	writer, err := NewWriter(tx, innerWindow)
	require.NoError(t, err)
	defer writer.Close()

	require.NoError(t, tx.Rollback())

	err = writer.StoreManifest(FileRecord{Path: "a.go"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to record a.go in the manifest")
}

func TestWriteLinesReportsInsertFailure(t *testing.T) {
	db := innerDB(t)
	tx, err := db.Beginx()
	require.NoError(t, err)
	writer, err := NewWriter(tx, innerWindow)
	require.NoError(t, err)
	defer writer.Close()

	require.NoError(t, tx.Rollback())

	err = writer.writeLines("a.go", []string{"package a"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to store chunk")
}

func TestDeleteChunksReportsFailure(t *testing.T) {
	db := innerDB(t)

	err := DeleteChunks(deadTx(t, db), "a.go")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to drop stale chunks for a.go")
}

func TestRemoveReportsChunkFailure(t *testing.T) {
	db := innerDB(t)

	err := Remove(deadTx(t, db), "a.go")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to drop stale chunks")
}

func TestRemoveDropsManifestAndChunks(t *testing.T) {
	db := innerDB(t)

	tx, err := db.Beginx()
	require.NoError(t, err)
	writer, err := NewWriter(tx, innerWindow)
	require.NoError(t, err)
	require.NoError(t, writer.Store(FileRecord{Path: "keep.go", Hash: "h"}, "package keep", false))
	require.NoError(t, writer.Close())
	require.NoError(t, tx.Commit())

	tx2, err := db.Beginx()
	require.NoError(t, err)
	require.NoError(t, Remove(tx2, "keep.go"))
	require.NoError(t, tx2.Commit())

	var files, chunks int
	require.NoError(t, db.Get(&files, "SELECT count(*) FROM indexed_files"))
	require.NoError(t, db.Get(&chunks, "SELECT count(*) FROM index_chunks"))
	assert.Zero(t, files, "the manifest row must be gone")
	assert.Zero(t, chunks, "the chunk map rows must be gone")
}

// TestStoreLinesReplaceFalseKeepsPriorRows pins the contract callers rely on
// when a file is brand new: skipping the delete must not disturb other files.
func TestStoreLinesReplaceFalseKeepsOtherFiles(t *testing.T) {
	db := innerDB(t)

	tx, err := db.Beginx()
	require.NoError(t, err)
	writer, err := NewWriter(tx, innerWindow)
	require.NoError(t, err)
	require.NoError(t, writer.Store(FileRecord{Path: "a.go", Hash: "a"}, "package a", false))
	require.NoError(t, writer.Store(FileRecord{Path: "b.go", Hash: "b"}, "package b", false))
	require.NoError(t, writer.Close())
	require.NoError(t, tx.Commit())

	var files int
	require.NoError(t, db.Get(&files, "SELECT count(*) FROM indexed_files"))
	assert.Equal(t, 2, files)
}
