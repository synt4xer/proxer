package indexer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
)

// newWorkspace builds an isolated project on disk and returns its root. Tests
// no longer chdir into it: the index is addressed by root, not by process
// working directory.
func newWorkspace(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "internal"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "internal", "hello.go"),
		[]byte("package main\nimport \"fmt\"\nfunc hello() {\n\tfmt.Println(\"Hello Agent!\")\n}\n"),
		0o644,
	))
	// A binary extension must be skipped by the scanner.
	require.NoError(t, os.WriteFile(filepath.Join(root, "ignore_me.png"), []byte("fake png"), 0o644))

	return root
}

func TestIndexerEngine(t *testing.T) {
	root := newWorkspace(t)

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err, "InitDB should not produce an error")
	defer db.Close()

	var tableName string
	require.NoError(t, db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='chunk_search'").Scan(&tableName))
	assert.Equal(t, "chunk_search", tableName)

	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM chunk_search").Scan(&count))
	assert.Equal(t, 1, count, "Only internal/hello.go should be indexed")

	var storedPath string
	var start, end int
	require.NoError(t, db.QueryRow("SELECT filepath, start_line, end_line FROM chunk_search WHERE content MATCH 'Agent'").Scan(&storedPath, &start, &end))

	assert.Equal(t, "internal/hello.go", storedPath, "chunks must be keyed by path inside the project")
	assert.Equal(t, 1, start)
	assert.True(t, end > 1)
}

func TestIndexProjectStoresIdenticalPathsForAbsoluteAndRelativeRoots(t *testing.T) {
	root := newWorkspace(t)

	// Index once through the absolute root, once through a path relative to
	// the parent directory. Both must produce the same rows.
	relRoot, err := filepath.Rel(filepath.Dir(root), root)
	require.NoError(t, err)

	var indexed []string
	for _, candidate := range []string{root, filepath.Join(filepath.Dir(root), relRoot)} {
		db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
		require.NoError(t, err)

		mustIndex(t, db, candidate, config.DefaultIgnoredDirs)

		var rows []string
		require.NoError(t, db.Select(&rows, "SELECT filepath FROM chunk_search ORDER BY filepath"))
		indexed = append(indexed, rows...)

		require.NoError(t, db.Close())
	}

	require.Len(t, indexed, 2)
	assert.Equal(t, indexed[0], indexed[1], "root spelling must not leak into the index")
}

func TestIndexProjectHandlesIgnoredDirectoryAsRoot(t *testing.T) {
	// "build" is an ignored directory name, but it is legitimate as a project
	// root. Indexing must not silently produce an empty index.
	root := filepath.Join(t.TempDir(), "build")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("func Build() {}\n"), 0o644))

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	defer db.Close()

	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM chunk_search").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestIndexProjectHonorsAProjectSkipList(t *testing.T) {
	root := newWorkspace(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "generated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "generated", "gen.go"), []byte("func Gen() {}\n"), 0o644))

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	defer db.Close()

	ignored := append(append([]string{}, config.DefaultIgnoredDirs...), "generated")
	mustIndex(t, db, root, ignored)

	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM chunk_search WHERE filepath LIKE 'generated/%'").Scan(&count))
	assert.Zero(t, count, "a directory on the project skip list must not be indexed")
}
