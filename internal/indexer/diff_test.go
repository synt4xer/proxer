package indexer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
)

func write(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// touch moves a file's modification time forward without changing its bytes.
// Tests use it instead of relying on the write itself to move the timestamp,
// because a fast filesystem can hand out the same value twice.
func touch(t *testing.T, path string) {
	t.Helper()

	later := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, later, later))
}

func chunkRowIDs(t *testing.T, db *sqlx.DB, rel string) []int64 {
	t.Helper()

	var rowids []int64
	require.NoError(t, db.Select(&rowids, "SELECT rowid FROM chunk_search WHERE filepath = ? ORDER BY rowid", rel))
	return rowids
}

func TestDeltaBuildTracksTheTree(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)
	ignored := config.DefaultIgnoredDirs

	write(t, filepath.Join(root, "a.go"), "func Alpha() {}\n")

	t.Run("counts a new file", func(t *testing.T) {
		refreshed := mustIndex(t, db, root, ignored)
		assert.Equal(t, indexer.Refreshed{Added: 1}, refreshed)
		assert.Len(t, chunkRowIDs(t, db, "a.go"), 1)
	})

	t.Run("picks up a file added outside proxer", func(t *testing.T) {
		write(t, filepath.Join(root, "b.go"), "func Beta() {}\n")

		refreshed := mustIndex(t, db, root, ignored)
		assert.Equal(t, indexer.Refreshed{Added: 1}, refreshed)
		assert.Len(t, chunkRowIDs(t, db, "b.go"), 1)
	})

	t.Run("drops a file deleted outside proxer", func(t *testing.T) {
		require.NoError(t, os.Remove(filepath.Join(root, "a.go")))

		refreshed := mustIndex(t, db, root, ignored)
		assert.Equal(t, indexer.Refreshed{Removed: 1}, refreshed)
		assert.Empty(t, chunkRowIDs(t, db, "a.go"))
		assert.Zero(t, countChunks(t, db, "a.go"))

		var mapped int
		require.NoError(t, db.Get(&mapped, "SELECT COUNT(*) FROM index_chunks WHERE filepath = ?", "a.go"))
		assert.Zero(t, mapped, "the chunk map must shrink with the chunks it points at")
	})

	t.Run("re-chunks a file rewritten outside proxer", func(t *testing.T) {
		write(t, filepath.Join(root, "b.go"), "func Gamma() {}\n// more\n")
		touch(t, filepath.Join(root, "b.go"))

		refreshed := mustIndex(t, db, root, ignored)
		assert.Equal(t, indexer.Refreshed{Changed: 1}, refreshed)

		content := contentFor(t, db, "b.go")
		assert.Contains(t, content, "Gamma")
		assert.NotContains(t, content, "Beta", "the old chunk must not survive a rewrite")
	})

	t.Run("does nothing when the tree has not moved", func(t *testing.T) {
		chunksBefore := countChunks(t, db, "b.go")

		refreshed := mustIndex(t, db, root, ignored)
		assert.True(t, refreshed.Empty(), "an unchanged tree must produce no work: %+v", refreshed)
		assert.Equal(t, chunksBefore, countChunks(t, db, "b.go"))
	})
}

// TestDeltaBuildReStampsATouchedFile covers the case the cheap gate exists for:
// the timestamp moves but the bytes do not. Re-chunking would be wasted work,
// and the hash backstop is what proves it is wasted.
func TestDeltaBuildReStampsATouchedFile(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)
	ignored := config.DefaultIgnoredDirs

	path := filepath.Join(root, "same.go")
	write(t, path, "func Same() {}\n")
	mustIndex(t, db, root, ignored)

	rowidsBefore := chunkRowIDs(t, db, "same.go")
	require.Len(t, rowidsBefore, 1)

	write(t, path, "func Same() {}\n")
	touch(t, path)

	refreshed := mustIndex(t, db, root, ignored)
	assert.True(t, refreshed.Empty(), "identical bytes must not count as a change: %+v", refreshed)
	assert.Equal(t, rowidsBefore, chunkRowIDs(t, db, "same.go"),
		"the chunk must be kept, not deleted and re-inserted")

	var manifestHash string
	var manifestMtime int64
	require.NoError(t, db.QueryRow("SELECT hash, mtime_ns FROM indexed_files WHERE filepath = ?", "same.go").Scan(&manifestHash, &manifestMtime))
	assert.Equal(t, indexer.ContentHash([]byte("func Same() {}\n")), manifestHash)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, info.ModTime().UnixNano(), manifestMtime, "the manifest must track the new timestamp")
}

// TestDeltaBuildKeepsAMissedTimestampHonest covers the narrow failure the plan
// documents: a same-size write inside the same timestamp tick. The stat gate
// says "unchanged", so the delta does not read the file and cannot see the
// write. The test pins the documented behaviour instead of pretending the gate
// is invisible.
func TestDeltaBuildKeepsAMissedTimestampHonest(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)
	ignored := config.DefaultIgnoredDirs

	path := filepath.Join(root, "same.go")
	write(t, path, "func First() {}\n")
	mustIndex(t, db, root, ignored)

	info, err := os.Stat(path)
	require.NoError(t, err)

	// Same length, same timestamp: without reading the file there is nothing to
	// tell this apart from an untouched file.
	write(t, path, "func Other() {}\n")
	require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))

	refreshed := mustIndex(t, db, root, ignored)
	assert.True(t, refreshed.Empty(), "the stat gate deliberately skips the read: %+v", refreshed)
}

// TestNoChangeRefreshTakesNoWriteLock is the promise that makes a refresh on
// the read path affordable. A second connection holds the write lock; if the
// no-change path tried to write, it would block on the five second busy timeout
// and then fail.
func TestNoChangeRefreshTakesNoWriteLock(t *testing.T) {
	root := t.TempDir()
	ignored := config.DefaultIgnoredDirs
	dbPath := filepath.Join(root, config.DBFile)

	reader := openWorkspaceDB(t, root)
	write(t, filepath.Join(root, "main.go"), "func Main() {}\n")
	mustIndex(t, reader, root, ignored)

	writer, err := database.Open(dbPath, indexer.Chunking())
	require.NoError(t, err)
	t.Cleanup(func() { writer.Close() })

	lock, err := writer.Beginx()
	require.NoError(t, err)
	t.Cleanup(func() { lock.Rollback() })

	started := time.Now()
	refreshed, err := indexer.IndexProject(reader, root, ignored)
	elapsed := time.Since(started)

	require.NoError(t, err, "a no-change refresh must not wait for the writer")
	assert.True(t, refreshed.Empty())
	assert.Less(t, elapsed, 2*time.Second, "a no-change refresh must not take the write lock")
}

// TestLegacyDatabaseIsRebuiltOnce covers the upgrade path. A database written
// before the manifest existed has chunks but no rowid map and no metadata, so a
// delta run against it would insert rows without dropping the old ones.
func TestLegacyDatabaseIsRebuiltOnce(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, config.DBFile)

	legacy, err := sqlx.Connect("sqlite", dbPath)
	require.NoError(t, err)
	_, err = legacy.Exec(`CREATE VIRTUAL TABLE chunk_search USING fts5(
		id UNINDEXED, filepath, start_line UNINDEXED, end_line UNINDEXED, content
	)`)
	require.NoError(t, err)
	_, err = legacy.Exec(
		"INSERT INTO chunk_search (id, filepath, start_line, end_line, content) VALUES (?, ?, ?, ?, ?)",
		"stale.go:1-1", "stale.go", 1, 1, "func Stale() {}",
	)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())

	write(t, filepath.Join(root, "main.go"), "func Main() {}\n")

	db := openWorkspaceDB(t, root)

	var stale int
	require.NoError(t, db.Get(&stale, "SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", "stale.go"))
	assert.Zero(t, stale, "a pre-manifest database must be cleared, not extended")

	var version string
	require.NoError(t, db.Get(&version, "SELECT value FROM meta WHERE key = 'schema_version'"))
	assert.Equal(t, database.SchemaVersion, version)

	refreshed := mustIndex(t, db, root, config.DefaultIgnoredDirs)
	assert.Equal(t, indexer.Refreshed{Added: 1}, refreshed, "the rebuild must come from the tree")
	assert.Zero(t, countChunks(t, db, "stale.go"))
}

func TestReindexFileWritesTheManifestRow(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)
	ignored := config.DefaultIgnoredDirs

	path := filepath.Join(root, "edit.go")
	write(t, path, "func Before() {}\n")
	mustIndex(t, db, root, ignored)

	write(t, path, "func After() {}\n")
	require.NoError(t, indexer.ReindexFile(db, root, path, ignored))

	var storedHash string
	require.NoError(t, db.Get(&storedHash, "SELECT hash FROM indexed_files WHERE filepath = ?", "edit.go"))
	assert.Equal(t, indexer.ContentHash([]byte("func After() {}\n")), storedHash)

	// The manifest must agree with the chunks. A stale manifest row would make
	// the next delta re-read or skip a file that has not moved.
	refreshed := mustIndex(t, db, root, ignored)
	assert.True(t, refreshed.Empty(), "the row written by edit must satisfy the next build: %+v", refreshed)
}

func TestReindexFileKeepsTheChunkMapAligned(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)
	ignored := config.DefaultIgnoredDirs

	path := filepath.Join(root, "grow.go")
	write(t, path, "func Grow() {}\n")
	mustIndex(t, db, root, ignored)

	// Shrink from several chunks to one, then check both stores agree.
	var big string
	big = "func Grow() {}\n"
	for i := 0; i < config.ChunkSize*2; i++ {
		big += "// filler\n"
	}
	write(t, path, big)
	require.NoError(t, indexer.ReindexFile(db, root, path, ignored))

	chunks := countChunks(t, db, "grow.go")
	require.Greater(t, chunks, 1)

	write(t, path, "func Shrunk() {}\n")
	require.NoError(t, indexer.ReindexFile(db, root, path, ignored))

	assert.Equal(t, 1, countChunks(t, db, "grow.go"))

	var mapped int
	require.NoError(t, db.Get(&mapped, "SELECT COUNT(*) FROM index_chunks WHERE filepath = ?", "grow.go"))
	assert.Equal(t, 1, mapped, "the map must not keep rowids the FTS table no longer has")

	var orphans int
	require.NoError(t, db.Get(&orphans, "SELECT COUNT(*) FROM index_chunks WHERE rowid NOT IN (SELECT rowid FROM chunk_search)"))
	assert.Zero(t, orphans, "every mapped rowid must point at a chunk")
}

func TestReindexFileDropsTheManifestRowOfADeletedFile(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)
	ignored := config.DefaultIgnoredDirs

	path := filepath.Join(root, "gone.go")
	write(t, path, "func Gone() {}\n")
	mustIndex(t, db, root, ignored)

	require.NoError(t, os.Remove(path))
	require.NoError(t, indexer.ReindexFile(db, root, path, ignored))

	var rows int
	require.NoError(t, db.Get(&rows, "SELECT COUNT(*) FROM indexed_files WHERE filepath = ?", "gone.go"))
	assert.Zero(t, rows, "a deleted file must not survive in the manifest")
}

func TestDeltaBuildSkipsLongLineFiles(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)
	ignored := config.DefaultIgnoredDirs

	// Single-line file exceeding config.MaxLineWidth (e.g. 5,000 characters).
	longContent := "// minified " + strings.Repeat("x", config.MaxLineWidth+100) + "\n"
	write(t, filepath.Join(root, "bundle.min.js"), longContent)

	t.Run("reports file as skipped rather than added/indexed", func(t *testing.T) {
		refreshed := mustIndex(t, db, root, ignored)
		assert.Equal(t, indexer.Refreshed{Skipped: 1}, refreshed)
		assert.Empty(t, chunkRowIDs(t, db, "bundle.min.js"), "skipped files must have no chunk rows")
		assert.Zero(t, countChunks(t, db, "bundle.min.js"))

		// Manifest must still record the file so subsequent walks skip re-reading it.
		var count int
		require.NoError(t, db.Get(&count, "SELECT COUNT(*) FROM indexed_files WHERE filepath = ?", "bundle.min.js"))
		assert.Equal(t, 1, count, "skipped files must be present in the manifest to prevent re-reading on every search")
	})

	t.Run("unchanged skipped file causes no work and takes no write lock", func(t *testing.T) {
		refreshed := mustIndex(t, db, root, ignored)
		assert.True(t, refreshed.Empty(), "an unchanged skipped file must not trigger any delta work: %+v", refreshed)
	})

	t.Run("re-chunked when reformatted to short lines", func(t *testing.T) {
		write(t, filepath.Join(root, "bundle.min.js"), "function normal() {}\n// short line\n")
		touch(t, filepath.Join(root, "bundle.min.js"))

		refreshed := mustIndex(t, db, root, ignored)
		assert.Equal(t, indexer.Refreshed{Changed: 1}, refreshed)
		assert.NotEmpty(t, chunkRowIDs(t, db, "bundle.min.js"), "reformatted file must now be indexed")
	})

	t.Run("chunks removed when modified into a long-line file", func(t *testing.T) {
		write(t, filepath.Join(root, "bundle.min.js"), longContent)
		touch(t, filepath.Join(root, "bundle.min.js"))

		refreshed := mustIndex(t, db, root, ignored)
		assert.Equal(t, indexer.Refreshed{Skipped: 1}, refreshed)
		assert.Empty(t, chunkRowIDs(t, db, "bundle.min.js"), "chunks must be dropped when file becomes minified")
	})
}

func TestReindexFileSkipsLongLines(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)
	ignored := config.DefaultIgnoredDirs

	path := filepath.Join(root, "code.js")
	write(t, path, "function normal() {}\n")
	mustIndex(t, db, root, ignored)
	require.NotEmpty(t, chunkRowIDs(t, db, "code.js"))

	// Rewrite to have a line exceeding config.MaxLineWidth
	longContent := "function long() {" + strings.Repeat("z", config.MaxLineWidth+50) + "}"
	write(t, path, longContent)
	require.NoError(t, indexer.ReindexFile(db, root, path, ignored))

	assert.Empty(t, chunkRowIDs(t, db, "code.js"), "chunks must be removed when file exceeds max line width")

	var rows int
	require.NoError(t, db.Get(&rows, "SELECT COUNT(*) FROM indexed_files WHERE filepath = ?", "code.js"))
	assert.Equal(t, 1, rows, "manifest row must survive to record current hash/size/mtime")
}
