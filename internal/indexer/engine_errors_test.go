package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/filesystem"
)

func indexerDB(t *testing.T) *sqlx.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "index.db"), database.OpenOptions{
		Chunking: Chunking(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestIndexProjectWithOptionsPropagatesDiffFailure(t *testing.T) {
	db := indexerDB(t)
	require.NoError(t, db.Close())

	_, err := IndexProjectWithOptions(db, t.TempDir(), IndexOptions{})

	require.Error(t, err, "a closed database must surface as an error, not an empty refresh")
}

func TestRelativePathEmptyRootUsesWorkingDirectory(t *testing.T) {
	// Callers reach the empty-root branch with relative walks, which is the only
	// shape where "." is a meaningful stand-in for the root.
	rel, err := relativePath("", filepath.Join("sub", "file.go"))

	require.NoError(t, err)
	assert.Equal(t, "sub/file.go", rel, "an empty root must behave like '.'")
}

func TestRelativePathRejectsEscape(t *testing.T) {
	root := t.TempDir()

	rel, err := relativePath(root, filepath.Join(root, "..", "outside.go"))

	require.Error(t, err)
	assert.Empty(t, rel)
	assert.Contains(t, err.Error(), "is outside the project root")
}

func TestRelativePathRejectsDotDotExactly(t *testing.T) {
	root := t.TempDir()

	_, err := relativePath(root, filepath.Join(root, ".."))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is outside the project root")
}

// TestReindexFileWithContentPropagatesIndexerFailures drives the early returns
// in reindexFileWithContent that a healthy database never reaches.
func TestReindexFileWithContentPropagatesIndexerFailures(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600))

	db := indexerDB(t)
	require.NoError(t, db.Close())

	err := ReindexFileContent(db, root, filepath.Join(root, "a.go"), []byte("package a\n"), nil)

	require.Error(t, err)
}

func TestReindexFileWithContentRejectsPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	require.NoError(t, os.WriteFile(outside, []byte("package outside\n"), 0o600))

	db := indexerDB(t)

	err := ReindexFileContent(db, root, outside, []byte("package outside\n"), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is outside the project root")
}

func TestStoreCurrentFileReportsReadFailure(t *testing.T) {
	root := t.TempDir()
	db := indexerDB(t)

	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()

	writer, err := database.NewWriter(tx, Chunking())
	require.NoError(t, err)
	defer writer.Close()

	// A directory is a path that exists but cannot be read as a file, so the
	// read error is neither os.IsNotExist nor recoverable.
	dir := filepath.Join(root, "adir")
	require.NoError(t, os.Mkdir(dir, 0o700))

	stored, reason, err := storeCurrentFile(tx, writer, root, database.FileRecord{Path: "adir"}, nil, false)

	require.Error(t, err)
	assert.False(t, stored)
	assert.Equal(t, filesystem.SkipNone, reason)
	assert.Contains(t, err.Error(), "failed to read")
}

// TestStoreCurrentFileDropsAFileThatVanished pins the truthful outcome when a
// file disappears between the walk and the write.
func TestStoreCurrentFileDropsAFileThatVanished(t *testing.T) {
	root := t.TempDir()
	db := indexerDB(t)

	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()

	writer, err := database.NewWriter(tx, Chunking())
	require.NoError(t, err)
	defer writer.Close()

	stored, _, err := storeCurrentFile(tx, writer, root, database.FileRecord{Path: "gone.go"}, nil, false)

	require.NoError(t, err)
	assert.False(t, stored, "a vanished file is not stored")
}

func TestStoreCurrentFileSkipsOversizedContent(t *testing.T) {
	root := t.TempDir()
	db := indexerDB(t)

	big := make([]byte, config.MaxFileSize+1)
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.go"), big, 0o600))

	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()

	writer, err := database.NewWriter(tx, Chunking())
	require.NoError(t, err)
	defer writer.Close()

	stored, reason, err := storeCurrentFile(tx, writer, root, database.FileRecord{Path: "big.go"}, nil, false)

	require.NoError(t, err)
	assert.False(t, stored)
	assert.Equal(t, filesystem.SkipSize, reason, "an oversized file must be recorded as skipped for size")
}
