package database

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenReadOnlyAndUnreadable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath, OpenOptions{Chunking: Chunking{Size: 60, Overlap: 15}})
	require.NoError(t, err)

	tx, err := db.Beginx()
	require.NoError(t, err)
	w, err := NewWriter(tx, Chunking{Size: 60, Overlap: 15})
	require.NoError(t, err)
	require.NoError(t, w.StoreLines(FileRecord{Path: "a.go", Size: 20, MTimeNS: 1, Hash: "h1"}, []string{"package main", "func Hello() {}"}, true))
	require.NoError(t, w.Close())
	require.NoError(t, StampIndexedAt(tx))
	require.NoError(t, tx.Commit())
	require.NoError(t, db.Close())

	require.NoError(t, os.Chmod(dbPath, 0o444))
	require.NoError(t, os.Chmod(dir, 0o555))
	defer func() {
		_ = os.Chmod(dir, 0o755)
		_ = os.Chmod(dbPath, 0o600)
	}()

	roDB, err := OpenReadOnly(dbPath, Chunking{Size: 60, Overlap: 15})
	require.NoError(t, err)
	var count int
	require.NoError(t, roDB.Get(&count, "SELECT count(*) FROM indexed_files"))
	assert.Equal(t, 1, count)
	require.NoError(t, roDB.Close())

	// Schema/chunking mismatch is rejected cleanly.
	_, err = OpenReadOnly(dbPath, Chunking{Size: 30, Overlap: 5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")

	// Unreadable DB file (0000) is rejected with PROXER_DB hint by both Open and OpenReadOnly.
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, os.Chmod(dbPath, 0o000))
	assert.False(t, IsReadableFile(dbPath))
	if os.Geteuid() != 0 {
		_, err = Open(dbPath, OpenOptions{Chunking: Chunking{Size: 60, Overlap: 15}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PROXER_DB")
	}
	_, err = OpenReadOnly(dbPath, Chunking{Size: 60, Overlap: 15})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PROXER_DB")
}

func TestMaintainNoOpLowDiskAndChurn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "maint.db")
	db, err := Open(dbPath, OpenOptions{Chunking: Chunking{Size: 60, Overlap: 15}})
	require.NoError(t, err)
	defer db.Close()

	// Clean no-op skips optimize and VACUUM.
	res, err := Maintain(db, dbPath, MaintainOptions{})
	require.NoError(t, err)
	assert.False(t, res.Optimized)
	assert.False(t, res.Vacuumed)

	// Insert enough data to exceed 64 KB freelist floor on churn.
	tx, err := db.Beginx()
	require.NoError(t, err)
	w, err := NewWriter(tx, Chunking{Size: 60, Overlap: 15})
	require.NoError(t, err)
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("func fn%d() int { return %d }", i, i)
	}
	for i := 0; i < 40; i++ {
		p := fmt.Sprintf("f%02d.go", i)
		require.NoError(t, w.StoreLines(FileRecord{Path: p, Size: 100, MTimeNS: 1, Hash: p}, lines, true))
	}
	require.NoError(t, w.Close())
	require.NoError(t, IncrementWritesSinceOptimize(tx))
	require.NoError(t, tx.Commit())

	// Low-disk-space check skips VACUUM gracefully without failing.
	origAvail := availableDiskBytes
	availableDiskBytes = func(string) (uint64, error) { return 1, nil }
	t.Cleanup(func() { availableDiskBytes = origAvail })

	res, err = Maintain(db, dbPath, MaintainOptions{ForceVacuum: true})
	require.NoError(t, err)
	assert.True(t, res.Optimized)
	assert.False(t, res.Vacuumed)
	assert.Contains(t, res.Warning, "skipped VACUUM")

	availableDiskBytes = origAvail

	// Verify shouldVacuum dual clause: >= 64KB AND (ratio >= 5% OR (writes >= 50 AND ratio >= 2%)).
	assert.False(t, shouldVacuum(1000, 10, 4096, 0))   // < 64 KB
	assert.False(t, shouldVacuum(1000, 30, 4096, 10))  // 3%, < 50 writes
	assert.True(t, shouldVacuum(1000, 30, 4096, 50))   // 3%, >= 50 writes
	assert.True(t, shouldVacuum(1000, 50, 4096, 0))    // 5%, >= 64 KB
	assert.False(t, shouldVacuum(1000, 15, 4096, 100)) // 1.5%, < 2%
}
