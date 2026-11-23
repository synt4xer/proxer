package database

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedLeakedDB(t *testing.T, dbPath, secret string) {
	t.Helper()

	db, err := Open(dbPath, OpenOptions{
		Chunking:       Chunking{Size: 60, Overlap: 15},
		IndexSensitive: true,
	})
	require.NoError(t, err)

	tx, err := db.Beginx()
	require.NoError(t, err)
	writer, err := NewWriter(tx, Chunking{Size: 60, Overlap: 15})
	require.NoError(t, err)

	require.NoError(t, writer.Store(FileRecord{Path: ".env", Size: 64, Hash: "h1"}, "API_KEY="+secret+"\n", false))
	require.NoError(t, writer.Store(FileRecord{Path: "config/prod.env", Size: 64, Hash: "h2"}, "DB_PASSWORD="+secret+"\n", false))
	require.NoError(t, writer.Store(FileRecord{Path: "main.go", Size: 32, Hash: "h3"}, "package main\nfunc Safe() {}\n", false))
	require.NoError(t, ResetSensitivePurge(tx))
	require.NoError(t, StampIndexedAt(tx))
	require.NoError(t, writer.Close())
	require.NoError(t, tx.Commit())
	require.NoError(t, db.Close())
}

func assertNoSecretBytesOnDisk(t *testing.T, dbPath, secret string) {
	t.Helper()

	needle := []byte(secret)
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		data, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		assert.False(t, bytes.Contains(data, needle), "secret bytes found in %s", filepath.Base(p))
	}
}

func assertMode0600(t *testing.T, dbPath string) {
	t.Helper()

	info, err := os.Stat(dbPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "DB file must be 0600")

	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Stat(dbPath + suffix)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "DB sibling %s must be 0600", suffix)
	}
}

func TestDBFilePermissions0600(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".proxer.db")

	db, err := Open(dbPath, OpenOptions{Chunking: Chunking{Size: 60, Overlap: 15}})
	require.NoError(t, err)
	assertMode0600(t, dbPath)
	require.NoError(t, db.Close())

	require.NoError(t, os.Chmod(dbPath, 0o644))
	db2, err := Open(dbPath, OpenOptions{Chunking: Chunking{Size: 60, Overlap: 15}})
	require.NoError(t, err)
	assertMode0600(t, dbPath)
	require.NoError(t, db2.Close())
}

func TestSensitivePurgeStage1AndStage2(t *testing.T) {
	const secret = "ULTRA_SECRET_TOKEN_P1A_TEST_987654"
	dbPath := filepath.Join(t.TempDir(), ".proxer.db")
	seedLeakedDB(t, dbPath, secret)

	db, err := Open(dbPath, OpenOptions{
		Chunking:       Chunking{Size: 60, Overlap: 15},
		IndexSensitive: false,
	})
	require.NoError(t, err)
	defer db.Close()

	var count int
	require.NoError(t, db.Get(&count, "SELECT COUNT(*) FROM indexed_files WHERE filepath IN ('.env', 'config/prod.env')"))
	assert.Zero(t, count, "Stage 1 must delete sensitive manifest rows")
	require.NoError(t, db.Get(&count, "SELECT COUNT(*) FROM chunk_search WHERE chunk_search MATCH ?", `"`+secret+`"`))
	assert.Zero(t, count, "Stage 1 must delete sensitive FTS rows immediately")

	sentinel, err := metaValue(db, metaSensitivePurge)
	require.NoError(t, err)
	assert.Equal(t, "1", sentinel)

	pending, err := PurgePending(db)
	require.NoError(t, err)
	assert.True(t, pending)

	origAvail := availableDiskBytes
	availableDiskBytes = func(string) (uint64, error) { return 1, nil }
	lowDiskRes, err := ScrubPurgedData(db, dbPath)
	availableDiskBytes = origAvail
	require.NoError(t, err)
	assert.True(t, lowDiskRes.Attempted)
	assert.False(t, lowDiskRes.Completed)
	assert.True(t, lowDiskRes.PurgePending)
	assert.NotEmpty(t, lowDiskRes.Warning)

	pending, err = PurgePending(db)
	require.NoError(t, err)
	assert.True(t, pending, "purge_pending must stay set after low-disk skip")

	scrubRes, err := ScrubPurgedData(db, dbPath)
	require.NoError(t, err)
	assert.True(t, scrubRes.Attempted)
	assert.True(t, scrubRes.Completed)
	assert.False(t, scrubRes.PurgePending)
	assert.Empty(t, scrubRes.Warning)

	pending, err = PurgePending(db)
	require.NoError(t, err)
	assert.False(t, pending)

	assertMode0600(t, dbPath)
	assertNoSecretBytesOnDisk(t, dbPath, secret)
}

func TestSensitivePurgeStage2ConcurrentReaderKeepsPurgePending(t *testing.T) {
	const secret = "CONCURRENT_READER_SECRET_TOKEN_4321"
	dbPath := filepath.Join(t.TempDir(), ".proxer.db")
	seedLeakedDB(t, dbPath, secret)

	// Hold an active read snapshot so WAL cannot truncate while Stage 2 runs.
	reader, err := sqlx.Connect("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	require.NoError(t, err)
	readerConn, err := reader.Conn(context.Background())
	require.NoError(t, err)
	_, err = readerConn.ExecContext(context.Background(), "BEGIN DEFERRED")
	require.NoError(t, err)
	var dummy int
	require.NoError(t, readerConn.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM indexed_files").Scan(&dummy))

	db, err := Open(dbPath, OpenOptions{
		Chunking:       Chunking{Size: 60, Overlap: 15},
		IndexSensitive: false,
	})
	require.NoError(t, err)
	defer db.Close()

	scrubBlocked, err := ScrubPurgedData(db, dbPath)
	require.NoError(t, err)
	assert.True(t, scrubBlocked.Attempted)
	assert.False(t, scrubBlocked.Completed)
	assert.True(t, scrubBlocked.PurgePending)
	assert.NotEmpty(t, scrubBlocked.Warning)

	pending, err := PurgePending(db)
	require.NoError(t, err)
	assert.True(t, pending)

	_, err = readerConn.ExecContext(context.Background(), "ROLLBACK")
	require.NoError(t, err)
	require.NoError(t, readerConn.Close())
	require.NoError(t, reader.Close())

	scrubDone, err := ScrubPurgedData(db, dbPath)
	require.NoError(t, err)
	assert.True(t, scrubDone.Attempted)
	assert.True(t, scrubDone.Completed)
	assert.False(t, scrubDone.PurgePending)
	assert.Empty(t, scrubDone.Warning)

	pending, err = PurgePending(db)
	require.NoError(t, err)
	assert.False(t, pending)
	assertNoSecretBytesOnDisk(t, dbPath, secret)
}

func TestCheckWritablePreflight(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), ".proxer.db")
	db, err := Open(dbPath, OpenOptions{Chunking: Chunking{Size: 60, Overlap: 15}})
	require.NoError(t, err)
	require.NoError(t, CheckWritable(context.Background(), db))

	t.Run("rejects mode=ro handle", func(t *testing.T) {
		roDB, err := sqlx.Connect("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
		require.NoError(t, err)
		defer roDB.Close()

		err = CheckWritable(context.Background(), roDB)
		require.Error(t, err)
		assert.True(t, IsReadOnlyError(err), "expected readonly error, got: %v", err)
	})

	t.Run("fails fast under write lock and restores busy_timeout", func(t *testing.T) {
		locker, err := sqlx.Connect("sqlite", dsn(dbPath))
		require.NoError(t, err)
		defer locker.Close()

		lockConn, err := locker.Conn(context.Background())
		require.NoError(t, err)
		defer lockConn.Close()
		_, err = lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
		require.NoError(t, err)
		defer lockConn.ExecContext(context.Background(), "ROLLBACK")

		start := time.Now()
		err = CheckWritable(context.Background(), db)
		elapsed := time.Since(start)
		require.Error(t, err)
		assert.True(t, IsBusyError(err), "expected busy error, got: %v", err)
		assert.Less(t, elapsed, 1500*time.Millisecond, "CheckWritable must not hang for 5s")

		var timeout int
		require.NoError(t, db.Get(&timeout, "PRAGMA busy_timeout"))
		assert.Equal(t, defaultBusyTimeoutMS, timeout, "busy_timeout must be restored to 5000")
	})

	require.NoError(t, db.Close())

	t.Run("rejects chmod 0444 file", func(t *testing.T) {
		require.NoError(t, os.Chmod(dbPath, 0o444))
		defer os.Chmod(dbPath, 0o600)

		roFileDB, err := sqlx.Connect("sqlite", dsn(dbPath))
		require.NoError(t, err)
		defer roFileDB.Close()

		err = CheckWritable(context.Background(), roFileDB)
		require.Error(t, err)
		assert.True(t, IsReadOnlyError(err), "expected readonly error on 0444 file, got: %v", err)
	})
}
