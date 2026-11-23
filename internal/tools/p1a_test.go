package tools

import (
	"context"
	"errors"
	"fmt"
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

func TestPerformRead64KBCapAndEditCompatibility(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, config.DBFile)
	db, err := database.Open(dbPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	defer db.Close()

	var b strings.Builder
	for i := 1; i <= 40000; i++ {
		fmt.Fprintf(&b, "line_%05d := \"padding_value_for_64kb_payload_cap_test\"\n", i)
	}
	filePath := filepath.Join(root, "huge.go")
	require.NoError(t, os.WriteFile(filePath, []byte(b.String()), 0o644))

	res, err := PerformRead(root, "huge.go", 1, 40000)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(res.Content), config.MaxReadBytes)
	assert.Equal(t, 1, res.StartLine)
	assert.Greater(t, res.EndLine, 1)
	assert.Less(t, res.EndLine, 40000)
	assert.Equal(t, 40000, res.TotalLines)
	assert.True(t, res.Truncated)
	assert.Equal(t, res.EndLine+1, res.NextStart)
	assert.True(t, strings.HasSuffix(res.Content, "\n"), "truncation must occur on a whole-line boundary")

	editRes, err := PerformEdit(db, root, "huge.go", 2, 2, "line_00002 := \"edited\"", res.Hash, config.DefaultIgnoredDirs)
	require.NoError(t, err)
	assert.Equal(t, "success", editRes.Status)
}

func TestPerformReadNextStartVisibility(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "medium.go"), []byte(b.String()), 0o644))

	first, err := PerformRead(root, "medium.go", 1, 60)
	require.NoError(t, err)
	assert.Equal(t, 1, first.StartLine)
	assert.Equal(t, 60, first.EndLine)
	assert.Equal(t, 500, first.TotalLines)
	assert.False(t, first.Truncated)
	assert.Equal(t, 61, first.NextStart)

	last, err := PerformRead(root, "medium.go", 450, 500)
	require.NoError(t, err)
	assert.Equal(t, 450, last.StartLine)
	assert.Equal(t, 500, last.EndLine)
	assert.Equal(t, 500, last.TotalLines)
	assert.False(t, last.Truncated)
	assert.Zero(t, last.NextStart)
}

func TestPerformSearchDefensiveFilterOnReadOnlyLeakedDB(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, config.DBFile)

	db, err := database.Open(dbPath, database.OpenOptions{
		Chunking:       indexer.Chunking(),
		IndexSensitive: true,
	})
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("SHARED_TOKEN=secret_a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "config", "prod.env"), []byte("SHARED_TOKEN=secret_b\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nconst SHARED_TOKEN = \"safe\"\n"), 0o644))

	_, err = indexer.IndexProjectWithOptions(db, root, indexer.IndexOptions{
		IgnoredDirs:    config.DefaultIgnoredDirs,
		IndexSensitive: true,
	})
	require.NoError(t, err)
	require.NoError(t, db.Close())

	roDB, err := sqlx.Connect("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	require.NoError(t, err)
	defer roDB.Close()

	resp, err := PerformSearch(roDB, root, "SHARED_TOKEN", SearchOptions{
		NoRefresh:      true,
		IndexSensitive: false,
		Limit:          5,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.FilteredSensitive)
	assert.Equal(t, 1, resp.Total)
	assert.False(t, resp.Truncated)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, "main.go", resp.Results[0].Filepath)
}

func TestPerformEditPreflightAndPostWriteReindexFailure(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, config.DBFile)
	mainPath := filepath.Join(root, "main.go")
	original := "package main\nfunc Original() {}\n"
	require.NoError(t, os.WriteFile(mainPath, []byte(original), 0o644))

	db, err := database.Open(dbPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)

	t.Run("refuses edit on mode=ro DB without mutating file", func(t *testing.T) {
		roDB, err := sqlx.Connect("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
		require.NoError(t, err)
		defer roDB.Close()

		_, err = PerformEdit(roDB, root, "main.go", 2, 2, "func Mutated() {}", "", config.DefaultIgnoredDirs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PROXER_DB")

		got, readErr := os.ReadFile(mainPath)
		require.NoError(t, readErr)
		assert.Equal(t, original, string(got), "file on disk must remain untouched")
	})

	t.Run("fails fast under write lock without mutating file", func(t *testing.T) {
		locker, err := sqlx.Connect("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
		require.NoError(t, err)
		defer locker.Close()

		lockConn, err := locker.Conn(context.Background())
		require.NoError(t, err)
		defer lockConn.Close()
		_, err = lockConn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
		require.NoError(t, err)
		defer lockConn.ExecContext(context.Background(), "ROLLBACK")

		start := time.Now()
		_, err = PerformEdit(db, root, "main.go", 2, 2, "func Mutated() {}", "", config.DefaultIgnoredDirs)
		elapsed := time.Since(start)
		require.Error(t, err)
		assert.Less(t, elapsed, 1500*time.Millisecond)

		got, readErr := os.ReadFile(mainPath)
		require.NoError(t, readErr)
		assert.Equal(t, original, string(got), "file on disk must remain untouched")
	})

	t.Run("reports post-write reindex failure non-fatally", func(t *testing.T) {
		origReindex := reindexFileContent
		reindexFileContent = func(*sqlx.DB, string, string, []byte, indexer.IndexOptions) error {
			return errors.New("simulated transient SQLITE_BUSY")
		}
		defer func() { reindexFileContent = origReindex }()

		res, err := PerformEdit(db, root, "main.go", 2, 2, "func Updated() {}", "", config.DefaultIgnoredDirs)
		require.NoError(t, err, "post-write reindex failure must not return exit 1")
		assert.Equal(t, "success", res.Status)
		assert.True(t, res.ReindexFailed)
		assert.Contains(t, res.ReindexError, "simulated transient SQLITE_BUSY")

		got, readErr := os.ReadFile(mainPath)
		require.NoError(t, readErr)
		assert.Contains(t, string(got), "func Updated() {}")
	})

	require.NoError(t, db.Close())

	t.Run("refuses edit on chmod 0444 DB without mutating file", func(t *testing.T) {
		beforeBytes, err := os.ReadFile(mainPath)
		require.NoError(t, err)

		require.NoError(t, os.Chmod(dbPath, 0o444))
		defer os.Chmod(dbPath, 0o600)

		roFileDB, err := sqlx.Connect("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
		require.NoError(t, err)
		defer roFileDB.Close()

		_, err = PerformEdit(roFileDB, root, "main.go", 2, 2, "func Blocked() {}", "", config.DefaultIgnoredDirs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PROXER_DB")

		afterBytes, readErr := os.ReadFile(mainPath)
		require.NoError(t, readErr)
		assert.Equal(t, string(beforeBytes), string(afterBytes))
	})
}
