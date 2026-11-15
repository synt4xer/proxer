package indexer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
)

func openWorkspaceDB(t *testing.T, root string) *sqlx.DB {
	t.Helper()

	db, err := indexer.InitDB(filepath.Join(root, config.DBFile))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	return db
}

func countChunks(t *testing.T, db *sqlx.DB, rel string) int {
	t.Helper()

	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", rel).Scan(&count))
	return count
}

func contentFor(t *testing.T, db *sqlx.DB, rel string) string {
	t.Helper()

	var rows []string
	require.NoError(t, db.Select(&rows, "SELECT content FROM chunk_search WHERE filepath = ?", rel))
	return strings.Join(rows, "\n")
}

func TestReindexFileReplacesOnlyTheEditedFile(t *testing.T) {
	root := t.TempDir()
	edited := filepath.Join(root, "edited.go")
	untouched := filepath.Join(root, "untouched.go")

	require.NoError(t, os.WriteFile(edited, []byte("func Before() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(untouched, []byte("func Untouched() {}\n"), 0o644))

	db := openWorkspaceDB(t, root)
	require.NoError(t, indexer.IndexProject(db, root))
	require.Equal(t, 1, countChunks(t, db, "edited.go"))

	// The edit both replaces the old marker and grows the file across a chunk
	// boundary, so a stale-row bug would be visible in the count.
	var grown strings.Builder
	grown.WriteString("func After() {}\n")
	for i := 0; i < config.ChunkSize*2; i++ {
		grown.WriteString("// filler to force a second chunk\n")
	}
	require.NoError(t, os.WriteFile(edited, []byte(grown.String()), 0o644))

	require.NoError(t, indexer.ReindexFile(db, root, edited))

	editedContent := contentFor(t, db, "edited.go")
	assert.Contains(t, editedContent, "After")
	assert.NotContains(t, editedContent, "Before", "stale chunks of the edited file must be dropped")
	assert.Greater(t, countChunks(t, db, "edited.go"), 1, "the grown file must be re-chunked")

	assert.Equal(t, 1, countChunks(t, db, "untouched.go"), "other files must survive a single-file reindex")
	assert.Contains(t, contentFor(t, db, "untouched.go"), "Untouched")
}

func TestReindexFileDropsChunksOfDeletedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "gone.go")
	require.NoError(t, os.WriteFile(path, []byte("func Gone() {}\n"), 0o644))

	db := openWorkspaceDB(t, root)
	require.NoError(t, indexer.IndexProject(db, root))
	require.Equal(t, 1, countChunks(t, db, "gone.go"))

	require.NoError(t, os.Remove(path))
	require.NoError(t, indexer.ReindexFile(db, root, path))

	assert.Equal(t, 0, countChunks(t, db, "gone.go"), "a deleted file must disappear from the index")
}

func TestReindexFileKeysChunksByProjectPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pkg", "file.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("func First() {}\n"), 0o644))

	db := openWorkspaceDB(t, root)
	require.NoError(t, indexer.IndexProject(db, root))

	require.NoError(t, os.WriteFile(path, []byte("func Second() {}\n"), 0o644))

	// The relative spelling must hit the same rows as the absolute one.
	relative := filepath.Join("pkg", "file.go")
	require.NoError(t, indexer.ReindexFile(db, root, filepath.Join(root, relative)))

	assert.Equal(t, 1, countChunks(t, db, "pkg/file.go"), "reindex must not duplicate rows")
	assert.Contains(t, contentFor(t, db, "pkg/file.go"), "Second")
}

func TestReindexFileRejectsPathsOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "sibling-project", "foreign.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(outside), 0o755))
	require.NoError(t, os.WriteFile(outside, []byte("package foreign\n"), 0o644))

	db := openWorkspaceDB(t, root)

	// Storing the file under a "../" key would be worse than failing: no later
	// walk of the root can ever reach that key to clean it up.
	err := indexer.ReindexFile(db, root, outside)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the project root")

	assert.Zero(t, countChunks(t, db, "../sibling-project/foreign.go"))
}
