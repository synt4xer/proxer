package tools_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
	"github.com/synt4xer/proxer/internal/tools"
)

const mockCode = `package main
import "fmt"

func ProcessData() {
	fmt.Println("Processing...")
	// We need to change this logic later
	return
}
`

// newToolWorkspace builds an isolated indexed project. Nothing chdirs: every
// tool call receives the root explicitly, which is how a harness invokes it.
func newToolWorkspace(t *testing.T) (string, *sqlx.DB) {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(mockCode), 0o644))

	db, err := indexer.InitDB(filepath.Join(root, config.DBFile))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	require.NoError(t, indexer.IndexProject(db, root))
	return root, db
}

func TestToolsSuite(t *testing.T) {
	root, db := newToolWorkspace(t)

	t.Run("PerformSearch", func(t *testing.T) {
		resultJSON, err := tools.PerformSearch(db, "ProcessData")
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "main.go")
		assert.Contains(t, resultJSON, "ProcessData")

		emptyJSON, err := tools.PerformSearch(db, "NonExistentKeyword")
		require.NoError(t, err)
		assert.Equal(t, "[]", emptyJSON)
	})

	t.Run("PerformSearchSanitizesInput", func(t *testing.T) {
		// Raw FTS5 syntax must never reach the parser as an error.
		for _, keyword := range []string{"ProcessData(", "AND", "*", `"quoted`, "middleware?"} {
			_, err := tools.PerformSearch(db, keyword)
			require.NoError(t, err, "keyword %q produced an error", keyword)
		}

		resultJSON, err := tools.PerformSearch(db, "()*&^%")
		require.NoError(t, err)
		assert.Equal(t, "[]", resultJSON)
	})

	t.Run("PerformRead", func(t *testing.T) {
		resultJSON, err := tools.PerformRead(root, "main.go", 4, 5)
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "func ProcessData()")
		assert.Contains(t, resultJSON, "fmt.Println")

		_, err = tools.PerformRead(root, "missing.go", 1, 5)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("PerformReadResolvesAgainstRootNotWorkingDirectory", func(t *testing.T) {
		// A harness may spawn proxer from an unrelated directory.
		t.Chdir(t.TempDir())

		resultJSON, err := tools.PerformRead(root, "main.go", 4, 4)
		require.NoError(t, err, "relative paths must resolve against the project root")
		assert.Contains(t, resultJSON, "func ProcessData()")
		assert.Contains(t, resultJSON, `"filepath":"main.go"`, "the path must be echoed as given")
	})

	t.Run("PerformReadAcceptsAbsolutePaths", func(t *testing.T) {
		absolute := filepath.Join(root, "main.go")
		resultJSON, err := tools.PerformRead(root, absolute, 1, 1)
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "package main")
	})

	t.Run("PerformEdit", func(t *testing.T) {
		newContent := "\tfmt.Println(\"Data Processed Successfully\")\n\treturn true"

		resultJSON, err := tools.PerformEdit(db, root, "main.go", 5, 7, newContent)
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "success")

		updatedBytes, err := os.ReadFile(filepath.Join(root, "main.go"))
		require.NoError(t, err)
		updatedContent := string(updatedBytes)
		assert.Contains(t, updatedContent, "Data Processed Successfully")
		assert.NotContains(t, updatedContent, "We need to change this logic later")

		searchResult, err := tools.PerformSearch(db, "Successfully")
		require.NoError(t, err)
		assert.Contains(t, searchResult, "main.go", "auto-reindex failed to register new content")

		// The stale line must be gone from the index too, not only from disk.
		staleResult, err := tools.PerformSearch(db, "Processing")
		require.NoError(t, err)
		assert.NotContains(t, staleResult, "We need to change this logic later")
	})

	t.Run("PerformEditRejectsOutOfRange", func(t *testing.T) {
		_, err := tools.PerformEdit(db, root, "main.go", 900, 901, "x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid line range")
	})
}
