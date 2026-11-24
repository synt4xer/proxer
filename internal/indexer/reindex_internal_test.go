package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/filesystem"
)

func TestRefreshedToSummaryAndRecordSkip(t *testing.T) {
	var r Refreshed
	r.recordSkip(filesystem.SkipNone)
	assert.Zero(t, r.Skipped)
	assert.Nil(t, r.SkippedByReason)

	r.recordSkip(filesystem.SkipIgnoredFile)
	r.recordSkip(filesystem.SkipExtension)
	r.recordSkip(filesystem.SkipSensitive)
	r.recordSkip(filesystem.SkipMinified)
	r.recordSkip(filesystem.SkipSize)
	r.Added = 2
	r.Changed = 1
	r.Removed = 3
	r.IgnoredDirs = 4

	summary := r.ToSummary()
	assert.Equal(t, domain.RefreshSummary{
		Added:       2,
		Changed:     1,
		Removed:     3,
		Skipped:     5,
		IgnoredDirs: 4,
		SkippedByReason: &domain.SkippedCounts{
			IgnoredFiles: 1,
			Extension:    1,
			Sensitive:    1,
			Minified:     1,
			Size:         1,
		},
	}, summary)
}

func TestReindexFileContentVariants(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, config.DBFile)
	db, err := database.Open(dbPath, database.OpenOptions{Chunking: Chunking(), IndexSensitive: true})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	srcPath := filepath.Join(root, "app.go")
	require.NoError(t, os.WriteFile(srcPath, []byte("func Initial() {}\n"), 0o644))
	_, err = IndexProject(db, root, config.DefaultIgnoredDirs)
	require.NoError(t, err)

	// ReindexFileContent indexes supplied bytes without re-reading disk.
	updated := []byte("func SuppliedBytes() {}\n")
	require.NoError(t, os.WriteFile(srcPath, updated, 0o644))
	require.NoError(t, ReindexFileContent(db, root, srcPath, updated, config.DefaultIgnoredDirs))

	var content string
	require.NoError(t, db.Get(&content, "SELECT content FROM chunk_search WHERE filepath = ?", "app.go"))
	assert.Contains(t, content, "SuppliedBytes")

	// Minified content drops chunks but keeps the manifest row.
	minified := []byte(strings.Repeat("x", config.MaxLineWidth+10) + "\n")
	require.NoError(t, os.WriteFile(srcPath, minified, 0o644))
	require.NoError(t, ReindexFileContentWithOptions(db, root, srcPath, minified, IndexOptions{
		IgnoredDirs: config.DefaultIgnoredDirs,
	}))

	var chunks int
	require.NoError(t, db.Get(&chunks, "SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", "app.go"))
	assert.Zero(t, chunks)
	files, err := database.Files(db)
	require.NoError(t, err)
	assert.Contains(t, files, "app.go")

	// Sensitive file reindex with IndexSensitive=true indexes it and resets purge sentinel.
	envPath := filepath.Join(root, ".env")
	envBytes := []byte("SECRET_KEY=demo_value\n")
	require.NoError(t, os.WriteFile(envPath, envBytes, 0o644))
	require.NoError(t, ReindexFileContentWithOptions(db, root, envPath, envBytes, IndexOptions{
		IgnoredDirs:    config.DefaultIgnoredDirs,
		IndexSensitive: true,
	}))
	require.NoError(t, db.Get(&chunks, "SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", ".env"))
	assert.Equal(t, 1, chunks)

	// Reindexing sensitive file with IndexSensitive=false removes it from the index.
	require.NoError(t, ReindexFileContentWithOptions(db, root, envPath, envBytes, IndexOptions{
		IgnoredDirs:    config.DefaultIgnoredDirs,
		IndexSensitive: false,
	}))
	require.NoError(t, db.Get(&chunks, "SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", ".env"))
	assert.Zero(t, chunks)

	// Deleted file with pre-supplied content still removes rows via os.Stat IsNotExist.
	require.NoError(t, os.Remove(srcPath))
	require.NoError(t, ReindexFileContent(db, root, srcPath, updated, config.DefaultIgnoredDirs))
	files, err = database.Files(db)
	require.NoError(t, err)
	assert.NotContains(t, files, "app.go")
}

func TestIndexProjectWithBusyTimeoutAndMinifiedEdit(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, config.DBFile)
	db, err := database.Open(dbPath, database.OpenOptions{Chunking: Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	filePath := filepath.Join(root, "main.go")
	require.NoError(t, os.WriteFile(filePath, []byte("package main\nfunc Hello() {}\n"), 0o644))

	// Exercises BusyTimeoutMS > 0 path with explicit context.
	refreshed, err := IndexProjectWithOptions(db, root, IndexOptions{
		Ctx:           context.Background(),
		IgnoredDirs:   config.DefaultIgnoredDirs,
		BusyTimeoutMS: database.SearchRefreshBusyTimeoutMS,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, refreshed.Added)

	// Rewriting an indexed file to a minified blob drops chunks and records SkipMinified.
	minified := strings.Repeat("a", config.MaxLineWidth+20) + "\n"
	require.NoError(t, os.WriteFile(filePath, []byte(minified), 0o644))

	refreshed, err = IndexProjectWithOptions(db, root, IndexOptions{
		IgnoredDirs:   config.DefaultIgnoredDirs,
		BusyTimeoutMS: database.SearchRefreshBusyTimeoutMS,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, refreshed.Changed)
	require.NotNil(t, refreshed.SkippedByReason)
	assert.Equal(t, 1, refreshed.SkippedByReason.Minified)

	var chunks int
	require.NoError(t, db.Get(&chunks, "SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", "main.go"))
	assert.Zero(t, chunks)
}
