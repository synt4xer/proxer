package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPurgeSensitiveIfNeededLeavesARecordThatStage2Ran pins the sentinel
// contract: once Stage 1 runs, reopening must not rescan the manifest.
func TestPurgeSensitiveIfNeededIsIdempotent(t *testing.T) {
	db := innerDB(t)

	tx, err := db.Beginx()
	require.NoError(t, err)
	writer, err := NewWriter(tx, innerWindow)
	require.NoError(t, err)
	require.NoError(t, writer.Store(FileRecord{Path: ".env", Hash: "h"}, "SECRET=1", false))
	require.NoError(t, writer.Close())
	// An index written before the purge contract has no sentinel yet.
	require.NoError(t, ResetSensitivePurge(tx))
	require.NoError(t, tx.Commit())

	require.NoError(t, purgeSensitiveIfNeeded(db, false))

	var files int
	require.NoError(t, db.Get(&files, "SELECT count(*) FROM indexed_files"))
	assert.Zero(t, files, "the sensitive file must leave the manifest")

	val, err := metaValue(db, metaSensitivePurge)
	require.NoError(t, err)
	assert.Equal(t, "1", val, "the sentinel must be recorded so the scan is O(1) next time")
}

func mustTx(t *testing.T, db *sqlx.DB) *sqlx.Tx {
	t.Helper()
	tx, err := db.Beginx()
	require.NoError(t, err)
	return tx
}

func TestPurgeSensitiveIfNeededSkipsWhenAlreadyPurged(t *testing.T) {
	db := innerDB(t)
	tx := mustTx(t, db)
	require.NoError(t, setMeta(tx, metaSensitivePurge, "1"))
	require.NoError(t, tx.Commit())

	require.NoError(t, purgeSensitiveIfNeeded(db, false))

	val, err := metaValue(db, metaSensitivePurge)
	require.NoError(t, err)
	assert.Equal(t, "1", val)
}

func TestPurgeSensitiveIfNeededNoopWhenIndexingSensitive(t *testing.T) {
	db := innerDB(t)
	tx := mustTx(t, db)
	require.NoError(t, ResetSensitivePurge(tx))
	require.NoError(t, tx.Commit())

	require.NoError(t, purgeSensitiveIfNeeded(db, true))

	val, err := metaValue(db, metaSensitivePurge)
	require.NoError(t, err)
	assert.Equal(t, "0", val, "opting into sensitive indexing must not claim the purge already ran")
}

func TestPurgeSensitiveIfNeededReportsBrokenMeta(t *testing.T) {
	db := brokenDB(t)

	err := purgeSensitiveIfNeeded(db, false)

	require.Error(t, err)
}

func TestScrubPurgedDataNoopWhenNotPending(t *testing.T) {
	db := innerDB(t)
	tx := mustTx(t, db)
	require.NoError(t, setMeta(tx, metaPurgePending, "0"))
	require.NoError(t, tx.Commit())

	res, err := ScrubPurgedData(db, filepath.Join(t.TempDir(), "index.db"))

	require.NoError(t, err)
	assert.False(t, res.Attempted, "a clean index needs no disk scrub")
	assert.False(t, res.PurgePending)
}

func TestScrubPurgedDataWarnsWhenOptimizeFails(t *testing.T) {
	db := innerDB(t)
	tx := mustTx(t, db)
	require.NoError(t, setMeta(tx, metaPurgePending, "1"))
	require.NoError(t, tx.Commit())

	_, err := db.Exec("DROP TABLE chunk_search")
	require.NoError(t, err)

	res, err := ScrubPurgedData(db, "")

	require.NoError(t, err)
	assert.True(t, res.PurgePending, "the pending flag must survive a failed scrub")
	assert.NotEmpty(t, res.Warning, "a failed optimize must be reported instead of silently ignored")
	assert.False(t, res.Completed)
}

func TestScrubPurgedDataCompletes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	db, err := Open(dbPath, OpenOptions{Chunking: innerWindow})
	require.NoError(t, err)
	defer db.Close()

	tx := mustTx(t, db)
	require.NoError(t, setMeta(tx, metaPurgePending, "1"))
	require.NoError(t, tx.Commit())

	res, err := ScrubPurgedData(db, dbPath)

	require.NoError(t, err)
	assert.True(t, res.Attempted)
	assert.False(t, res.PurgePending, "a successful scrub clears the pending flag")
	assert.True(t, res.Completed)

	pending, err := PurgePending(db)
	require.NoError(t, err)
	assert.False(t, pending)
}

func TestScrubPurgedDataReportsBrokenMeta(t *testing.T) {
	db := brokenDB(t)

	_, err := ScrubPurgedData(db, "")

	require.Error(t, err)
}

func TestMaintainReportsBrokenMeta(t *testing.T) {
	db := brokenDB(t)

	_, err := Maintain(db, "", MaintainOptions{})

	require.Error(t, err)
}

func TestMaintainWarnsWhenOptimizeFails(t *testing.T) {
	db := innerDB(t)
	_, err := db.Exec("DROP TABLE chunk_search")
	require.NoError(t, err)

	res, err := Maintain(db, "", MaintainOptions{ForceVacuum: true})

	require.NoError(t, err)
	assert.NotEmpty(t, res.Warning)
	assert.False(t, res.Optimized)
}

func TestMaintainResetsWriteCounter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	db, err := Open(dbPath, OpenOptions{Chunking: innerWindow})
	require.NoError(t, err)
	defer db.Close()

	tx := mustTx(t, db)
	require.NoError(t, IncrementWritesSinceOptimize(tx))
	require.NoError(t, tx.Commit())

	res, err := Maintain(db, dbPath, MaintainOptions{HadDiff: true})

	require.NoError(t, err)
	assert.True(t, res.Optimized)

	writes, err := WritesSinceOptimize(db)
	require.NoError(t, err)
	assert.Zero(t, writes, "a completed optimize must reset the counter")
}

func TestMaintainSkipsWorkOnACleanIndex(t *testing.T) {
	db := innerDB(t)

	res, err := Maintain(db, "", MaintainOptions{})

	require.NoError(t, err)
	assert.False(t, res.Optimized, "a clean no-op run must skip FTS5 optimize")
	assert.False(t, res.Vacuumed)
}

func TestChmodDBFilesTightensSidecars(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}

	require.NoError(t, ChmodDBFiles(dbPath))

	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "%s must be tightened", filepath.Base(p))
	}
}
