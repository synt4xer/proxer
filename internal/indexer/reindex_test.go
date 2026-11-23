package indexer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
)

func openWorkspaceDB(t *testing.T, root string) *sqlx.DB {
	t.Helper()

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
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
	mustIndex(t, db, root, config.DefaultIgnoredDirs)
	require.Equal(t, 1, countChunks(t, db, "edited.go"))

	// The edit both replaces the old marker and grows the file across a chunk
	// boundary, so a stale-row bug would be visible in the count.
	var grown strings.Builder
	grown.WriteString("func After() {}\n")
	for i := 0; i < config.ChunkSize*2; i++ {
		grown.WriteString("// filler to force a second chunk\n")
	}
	require.NoError(t, os.WriteFile(edited, []byte(grown.String()), 0o644))

	require.NoError(t, indexer.ReindexFile(db, root, edited, config.DefaultIgnoredDirs))

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
	mustIndex(t, db, root, config.DefaultIgnoredDirs)
	require.Equal(t, 1, countChunks(t, db, "gone.go"))

	require.NoError(t, os.Remove(path))
	require.NoError(t, indexer.ReindexFile(db, root, path, config.DefaultIgnoredDirs))

	assert.Equal(t, 0, countChunks(t, db, "gone.go"), "a deleted file must disappear from the index")
}

func TestReindexFileKeysChunksByProjectPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pkg", "file.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("func First() {}\n"), 0o644))

	db := openWorkspaceDB(t, root)
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	require.NoError(t, os.WriteFile(path, []byte("func Second() {}\n"), 0o644))

	// The relative spelling must hit the same rows as the absolute one.
	relative := filepath.Join("pkg", "file.go")
	require.NoError(t, indexer.ReindexFile(db, root, filepath.Join(root, relative), config.DefaultIgnoredDirs))

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
	err := indexer.ReindexFile(db, root, outside, config.DefaultIgnoredDirs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the project root")

	assert.Zero(t, countChunks(t, db, "../sibling-project/foreign.go"))
}

// TestReindexFileWaitsForConcurrentWriters models the CLI and a long-running
// server sharing one index. Before the busy timeout and BEGIN IMMEDIATE, the
// FTS5 delete took a read snapshot first and the write upgrade failed at once
// with SQLITE_BUSY instead of queuing.
func TestReindexFileWaitsForConcurrentWriters(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, config.DBFile)

	first := openWorkspaceDB(t, root)
	second, err := database.Open(dbPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { second.Close() })

	handles := []*sqlx.DB{first, second}

	files := make([]string, 6)
	for i := range files {
		files[i] = filepath.Join(root, fmt.Sprintf("file%d.go", i))
		require.NoError(t, os.WriteFile(files[i], []byte("package p\n"), 0o644))
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(files))
	for i, path := range files {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			if err := indexer.ReindexFile(handles[i%len(handles)], root, path, config.DefaultIgnoredDirs); err != nil {
				errs <- err
			}
		}(i, path)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("a concurrent reindex failed instead of waiting: %v", err)
	}
	for i := range files {
		assert.Equal(t, 1, countChunks(t, first, fmt.Sprintf("file%d.go", i)))
	}
}

func TestReindexFileDropsChunksOfAnIgnoredDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "generated", "gen.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("func Generated() {}\n"), 0o644))

	db := openWorkspaceDB(t, root)
	ignored := append(append([]string{}, config.DefaultIgnoredDirs...), "generated")
	// Seed the row a previous build without the skip list would have left.
	mustIndex(t, db, root, config.DefaultIgnoredDirs)
	require.Equal(t, 1, countChunks(t, db, "generated/gen.go"))

	require.NoError(t, indexer.ReindexFile(db, root, path, ignored))

	assert.Equal(t, 0, countChunks(t, db, "generated/gen.go"),
		"editing a file inside an ignored directory must not keep it in the index")
}

func TestInitDBAcceptsPathsWithURIDelimiters(t *testing.T) {
	// "file:" DSNs hand the path to SQLite's URI parser, so a "?" or "#" in a
	// directory name must be escaped to stay part of the filename.
	root := filepath.Join(t.TempDir(), "odd ? # name")
	require.NoError(t, os.MkdirAll(root, 0o755))

	db := openWorkspaceDB(t, root)
	path := filepath.Join(root, "main.go")
	require.NoError(t, os.WriteFile(path, []byte("package odd\n"), 0o644))
	require.NoError(t, indexer.ReindexFile(db, root, path, config.DefaultIgnoredDirs))

	assert.Contains(t, contentFor(t, db, "main.go"), "package odd")
}
