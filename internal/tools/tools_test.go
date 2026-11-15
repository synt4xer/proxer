package tools_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
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
		resultJSON, err := tools.PerformSearch(db, root, "ProcessData")
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "main.go")
		assert.Contains(t, resultJSON, "ProcessData")

		var results []domain.SearchResultJSON
		require.NoError(t, json.Unmarshal([]byte(resultJSON), &results))
		require.NotEmpty(t, results)
		assert.Equal(t, root, results[0].Root, "each match must name the index it came from")

		emptyJSON, err := tools.PerformSearch(db, root, "NonExistentKeyword")
		require.NoError(t, err)
		assert.Equal(t, "[]", emptyJSON)
	})

	t.Run("PerformSearchSanitizesInput", func(t *testing.T) {
		// Raw FTS5 syntax must never reach the parser as an error.
		for _, keyword := range []string{"ProcessData(", "AND", "*", `"quoted`, "middleware?"} {
			_, err := tools.PerformSearch(db, root, keyword)
			require.NoError(t, err, "keyword %q produced an error", keyword)
		}

		resultJSON, err := tools.PerformSearch(db, root, "()*&^%")
		require.NoError(t, err)
		assert.Equal(t, "[]", resultJSON)
	})

	t.Run("PerformRead", func(t *testing.T) {
		resultJSON, err := tools.PerformRead(root, "main.go", 4, 5)
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "func ProcessData()")
		assert.Contains(t, resultJSON, "fmt.Println")

		var result domain.ReadResultJSON
		require.NoError(t, json.Unmarshal([]byte(resultJSON), &result))
		assert.Equal(t, root, result.Root, "a read result must name the index it came from")

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

	t.Run("PerformReadAcceptsAbsolutePathsInsideTheRoot", func(t *testing.T) {
		absolute := filepath.Join(root, "main.go")
		resultJSON, err := tools.PerformRead(root, absolute, 1, 1)
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "package main")
	})

	t.Run("PerformReadRejectsPathsOutsideTheRoot", func(t *testing.T) {
		sibling := filepath.Join(filepath.Dir(root), "other-project", "secret.go")
		require.NoError(t, os.MkdirAll(filepath.Dir(sibling), 0o755))
		require.NoError(t, os.WriteFile(sibling, []byte("package other\n"), 0o644))

		_, err := tools.PerformRead(root, "../other-project/secret.go", 1, 1)
		require.Error(t, err, "a relative path must not climb out of the root")
		assert.Contains(t, err.Error(), "outside the project root")

		_, err = tools.PerformRead(root, sibling, 1, 1)
		require.Error(t, err, "an absolute path must not name another project")
		assert.Contains(t, err.Error(), "outside the project root")
	})

	t.Run("PerformReadRejectsSiblingWithASharedPrefix", func(t *testing.T) {
		// "/tmp/app-other" must not pass a check written for "/tmp/app".
		sibling := root + "-other"
		require.NoError(t, os.MkdirAll(sibling, 0o755))
		t.Cleanup(func() { os.RemoveAll(sibling) })
		require.NoError(t, os.WriteFile(filepath.Join(sibling, "x.go"), []byte("package x\n"), 0o644))

		_, err := tools.PerformRead(root, filepath.Join(sibling, "x.go"), 1, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside the project root")
	})

	t.Run("PerformReadRejectsSymlinksPointingOutsideTheRoot", func(t *testing.T) {
		outsideDir := t.TempDir()
		target := filepath.Join(outsideDir, "secret.go")
		require.NoError(t, os.WriteFile(target, []byte("package secret\n"), 0o644))

		link := filepath.Join(root, "linked.go")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		_, err := tools.PerformRead(root, "linked.go", 1, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside the project root")
	})

	t.Run("PerformEditRejectsPathsOutsideTheRoot", func(t *testing.T) {
		sibling := filepath.Join(filepath.Dir(root), "edit-target", "keep.go")
		require.NoError(t, os.MkdirAll(filepath.Dir(sibling), 0o755))
		require.NoError(t, os.WriteFile(sibling, []byte("package keep\n"), 0o644))

		_, err := tools.PerformEdit(db, root, sibling, 1, 1, "package hijacked")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside the project root")

		untouched, readErr := os.ReadFile(sibling)
		require.NoError(t, readErr)
		assert.Equal(t, "package keep\n", string(untouched), "the file must not be written")

		var foreign int
		require.NoError(t, db.Get(&foreign, "SELECT COUNT(*) FROM chunk_search WHERE filepath IN (?, ?)", "keep.go", "../edit-target/keep.go"))
		assert.Zero(t, foreign, "a rejected edit must not index the file")
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

		searchResult, err := tools.PerformSearch(db, root, "Successfully")
		require.NoError(t, err)
		assert.Contains(t, searchResult, "main.go", "auto-reindex failed to register new content")

		// The stale line must be gone from the index too, not only from disk.
		staleResult, err := tools.PerformSearch(db, root, "Processing")
		require.NoError(t, err)
		assert.NotContains(t, staleResult, "We need to change this logic later")
	})

	t.Run("PerformEditReindexesOnlyTheEditedFile", func(t *testing.T) {
		// A sentinel row that no full rebuild would survive: if edit still
		// rebuilt the whole index, this row would vanish.
		_, err := db.Exec(
			"INSERT INTO chunk_search (id, filepath, start_line, end_line, content) VALUES (?, ?, ?, ?, ?)",
			"other.go:1-1", "other.go", 1, 1, "sentinel xyzzy",
		)
		require.NoError(t, err)

		_, err = tools.PerformEdit(db, root, "main.go", 1, 1, "package main // touched")
		require.NoError(t, err)

		var untouched int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", "other.go").Scan(&untouched))
		assert.Equal(t, 1, untouched, "editing one file must not rebuild the rest of the index")

		var counted int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", "main.go").Scan(&counted))
		assert.Equal(t, 1, counted, "the edited file must not be duplicated in the index")
	})

	t.Run("PerformEditRejectsOutOfRange", func(t *testing.T) {
		_, err := tools.PerformEdit(db, root, "main.go", 900, 901, "x")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid line range")
	})
}

func TestJSONOutputIsNotHTMLEscaped(t *testing.T) {
	root, db := newToolWorkspace(t)

	page := filepath.Join(root, "page.html")
	require.NoError(t, os.WriteFile(page, []byte("<div>a & b</div>\n"), 0o644))
	require.NoError(t, indexer.IndexProject(db, root))

	t.Run("searchKeepsSnippetTagsLiteral", func(t *testing.T) {
		resultJSON, err := tools.PerformSearch(db, root, "ProcessData")
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "<b>ProcessData</b>")
		assert.NotContains(t, resultJSON, `\u003c`)
	})

	t.Run("readKeepsContentLiteral", func(t *testing.T) {
		resultJSON, err := tools.PerformRead(root, "page.html", 1, 1)
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "<div>a & b</div>")
		assert.NotContains(t, resultJSON, `\u003c`)
		assert.NotContains(t, resultJSON, `\u0026`)
	})
}

func TestWriteFileAtomicallyKeepsTheContract(t *testing.T) {
	root, db := newToolWorkspace(t)

	t.Run("replacesContentWithoutLeavingTemporaryFiles", func(t *testing.T) {
		_, err := tools.PerformEdit(db, root, "main.go", 1, 1, "package edited")
		require.NoError(t, err)

		leftovers, err := filepath.Glob(filepath.Join(root, ".proxer-edit-*"))
		require.NoError(t, err)
		assert.Empty(t, leftovers, "the temporary file must not survive the rename")

		content, err := os.ReadFile(filepath.Join(root, "main.go"))
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(content), "package edited\n"))
	})

	t.Run("preservesFilePermissions", func(t *testing.T) {
		path := filepath.Join(root, "script.sh")
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho one\necho two\n"), 0o750))
		require.NoError(t, os.Chmod(path, 0o750))

		_, err := tools.PerformEdit(db, root, "script.sh", 2, 2, "echo edited")
		require.NoError(t, err)

		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o750), info.Mode().Perm(), "the rename must not reset the mode")
	})

	t.Run("keepsASymlinkASymlink", func(t *testing.T) {
		target := filepath.Join(root, "real.go")
		require.NoError(t, os.WriteFile(target, []byte("package real\nvar a = 1\n"), 0o644))
		link := filepath.Join(root, "link.go")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		_, err := tools.PerformEdit(db, root, "link.go", 2, 2, "var a = 2")
		require.NoError(t, err)

		info, err := os.Lstat(link)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&os.ModeSymlink, "editing through a link must not replace it")

		updated, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Contains(t, string(updated), "var a = 2")
	})
}
