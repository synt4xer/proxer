package database_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
)

func TestReadRecordedRootAndStatusMeta(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	_, err := database.ReadRecordedRoot(missing)
	require.ErrorIs(t, err, os.ErrNotExist)

	notSQLite := filepath.Join(t.TempDir(), "bad.db")
	require.NoError(t, os.WriteFile(notSQLite, []byte("not a sqlite database"), 0o600))
	_, err = database.ReadRecordedRoot(notSQLite)
	require.Error(t, err)

	dbPath := filepath.Join(t.TempDir(), "index.db")
	db := open(t, dbPath, window)
	store(t, db, window, database.FileRecord{Path: "a.go", Size: 12, Hash: "h1"}, "package a\n", false)

	tx, err := db.Beginx()
	require.NoError(t, err)
	require.NoError(t, database.SetRootPath(tx, "/workspace/project"))
	require.NoError(t, database.SetWalkStats(tx, 3, `{"minified":2}`))
	require.NoError(t, database.StampIndexedAt(tx))
	require.NoError(t, tx.Commit())

	meta, err := database.ReadStatusMeta(db)
	require.NoError(t, err)
	assert.Equal(t, "/workspace/project", meta.RootPath)
	assert.Equal(t, database.SchemaVersion, meta.SchemaVersion)
	assert.NotEmpty(t, meta.IndexedAt)
	assert.False(t, meta.PurgePending)
	assert.Equal(t, 1, meta.FilesIndexed)
	assert.Equal(t, 1, meta.ChunksIndexed)
	assert.Equal(t, 3, meta.LastIgnoredDirs)
	assert.Equal(t, `{"minified":2}`, meta.LastSkippedJSON)

	require.NoError(t, db.Close())

	recorded, err := database.ReadRecordedRoot(dbPath)
	require.NoError(t, err)
	assert.Equal(t, "/workspace/project", recorded)
}

func TestOpenReadOnlySchemaValidation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	db := open(t, dbPath, window)
	require.NoError(t, db.Close())

	_, err := database.OpenReadOnly(filepath.Join(t.TempDir(), "missing.db"), window)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not readable")

	_, err = database.OpenReadOnly(dbPath, database.Chunking{Size: 99, Overlap: 5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")

	roDB, err := database.OpenReadOnly(dbPath, window)
	require.NoError(t, err)
	defer roDB.Close()

	err = database.CheckWritable(context.Background(), roDB)
	require.Error(t, err)
	assert.True(t, database.IsReadOnlyError(err))
}

func TestOpenReadOnlyDirAndFileErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores chmod read-only restrictions")
	}

	roDir := filepath.Join(t.TempDir(), "rodir")
	require.NoError(t, os.MkdirAll(roDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

	_, err := database.Open(filepath.Join(roDir, "sub", "index.db"), database.OpenOptions{Chunking: window})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PROXER_DB")

	dbPath := filepath.Join(t.TempDir(), "locked.db")
	db := open(t, dbPath, window)
	require.NoError(t, db.Close())

	require.NoError(t, os.Chmod(dbPath, 0o400))
	t.Cleanup(func() { _ = os.Chmod(dbPath, 0o600) })

	_, err = database.Open(dbPath, database.OpenOptions{Chunking: database.Chunking{Size: 99, Overlap: 5}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PROXER_DB")
}

func TestCheckWritableAndErrorClassifiers(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "index.db"), window)
	require.NoError(t, database.CheckWritable(context.Background(), db))

	assert.False(t, database.IsReadOnlyError(nil))
	assert.True(t, database.IsReadOnlyError(syscall.EACCES))
	assert.True(t, database.IsReadOnlyError(syscall.EROFS))
	assert.True(t, database.IsReadOnlyError(errors.New("attempt to write a readonly database")))
	assert.True(t, database.IsReadOnlyError(errors.New("permission denied")))
	assert.False(t, database.IsReadOnlyError(errors.New("syntax error")))

	assert.False(t, database.IsBusyError(nil))
	assert.True(t, database.IsBusyError(errors.New("SQLITE_BUSY")))
	assert.True(t, database.IsBusyError(errors.New("database is locked")))
	assert.True(t, database.IsBusyError(errors.New("database is busy")))
	assert.False(t, database.IsBusyError(errors.New("other error")))
}

func TestMaintainAndScrubPurgedData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	db := open(t, dbPath, window)

	// Clean no-op Maintain skips optimize and vacuum.
	res, err := database.Maintain(db, dbPath, database.MaintainOptions{})
	require.NoError(t, err)
	assert.False(t, res.Optimized)
	assert.False(t, res.Vacuumed)

	// ScrubPurgedData is a no-op when purge_pending is false.
	scrub, err := database.ScrubPurgedData(db, dbPath)
	require.NoError(t, err)
	assert.False(t, scrub.Attempted)
	assert.False(t, scrub.Completed)

	// Write + IncrementWritesSinceOptimize + ForceVacuum resets counter and vacuums.
	store(t, db, window, database.FileRecord{Path: "a.go", Size: 10, Hash: "h1"}, "package a\n", false)
	tx, err := db.Beginx()
	require.NoError(t, err)
	require.NoError(t, database.IncrementWritesSinceOptimize(tx))
	require.NoError(t, tx.Commit())

	writes, err := database.WritesSinceOptimize(db)
	require.NoError(t, err)
	assert.Equal(t, 1, writes)

	res, err = database.Maintain(db, dbPath, database.MaintainOptions{ForceVacuum: true})
	require.NoError(t, err)
	assert.True(t, res.Optimized)
	assert.True(t, res.Vacuumed)
	assert.Empty(t, res.Warning)

	writes, err = database.WritesSinceOptimize(db)
	require.NoError(t, err)
	assert.Equal(t, 0, writes)
}
