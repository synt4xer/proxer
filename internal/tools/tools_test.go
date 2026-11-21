package tools_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
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

	db, err := database.Open(filepath.Join(root, config.DBFile), indexer.Chunking())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	mustIndex(t, db, root, config.DefaultIgnoredDirs)
	return root, db
}

func TestToolsSuite(t *testing.T) {
	root, db := newToolWorkspace(t)

	t.Run("PerformSearch", func(t *testing.T) {
		response := searchAll(t, db, root, "ProcessData")
		require.NotEmpty(t, response.Results)
		assert.Equal(t, root, response.Root, "a search must name the index it came from")
		assert.Equal(t, root, response.Results[0].Root, "each match must name the index it came from")
		assert.Equal(t, "main.go", response.Results[0].Filepath)

		empty := searchAll(t, db, root, "NonExistentKeyword")
		assert.Empty(t, empty.Results, "a term with no matches must return an empty result list")
		assert.NotNil(t, empty.Refreshed, "a default search must say what it refreshed")
	})

	t.Run("PerformSearchSanitizesInput", func(t *testing.T) {
		// Raw FTS5 syntax must never reach the parser as an error.
		for _, keyword := range []string{"ProcessData(", "AND", "*", `"quoted`, "middleware?"} {
			_, err := tools.PerformSearch(db, root, keyword, toolSearchOptions)
			require.NoError(t, err, "keyword %q produced an error", keyword)
		}

		punctuation := searchAll(t, db, root, "()*&^%")
		assert.Empty(t, punctuation.Results, "punctuation-only input must not reach the parser")
	})

	t.Run("PerformRead", func(t *testing.T) {
		resultJSON, err := tools.PerformRead(root, "main.go", 4, 5)
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "func ProcessData()")
		assert.Contains(t, resultJSON, "fmt.Println")

		var result domain.ReadResultJSON
		require.NoError(t, json.Unmarshal([]byte(resultJSON), &result))
		assert.Equal(t, root, result.Root, "a read result must name the index it came from")
		assert.Len(t, result.Hash, 64, "a read result must carry a sha256 of the file")

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

		_, err := tools.PerformEdit(db, root, sibling, 1, 1, "package hijacked", "", config.DefaultIgnoredDirs)
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

		resultJSON, err := tools.PerformEdit(db, root, "main.go", 5, 7, newContent, "", config.DefaultIgnoredDirs)
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "success")

		updatedBytes, err := os.ReadFile(filepath.Join(root, "main.go"))
		require.NoError(t, err)
		updatedContent := string(updatedBytes)
		assert.Contains(t, updatedContent, "Data Processed Successfully")
		assert.NotContains(t, updatedContent, "We need to change this logic later")

		searchResult := searchJSON(t, db, root, "Successfully")
		assert.Contains(t, searchResult, "main.go", "auto-reindex failed to register new content")

		// The stale line must be gone from the index too, not only from disk.
		staleResult := searchJSON(t, db, root, "Processing")
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

		_, err = tools.PerformEdit(db, root, "main.go", 1, 1, "package main // touched", "", config.DefaultIgnoredDirs)
		require.NoError(t, err)

		var untouched int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", "other.go").Scan(&untouched))
		assert.Equal(t, 1, untouched, "editing one file must not rebuild the rest of the index")

		var counted int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM chunk_search WHERE filepath = ?", "main.go").Scan(&counted))
		assert.Equal(t, 1, counted, "the edited file must not be duplicated in the index")
	})

	t.Run("PerformEditRejectsOutOfRange", func(t *testing.T) {
		_, err := tools.PerformEdit(db, root, "main.go", 900, 901, "x", "", config.DefaultIgnoredDirs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid line range")
	})
}

func TestPerformEditRejectsAStaleHash(t *testing.T) {
	root, db := newToolWorkspace(t)

	readHash := func(t *testing.T) string {
		t.Helper()
		resultJSON, err := tools.PerformRead(root, "main.go", 5, 7)
		require.NoError(t, err)
		var result domain.ReadResultJSON
		require.NoError(t, json.Unmarshal([]byte(resultJSON), &result))
		require.NotEmpty(t, result.Hash)
		return result.Hash
	}

	t.Run("acceptsTheHashFromTheLastRead", func(t *testing.T) {
		_, err := tools.PerformEdit(db, root, "main.go", 1, 1, "package main", readHash(t), config.DefaultIgnoredDirs)
		require.NoError(t, err)
	})

	t.Run("rejectsAnUnknownHash", func(t *testing.T) {
		_, err := tools.PerformEdit(db, root, "main.go", 1, 1, "package hijacked", "deadbeef", config.DefaultIgnoredDirs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "changed since it was read")

		content, readErr := os.ReadFile(filepath.Join(root, "main.go"))
		require.NoError(t, readErr)
		assert.NotContains(t, string(content), "hijacked", "a stale edit must not touch the file")
	})

	t.Run("rejectsAChangeOutsideTheEditedRange", func(t *testing.T) {
		hash := readHash(t)

		require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n// changed elsewhere\n"), 0o644))

		_, err := tools.PerformEdit(db, root, "main.go", 1, 1, "package main", hash, config.DefaultIgnoredDirs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "changed since it was read")
	})

	t.Run("editingWithoutAHashStillWorks", func(t *testing.T) {
		_, err := tools.PerformEdit(db, root, "main.go", 1, 1, "package main", "", config.DefaultIgnoredDirs)
		require.NoError(t, err)
	})
}

// TestSearchFindsAPathOnlyMatch covers the file the field report could not
// find: its identity lives in the path, so a token that appears nowhere in the
// body still has to reach it.
func TestSearchFindsAPathOnlyMatch(t *testing.T) {
	root, db := newToolWorkspace(t)

	migration := filepath.Join(root, "db", "migrations", "mysql", "000001_init.up.sql")
	require.NoError(t, os.MkdirAll(filepath.Dir(migration), 0o755))
	require.NoError(t, os.WriteFile(migration, []byte("CREATE TABLE users (id INTEGER);\n"), 0o644))
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	for _, keyword := range []string{"000001", "mysql", "up"} {
		response := searchAll(t, db, root, keyword)
		require.NotEmpty(t, response.Results, "keyword %q must find the migration by its path", keyword)
		assert.Equal(t, "db/migrations/mysql/000001_init.up.sql", response.Results[0].Filepath)
		assert.Equal(t, "path", response.Results[0].Match, "keyword %q matched the path, not the body", keyword)
	}

	fromContent := searchAll(t, db, root, "CREATE")
	require.NotEmpty(t, fromContent.Results)
	assert.Equal(t, "content", fromContent.Results[0].Match)
}

// TestSearchDedupesByFile covers the wasted slots in the field report: the same
// file occupied two of five results, so five slots described one file instead
// of five.
func TestSearchDedupesByFile(t *testing.T) {
	root, db := newToolWorkspace(t)

	var body strings.Builder
	body.WriteString("package big\n")
	for i := range 200 {
		fmt.Fprintf(&body, "// Marker line %d\n", i)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "big.go"), []byte(body.String()), 0o644))
	for _, name := range []string{"one.go", "two.go"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", name), []byte("package pkg\n\n// Marker here\n"), 0o644))
	}
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	response := searchAll(t, db, root, "Marker")
	require.Len(t, response.Results, 3, "five slots must describe three files, not one file three times")

	seen := make(map[string]struct{}, len(response.Results))
	for _, result := range response.Results {
		_, duplicate := seen[result.Filepath]
		assert.False(t, duplicate, "%s appeared more than once", result.Filepath)
		seen[result.Filepath] = struct{}{}
	}
	assert.Contains(t, seen, "pkg/big.go", "the many-chunked file must still be represented")
}

// TestSearchDemotesDocsAndAssets covers the ranking waste in the field report:
// 69 markdown chunks crowded the five slots, a minified bundle was indexed
// and returned for `func`, and test files calling a function repeatedly
// outranked the production file defining it.
func TestSearchDemotesDocsAndAssets(t *testing.T) {
	root, db := newToolWorkspace(t)

	files := map[string]string{
		filepath.Join("internal", "app.go"):      "package app\n\nfunc Demoted() {}\n",
		filepath.Join("internal", "app_test.go"): "package app_test\n\nfunc TestDemoted() { Demoted(); Demoted(); Demoted() }\n",
		"NOTES.md":                               "# Demoted\n",
		filepath.Join("static", "htmx.min.js"):   "function Demoted(){}\n",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	all := searchAll(t, db, root, "Demoted")
	require.Len(t, all.Results, 4)
	assert.Equal(t, []string{"internal/app.go", "internal/app_test.go", "NOTES.md", "static/htmx.min.js"}, resultPaths(all),
		"the default must lead with production code, then tests, then docs, then generated assets")

	code := searchWith(t, db, root, "Demoted", tools.SearchOptions{Kind: "code"})
	require.Len(t, code.Results, 2)
	assert.Equal(t, []string{"internal/app.go", "internal/app_test.go"}, resultPaths(code),
		"kind=code must filter out docs/assets while ranking production code before tests")

	docs := searchWith(t, db, root, "Demoted", tools.SearchOptions{Kind: "docs"})
	require.Len(t, docs.Results, 1)
	assert.Equal(t, []string{"NOTES.md"}, resultPaths(docs))

	_, err := tools.PerformSearch(db, root, "Demoted", tools.SearchOptions{Kind: "tests"})
	require.Error(t, err, "an unknown kind must be refused, not silently treated as all")
	assert.Contains(t, err.Error(), "invalid kind")
}

// TestSearchLimitAndTotal pins the honesty of a capped answer: the caller can
// ask for more, sees how much matched, and is told when the list it got is not
// everything the query found.
func TestSearchLimitAndTotal(t *testing.T) {
	root, db := newToolWorkspace(t)

	for i := range 12 {
		path := filepath.Join(root, "pkg", fmt.Sprintf("file%02d.go", i))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("package pkg\n\nfunc SharedThing() {}\n"), 0o644))
	}
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	defaults := searchAll(t, db, root, "SharedThing")
	assert.Len(t, defaults.Results, 5, "the default limit must stay a context guard")
	assert.Equal(t, 12, defaults.Total, "total must count the whole match, not the returned slice")
	assert.True(t, defaults.Truncated, "a capped answer must say it was capped")

	wide := searchWith(t, db, root, "SharedThing", tools.SearchOptions{Limit: 20})
	assert.Len(t, wide.Results, 12)
	assert.Equal(t, 12, wide.Total)
	assert.False(t, wide.Truncated, "a complete answer must not claim truncation")

	clamped := searchWith(t, db, root, "SharedThing", tools.SearchOptions{Limit: 500})
	assert.Len(t, clamped.Results, 12, "a limit above the maximum must clamp, not error")
	assert.False(t, clamped.Truncated)

	missing := searchWith(t, db, root, "NoSuchTokenAnywhere", tools.SearchOptions{Limit: 20})
	assert.Empty(t, missing.Results)
	assert.Zero(t, missing.Total)
	assert.False(t, missing.Truncated)
}

// TestSearchSurvivesADocSaturatedWindow covers the case demotion alone cannot
// fix: small, dense documentation outranks a single mention inside a large
// source file, fills the whole ranked window, and leaves the partition with no
// code to promote.
func TestSearchSurvivesADocSaturatedWindow(t *testing.T) {
	root, db := newToolWorkspace(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	for i := range 250 {
		var doc strings.Builder
		for range 4 {
			doc.WriteString("Crowded Crowded Crowded Crowded Crowded marker\n")
		}
		name := filepath.Join(root, "docs", fmt.Sprintf("note%03d.md", i))
		require.NoError(t, os.WriteFile(name, []byte(doc.String()), 0o644))
	}

	var source strings.Builder
	source.WriteString("package app\n")
	for i := range 200 {
		fmt.Fprintf(&source, "// line %d\n", i)
	}
	source.WriteString("func Crowded() {}\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "internal"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "app.go"), []byte(source.String()), 0o644))
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	// The docs really do outrank the code: an explicit docs query fills the
	// five slots without reaching app.go.
	docs := searchWith(t, db, root, "Crowded", tools.SearchOptions{Kind: "docs"})
	require.Len(t, docs.Results, 5)
	assert.NotContains(t, resultPaths(docs), "internal/app.go")

	all := searchAll(t, db, root, "Crowded")
	require.NotEmpty(t, all.Results)
	assert.Equal(t, "internal/app.go", all.Results[0].Filepath,
		"code must lead even when documentation filled the ranked window")
	assert.Equal(t, "content", all.Results[0].Match)

	code := searchWith(t, db, root, "Crowded", tools.SearchOptions{Kind: "code"})
	require.Len(t, code.Results, 1, "an explicit kind must search its own paths, not only filter the docs a shared window returned")
	assert.Equal(t, []string{"internal/app.go"}, resultPaths(code))
}

func TestJSONOutputIsNotHTMLEscaped(t *testing.T) {
	root, db := newToolWorkspace(t)

	page := filepath.Join(root, "page.html")
	require.NoError(t, os.WriteFile(page, []byte("<div>a & b</div>\n"), 0o644))
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	t.Run("searchKeepsSnippetTagsLiteral", func(t *testing.T) {
		resultJSON := searchJSON(t, db, root, "ProcessData")
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
		_, err := tools.PerformEdit(db, root, "main.go", 1, 1, "package edited", "", config.DefaultIgnoredDirs)
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

		_, err := tools.PerformEdit(db, root, "script.sh", 2, 2, "echo edited", "", config.DefaultIgnoredDirs)
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

		_, err := tools.PerformEdit(db, root, "link.go", 2, 2, "var a = 2", "", config.DefaultIgnoredDirs)
		require.NoError(t, err)

		info, err := os.Lstat(link)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&os.ModeSymlink, "editing through a link must not replace it")

		updated, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Contains(t, string(updated), "var a = 2")
	})
}

// toolSearchOptions is the default a harness gets: refresh on, project skip
// list in force.
var toolSearchOptions = tools.SearchOptions{IgnoredDirs: config.DefaultIgnoredDirs}

func searchJSON(t *testing.T, db *sqlx.DB, root, keyword string) string {
	t.Helper()

	resultJSON, err := tools.PerformSearch(db, root, keyword, toolSearchOptions)
	require.NoError(t, err)
	return resultJSON
}

func searchAll(t *testing.T, db *sqlx.DB, root, keyword string) domain.SearchResponse {
	t.Helper()

	return searchWith(t, db, root, keyword, toolSearchOptions)
}

func searchWith(t *testing.T, db *sqlx.DB, root, keyword string, opts tools.SearchOptions) domain.SearchResponse {
	t.Helper()

	opts.IgnoredDirs = config.DefaultIgnoredDirs
	resultJSON, err := tools.PerformSearch(db, root, keyword, opts)
	require.NoError(t, err)

	var response domain.SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resultJSON), &response))
	return response
}

func resultPaths(response domain.SearchResponse) []string {
	out := make([]string, 0, len(response.Results))
	for _, result := range response.Results {
		out = append(out, result.Filepath)
	}
	return out
}

// mustIndex runs a delta build and fails the test if it errors. The build is
// incremental now, so most call sites only care that it succeeded.
func mustIndex(t *testing.T, db *sqlx.DB, rootPath string, ignoredDirs []string) indexer.Refreshed {
	t.Helper()

	refreshed, err := indexer.IndexProject(db, rootPath, ignoredDirs)
	require.NoError(t, err)
	return refreshed
}

// TestSearchRefreshesTheIndex covers the defect the field report found: a file
// written by a harness tool, not by proxer edit, used to stay invisible until
// someone remembered to run index.
func TestSearchRefreshesTheIndex(t *testing.T) {
	root, db := newToolWorkspace(t)

	outside := filepath.Join(root, "outside.go")
	require.NoError(t, os.WriteFile(outside, []byte("func WrittenByAnotherTool() {}\n"), 0o644))

	response := searchAll(t, db, root, "WrittenByAnotherTool")
	require.Len(t, response.Results, 1, "search must find a file written since the last index")
	assert.NotEmpty(t, response.IndexedAt)
	require.NotNil(t, response.Refreshed)
	assert.Equal(t, 1, response.Refreshed.Added)

	require.NoError(t, os.Remove(outside))

	afterDelete := searchAll(t, db, root, "WrittenByAnotherTool")
	assert.Empty(t, afterDelete.Results, "search must stop returning a file that was deleted")
	require.NotNil(t, afterDelete.Refreshed)
	assert.Equal(t, 1, afterDelete.Refreshed.Removed)
}

// TestSearchWithoutRefreshTrustsTheIndex pins the opt-out. A caller that asked
// for a pure read gets exactly the stored index, and the response says nothing
// about a refresh rather than claiming there was nothing to do.
func TestSearchWithoutRefreshTrustsTheIndex(t *testing.T) {
	root, db := newToolWorkspace(t)

	outside := filepath.Join(root, "outside.go")
	require.NoError(t, os.WriteFile(outside, []byte("func WrittenByAnotherTool() {}\n"), 0o644))

	opts := tools.SearchOptions{NoRefresh: true, IgnoredDirs: config.DefaultIgnoredDirs}
	resultJSON, err := tools.PerformSearch(db, root, "WrittenByAnotherTool", opts)
	require.NoError(t, err)

	var response domain.SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resultJSON), &response))
	assert.Empty(t, response.Results, "without a refresh the new file must stay invisible")
	assert.Nil(t, response.Refreshed, "no refresh must be reported as not attempted, not as zero changes")

	refreshed := searchAll(t, db, root, "WrittenByAnotherTool")
	assert.Len(t, refreshed.Results, 1, "the same query with a refresh must find it")
}

func TestPerformReadLongLines(t *testing.T) {
	root, db := newToolWorkspace(t)

	// 100 KB single-line file: bufio.Scanner aborted at 64 KB with "token too long".
	// The reader must succeed, truncate the line past config.MaxLineWidth, append
	// config.TruncationMarker, and return the hash computed over the full 100 KB.
	const hundredKB = 100 * 1024
	longLine := strings.Repeat("A", hundredKB)
	longPath := filepath.Join(root, "longline.txt")
	require.NoError(t, os.WriteFile(longPath, []byte(longLine), 0o644))

	t.Run("single-line 100KB file reads back without ceiling and stays bounded", func(t *testing.T) {
		resultJSON, err := tools.PerformRead(root, "longline.txt", 1, 1)
		require.NoError(t, err)

		var result domain.ReadResultJSON
		require.NoError(t, json.Unmarshal([]byte(resultJSON), &result))

		expectedContent := strings.Repeat("A", config.MaxLineWidth) + config.TruncationMarker + "\n"
		assert.Equal(t, expectedContent, result.Content)
		assert.Less(t, len(result.Content), hundredKB/10, "read payload must be bounded, not 100 KB")
		assert.Equal(t, indexer.ContentHash([]byte(longLine)), result.Hash, "hash must cover the full file")

		// Round-tripping the truncated read output into edit must be rejected so the
		// file is not silently overwritten with truncated content.
		_, err = tools.PerformEdit(db, root, "longline.txt", 1, 1, result.Content, result.Hash, config.DefaultIgnoredDirs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), config.TruncationMarker)

		// Even without the marker in newContent, overwriting a line that exceeded
		// MaxLineWidth (and therefore could not be seen in full via read) is refused.
		_, err = tools.PerformEdit(db, root, "longline.txt", 1, 1, "short", result.Hash, config.DefaultIgnoredDirs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds")

		onDisk, readErr := os.ReadFile(longPath)
		require.NoError(t, readErr)
		assert.Equal(t, longLine, string(onDisk), "rejected edits must leave the original 100 KB file untouched")
	})

	t.Run("truncated read hash still guards edits to normal lines in the same file", func(t *testing.T) {
		mixedContent := "package main\n" + longLine + "\nfunc Normal() {}\n"
		mixedPath := filepath.Join(root, "mixed.go")
		require.NoError(t, os.WriteFile(mixedPath, []byte(mixedContent), 0o644))

		resultJSON, err := tools.PerformRead(root, "mixed.go", 1, 3)
		require.NoError(t, err)

		var result domain.ReadResultJSON
		require.NoError(t, json.Unmarshal([]byte(resultJSON), &result))
		assert.Contains(t, result.Content, config.TruncationMarker)

		// Editing a range that spans the long line (line 2) is refused.
		_, err = tools.PerformEdit(db, root, "mixed.go", 1, 2, "package main\nshort", result.Hash, config.DefaultIgnoredDirs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "line 2 exceeds")

		// Editing a normal line (line 3) using the hash from the truncated read succeeds
		// and preserves the untruncated 100 KB line 2 intact.
		editRes, err := tools.PerformEdit(db, root, "mixed.go", 3, 3, "func Updated() {}", result.Hash, config.DefaultIgnoredDirs)
		require.NoError(t, err)
		assert.Contains(t, editRes, "success")

		onDisk, readErr := os.ReadFile(mixedPath)
		require.NoError(t, readErr)
		assert.Equal(t, "package main\n"+longLine+"\nfunc Updated() {}\n", string(onDisk))
	})

	t.Run("minified bundle with 50KB line returns truncated output", func(t *testing.T) {
		const fiftyKB = 50 * 1024
		minified := "function test(){" + strings.Repeat("x", fiftyKB) + "}"
		minPath := filepath.Join(root, "bundle.min.js")
		require.NoError(t, os.WriteFile(minPath, []byte(minified), 0o644))

		resultJSON, err := tools.PerformRead(root, "bundle.min.js", 1, 1)
		require.NoError(t, err)

		var result domain.ReadResultJSON
		require.NoError(t, json.Unmarshal([]byte(resultJSON), &result))

		assert.Contains(t, result.Content, config.TruncationMarker)
		assert.Less(t, len(result.Content), fiftyKB/5, "payload must stay bounded")
		assert.Equal(t, indexer.ContentHash([]byte(minified)), result.Hash)
	})
}
