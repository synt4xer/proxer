package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
)

// damagedDB opens a real index and then drops one table, which is the only way
// to reach applyDiff's per-statement error branches: a healthy schema accepts
// every write, so those returns would never execute.
func damagedDB(t *testing.T, dropTable string) (*sqlx.DB, string) {
	t.Helper()

	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc a() {}\n"), 0o600))

	_, err = db.Exec("DROP TABLE " + dropTable)
	require.NoError(t, err)

	return db, root
}

func TestApplyDiffReportsRemovedChunkFailure(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, os.WriteFile(filepath.Join(root, "gone.go"), []byte("package gone\n"), 0o600))
	_, err = IndexProjectWithOptions(db, root, IndexOptions{})
	require.NoError(t, err)

	// Break the chunk map and pretend the file vanished: the removal path then
	// runs against a table that no longer exists.
	_, err = db.Exec("DROP TABLE index_chunks")
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, "gone.go")))

	_, err = IndexProjectWithOptions(db, root, IndexOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "index_chunks")
}

// TestApplyDiffReportsAddedManifestFailure breaks the manifest upsert while
// leaving the read path working: chunks are writable, so the insert is the
// statement that fails.
func TestApplyDiffReportsAddedManifestFailure(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600))

	// indexed_files exists but rejects writes, exercising StoreManifest.
	_, err = db.Exec("DROP TABLE indexed_files")
	require.NoError(t, err)
	_, err = db.Exec("CREATE VIEW indexed_files AS SELECT '' AS filepath, 0 AS size, 0 AS mtime_ns, '' AS hash")
	require.NoError(t, err)

	_, err = IndexProjectWithOptions(db, root, IndexOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "indexed_files")
}

func TestApplyDiffReportsRootPathFailure(t *testing.T) {
	db, root := damagedDB(t, "meta")

	_, err := IndexProjectWithOptions(db, root, IndexOptions{})

	require.Error(t, err)
}

// TestApplyDiffReportsChangedManifestFailure covers the changed-file loop, which
// only runs when a known file's content moved.
func TestApplyDiffReportsChangedManifestFailure(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	defer db.Close()

	target := filepath.Join(root, "a.go")
	require.NoError(t, os.WriteFile(target, []byte("package a\n"), 0o600))
	_, err = IndexProjectWithOptions(db, root, IndexOptions{})
	require.NoError(t, err)

	// Change the file so it lands in diff.Changed, then make the manifest
	// readable but unwritable.
	require.NoError(t, os.WriteFile(target, []byte("package a\n\nfunc b() {}\n"), 0o600))
	_, err = db.Exec("DROP TABLE indexed_files")
	require.NoError(t, err)
	_, err = db.Exec("CREATE VIEW indexed_files AS SELECT '' AS filepath, 0 AS size, 0 AS mtime_ns, '' AS hash")
	require.NoError(t, err)

	_, err = IndexProjectWithOptions(db, root, IndexOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "indexed_files")
}

func TestApplyDiffReportsChunkStoreFailure(t *testing.T) {
	db, root := damagedDB(t, "chunk_search")

	_, err := IndexProjectWithOptions(db, root, IndexOptions{})

	require.Error(t, err)
}

func TestApplyDiffRemovesAStoredFileThatDisappeared(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	defer db.Close()

	target := filepath.Join(root, "gone.go")
	require.NoError(t, os.WriteFile(target, []byte("package gone\n"), 0o600))
	_, err = IndexProjectWithOptions(db, root, IndexOptions{})
	require.NoError(t, err)

	require.NoError(t, os.Remove(target))

	refreshed, err := IndexProjectWithOptions(db, root, IndexOptions{})

	require.NoError(t, err)
	assert.Equal(t, 1, refreshed.Removed, "a deleted file must be reported as removed")

	var files int
	require.NoError(t, db.Get(&files, "SELECT count(*) FROM indexed_files"))
	assert.Zero(t, files)
}

// TestApplyDiffDropsIgnoredFileOnReindex pins the ignore contract: a file that
// becomes ignored must leave the index rather than linger from an older build.
func TestApplyDiffDropsIgnoredFileOnReindex(t *testing.T) {
	root := t.TempDir()
	db, err := database.Open(filepath.Join(root, "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.go"), []byte("package keep\n"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "vendor"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "vendor", "v.go"), []byte("package v\n"), 0o600))

	_, err = IndexProjectWithOptions(db, root, IndexOptions{})
	require.NoError(t, err)

	refreshed, err := IndexProjectWithOptions(db, root, IndexOptions{IgnoredDirs: []string{"vendor"}})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, refreshed.Removed, 1, "a newly ignored file must be dropped")
}

func TestComputeDiffReportsMissingRoot(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "index.db"), database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	defer db.Close()

	_, err = IndexProjectWithOptions(db, filepath.Join(t.TempDir(), "does-not-exist"), IndexOptions{})

	require.Error(t, err, "a missing root must not silently produce an empty index")
}
