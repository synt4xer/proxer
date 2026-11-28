package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
)

// TestApplyDiffPaysForBusyTimeoutOnlyWhenAsked covers the guarded transaction
// path: callers that pin a busy timeout get a dedicated connection whose pragma
// is restored afterwards.
func TestApplyDiffPaysForBusyTimeoutOnlyWhenAsked(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600))

	refreshed, err := IndexProjectWithOptions(db, root, IndexOptions{
		Ctx:           context.Background(),
		BusyTimeoutMS: 250,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, refreshed.Added)

	var timeout int
	require.NoError(t, db.Get(&timeout, "PRAGMA busy_timeout"))
	assert.Equal(t, database.DefaultBusyTimeoutMS, timeout, "the caller's busy timeout must not leak to the pooled connection")
}

func TestApplyDiffRestoresBusyTimeoutOnFailure(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600))
	_, err = db.Exec("DROP TABLE chunk_search")
	require.NoError(t, err)

	_, err = IndexProjectWithOptions(db, root, IndexOptions{
		Ctx:           context.Background(),
		BusyTimeoutMS: 250,
	})

	require.Error(t, err)

	var timeout int
	require.NoError(t, db.Get(&timeout, "PRAGMA busy_timeout"))
	assert.Equal(t, database.DefaultBusyTimeoutMS, timeout, "the restore must run even when the diff fails")
}

// TestApplyDiffReportsBusyTimeoutFailure pins that a broken pragma aborts the
// refresh instead of quietly writing with the wrong timeout.
func TestApplyDiffReportsBusyTimeoutFailure(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600))
	require.NoError(t, db.Close())

	_, err = applyDiff(db, root, &Diff{Added: []database.FileRecord{{Path: "a.go"}}}, IndexOptions{
		Ctx:           context.Background(),
		BusyTimeoutMS: 250,
	})

	require.Error(t, err, "a closed pool must fail before any rows are written")
}

func TestApplyDiffReportsTransactionFailure(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)

	_, err = db.Exec("DROP TABLE chunk_search")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = applyDiff(db, root, &Diff{Added: []database.FileRecord{{Path: "a.go"}}}, IndexOptions{})

	require.Error(t, err)
}

func TestApplyDiffRemovesAFileWithNoChunks(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	defer db.Close()

	_, err = applyDiff(db, root, &Diff{Removed: []string{"never-indexed.go"}}, IndexOptions{})

	require.NoError(t, err)
	assert.Zero(t, countRows(t, db, "SELECT count(*) FROM indexed_files"))
	assert.Zero(t, countRows(t, db, "SELECT count(*) FROM index_chunks"))
}

func countRows(t *testing.T, db interface {
	Get(any, string, ...any) error
}, query string) int {
	t.Helper()

	var n int
	require.NoError(t, db.Get(&n, query))
	return n
}
