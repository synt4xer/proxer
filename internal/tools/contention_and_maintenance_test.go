package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/indexer"
)

func TestSearchBusyContentionAndCancelledCtxRestore(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc BusyTarget() {}\n"), 0o644))

	dbPath := filepath.Join(root, ".proxer.db")
	db, err := database.Open(dbPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	defer db.Close()

	_, err = indexer.IndexProject(db, root, nil)
	require.NoError(t, err)

	// Modify file on disk so pre-search refresh attempts a write transaction.
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc BusyTarget() {}\nfunc NewFunc() {}\n"), 0o644))

	// Hold an exclusive write lock on a second connection.
	locker, err := sqlx.Connect("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_txlock=immediate&_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	defer locker.Close()

	lockTx, err := locker.Beginx()
	require.NoError(t, err)
	_, err = lockTx.Exec("UPDATE meta SET value = value WHERE key = 'schema_version'")
	require.NoError(t, err)

	// 1. Contention returns stale: true, stale_reason: "busy" within ~250-500ms and restores busy_timeout=5000.
	start := time.Now()
	res, err := PerformSearch(db, root, "BusyTarget", SearchOptions{Ctx: context.Background()})
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.True(t, res.Stale)
	assert.Equal(t, "busy", res.StaleReason)
	assert.Nil(t, res.Refreshed)
	assert.NotEmpty(t, res.Results)
	assert.Less(t, elapsed, 1500*time.Millisecond)

	var timeout int
	require.NoError(t, db.Get(&timeout, "PRAGMA busy_timeout"))
	assert.Equal(t, database.DefaultBusyTimeoutMS, timeout)

	// 2. Cancelled ctx during contention must still restore busy_timeout=5000 (never poisoned at 250).
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, _ = PerformSearch(db, root, "BusyTarget", SearchOptions{Ctx: ctx})

	require.NoError(t, lockTx.Rollback())

	require.NoError(t, db.Get(&timeout, "PRAGMA busy_timeout"))
	assert.Equal(t, database.DefaultBusyTimeoutMS, timeout)
}

func TestReindexBloatReclaimedByMaintain(t *testing.T) {
	root := t.TempDir()
	lines := make([]byte, 0, 4096)
	lines = append(lines, []byte("package main\n\n")...)
	for i := 0; i < 60; i++ {
		lines = append(lines, []byte(fmt.Sprintf("func helper%d(x int) int { return x + %d }\n", i, i))...)
	}
	for i := 0; i < 20; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.go", i)), lines, 0o644))
	}

	dbPath := filepath.Join(root, ".proxer.db")
	db, err := database.Open(dbPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	defer db.Close()

	_, err = indexer.IndexProject(db, root, nil)
	require.NoError(t, err)
	// Checkpoint initial build so baseline size reflects steady state.
	_, err = database.Maintain(db, dbPath, database.MaintainOptions{ForceVacuum: true})
	require.NoError(t, err)
	baseInfo, err := os.Stat(dbPath)
	require.NoError(t, err)

	target := filepath.Join(root, "f00.go")
	for i := 0; i < 15; i++ {
		content := append(append([]byte{}, lines...), []byte(fmt.Sprintf("// rev %d\n", i))...)
		require.NoError(t, os.WriteFile(target, content, 0o644))
		require.NoError(t, indexer.ReindexFileContent(db, root, target, content, nil))
	}

	writes, err := database.WritesSinceOptimize(db)
	require.NoError(t, err)
	assert.Equal(t, 15, writes)

	// Explicit Maintain runs even with HadDiff=false because writes_since_optimize > 0.
	maint, err := database.Maintain(db, dbPath, database.MaintainOptions{HadDiff: false})
	require.NoError(t, err)
	assert.True(t, maint.Optimized)

	writesAfter, err := database.WritesSinceOptimize(db)
	require.NoError(t, err)
	assert.Equal(t, 0, writesAfter)

	afterInfo, err := os.Stat(dbPath)
	require.NoError(t, err)
	growth := float64(afterInfo.Size()-baseInfo.Size()) / float64(baseInfo.Size())
	assert.Less(t, growth, 0.05, "net DB growth after 15 reindexes + Maintain must be < 5%%")
}
