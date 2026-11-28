package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// brokenDB opens a real index and drops `meta`, so every metadata accessor
// reports an error instead of falling through its "missing key" branch.
func brokenDB(t *testing.T) *sqlx.DB {
	t.Helper()

	db := innerDB(t)
	_, err := db.Exec("DROP TABLE meta")
	require.NoError(t, err)
	return db
}

func TestMetaValueReportsQueryFailure(t *testing.T) {
	db := brokenDB(t)

	_, err := metaValue(db, metaIndexedAt)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "meta")
}

func TestMetaValueTreatsMissingRowAsEmpty(t *testing.T) {
	db := innerDB(t)

	val, err := metaValue(db, "absent-key")

	require.NoError(t, err)
	assert.Empty(t, val, "a key that was never written reads as empty, not as an error")
}

func TestSetMetaReportsFailure(t *testing.T) {
	db := brokenDB(t)
	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()

	err = setMeta(tx, metaRootPath, "/tmp")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to record")
}

func TestPurgePendingReportsFailure(t *testing.T) {
	db := brokenDB(t)

	_, err := PurgePending(db)

	require.Error(t, err)
}

func TestWritesSinceOptimizeReportsFailure(t *testing.T) {
	db := brokenDB(t)

	_, err := WritesSinceOptimize(db)

	require.Error(t, err)
}

func TestWritesSinceOptimizeIgnoresUnparsableValue(t *testing.T) {
	db := innerDB(t)

	tx, err := db.Beginx()
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		metaWritesSinceOptimize, "not-a-number")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	n, err := WritesSinceOptimize(db)

	require.NoError(t, err)
	assert.Zero(t, n, "a corrupt counter must read as zero instead of failing the caller")
}

func TestReadStatusMetaReportsFailure(t *testing.T) {
	db := brokenDB(t)

	_, err := ReadStatusMeta(db)

	require.Error(t, err)
}

func TestSetWalkStatsReportsFailure(t *testing.T) {
	db := brokenDB(t)
	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()

	err = SetWalkStats(tx, 3, "{}")

	require.Error(t, err)
}

func TestIncrementWritesSinceOptimizeReportsFailure(t *testing.T) {
	db := brokenDB(t)
	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()

	err = IncrementWritesSinceOptimize(tx)

	require.Error(t, err)
}

func TestCheckWritableRejectsAClosedPool(t *testing.T) {
	db := innerDB(t)
	require.NoError(t, db.Close())

	err := CheckWritable(context.Background(), db)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to acquire database connection")
}

func TestCheckWritableRejectsACancelledContext(t *testing.T) {
	db := innerDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CheckWritable(ctx, db)

	require.Error(t, err)
}

func TestCheckWritableAcceptsAWritableIndex(t *testing.T) {
	db := innerDB(t)

	assert.NoError(t, CheckWritable(context.Background(), db))
}

func TestChmodDBFilesToleratesMissingWALSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	require.NoError(t, os.WriteFile(dbPath, []byte("x"), 0o644))

	err := ChmodDBFiles(dbPath)

	require.NoError(t, err, "a database with no -wal/-shm sidecar is normal and must not fail")
	info, err := os.Stat(dbPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestIsReadOnlyErrorClassifiers(t *testing.T) {
	assert.False(t, IsReadOnlyError(nil))
	assert.True(t, IsReadOnlyError(errors.New("attempt to write a readonly database")))
	assert.True(t, IsReadOnlyError(errors.New("SQLITE_READONLY")))
	assert.False(t, IsReadOnlyError(errors.New("no such table")))
}

func TestIsReadableFile(t *testing.T) {
	dir := t.TempDir()

	assert.False(t, IsReadableFile(filepath.Join(dir, "missing")))
	assert.False(t, IsReadableFile(dir), "a directory is not a readable database")
}
