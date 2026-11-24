package tools_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
	"github.com/synt4xer/proxer/internal/tools"
)

func TestSearchTotalExactnessAndWindowFastPath(t *testing.T) {
	root := t.TempDir()

	// Create 60 Go files (> minCandidateWindow=50) + 55 Markdown files so we can
	// exercise both < window and == window exact count paths, AND->OR fallback,
	// --kind filters, and doc-exclusion fallback.
	for i := range 60 {
		name := fmt.Sprintf("pkg/file_%03d.go", i)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
		content := fmt.Sprintf("package pkg\n\nfunc Handler%03d(user_id string) string {\n\tif user_id == \"\" {\n\t\treturn \"\"\n\t}\n\treturn user_id\n}\n", i)
		if i < 3 {
			content += "\n// rarealpha cooccurs with rarebeta\n"
		} else if i < 10 {
			content += "\n// rarealpha only\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(content), 0o644))
	}
	for i := range 55 {
		name := fmt.Sprintf("docs/doc_%03d.md", i)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
		// Dense repetition so docs fill the top 50 window for doconly_target.
		var body string
		for range 10 {
			body += "doconly_target doconly_target doconly_target\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg/doconly_code.go"), []byte("package pkg\n\nfunc doconly_target() {}\n"), 0o644))

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	t.Run("< window fast path returns exact total", func(t *testing.T) {
		res, err := tools.PerformSearch(db, root, "Handler001", tools.SearchOptions{NoRefresh: true, Limit: 5})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Total)
		assert.False(t, res.Truncated)
		assert.Len(t, res.Results, 1)
	})

	t.Run("== window exact count path matches SELECT count(*)", func(t *testing.T) {
		// Limit=1 -> window=50; 60 Go files match "func", so rawCount == 50 == window.
		res, err := tools.PerformSearch(db, root, "func", tools.SearchOptions{NoRefresh: true, Limit: 1})
		require.NoError(t, err)
		var sqlTotal int
		require.NoError(t, db.Get(&sqlTotal, `SELECT count(*) FROM chunk_search WHERE chunk_search MATCH '"func"*'`))
		assert.Equal(t, sqlTotal, res.Total)
		assert.True(t, res.Truncated)
		assert.Len(t, res.Results, 1)
	})

	t.Run("AND query satisfies limit", func(t *testing.T) {
		res, err := tools.PerformSearch(db, root, "rarealpha rarebeta", tools.SearchOptions{NoRefresh: true, Limit: 2})
		require.NoError(t, err)
		assert.Equal(t, 3, res.Total)
		assert.True(t, res.Truncated)
		assert.Len(t, res.Results, 2)
	})

	t.Run("AND->OR fallback counts winning OR query", func(t *testing.T) {
		// Only 3 files match AND, Limit=5 -> falls back to OR (10 files total).
		res, err := tools.PerformSearch(db, root, "rarealpha rarebeta", tools.SearchOptions{NoRefresh: true, Limit: 5})
		require.NoError(t, err)
		assert.Equal(t, 10, res.Total)
		assert.True(t, res.Truncated)
		assert.Len(t, res.Results, 5)
	})

	t.Run("kind=docs and kind=code count their respective filtered queries", func(t *testing.T) {
		docsRes, err := tools.PerformSearch(db, root, "doconly_target", tools.SearchOptions{NoRefresh: true, Limit: 1, Kind: "docs"})
		require.NoError(t, err)
		assert.Equal(t, 55, docsRes.Total)
		assert.True(t, docsRes.Truncated)

		codeRes, err := tools.PerformSearch(db, root, "doconly_target", tools.SearchOptions{NoRefresh: true, Limit: 1, Kind: "code"})
		require.NoError(t, err)
		assert.Equal(t, 1, codeRes.Total)
		assert.False(t, codeRes.Truncated)
	})

	t.Run("doc-exclusion fallback preserves kindAll total and surfaces code", func(t *testing.T) {
		// Limit=1 -> window=50; 55 docs outrank 1 code file, triggering doc-exclusion fallback.
		res, err := tools.PerformSearch(db, root, "doconly_target", tools.SearchOptions{NoRefresh: true, Limit: 1})
		require.NoError(t, err)
		assert.Equal(t, 56, res.Total)
		assert.True(t, res.Truncated)
		require.Len(t, res.Results, 1)
		assert.Equal(t, "pkg/doconly_code.go", res.Results[0].Filepath)
	})
}

func TestSearchSubtokenAndCommonTermInvariants(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "foo-bar"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "foo-bar", "auth.go"), []byte(`package foobar

import "context"

type AuthMiddleware struct{}

func (a *AuthMiddleware) Handle(ctx context.Context, user_id string) string {
	if user_id == "" {
		return "empty"
	}
	return ctx.Value("key").(string)
}
`), 0o644))

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	for _, kw := range []string{
		"user", "id", "user_id",
		"foo", "bar", "foo-bar",
		"middleware", "auth",
		"func", "if", "context Value",
		"Handle",
	} {
		res, err := tools.PerformSearch(db, root, kw, tools.SearchOptions{NoRefresh: true, Limit: 5})
		require.NoError(t, err, "query %q failed", kw)
		require.NotEmpty(t, res.Results, "query %q should match", kw)
		assert.Equal(t, "foo-bar/auth.go", res.Results[0].Filepath, "query %q", kw)
	}
}

func TestCandidateWindowParityAgainstLegacyCTE(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
	for i := range 60 {
		body := fmt.Sprintf("package src\n\nfunc F%02d() {\n", i)
		for j := 0; j < (i%5)+1; j++ {
			body += "\tif true { _ = 1 }\n"
		}
		body += "}\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("src/f_%02d.go", i)), []byte(body), 0o644))
	}

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	mustIndex(t, db, root, config.DefaultIgnoredDirs)

	res, err := tools.PerformSearch(db, root, "if", tools.SearchOptions{NoRefresh: true, Limit: 5})
	require.NoError(t, err)
	assert.Equal(t, 60, res.Total)
	require.Len(t, res.Results, 5)

	type legacyRow struct {
		Filepath  string  `db:"filepath"`
		StartLine int     `db:"start_line"`
		EndLine   int     `db:"end_line"`
		Snippet   string  `db:"snippet"`
		Score     float64 `db:"score"`
		Total     int     `db:"total"`
	}
	var legacy []legacyRow
	require.NoError(t, db.Select(&legacy, `
		WITH matched AS (
			SELECT filepath, start_line, end_line, content,
			       snippet(chunk_search, 4, '<b>', '</b>', '...', 10) AS snippet,
			       bm25(chunk_search, 0, 5, 0, 0, 1, 0.5) AS score
			FROM chunk_search
			WHERE chunk_search MATCH '"if"*'
		)
		SELECT filepath, start_line, end_line, snippet, score, count(*) OVER() AS total
		FROM matched
		ORDER BY score
		LIMIT 5
	`))
	require.Len(t, legacy, 5)
	assert.Equal(t, legacy[0].Total, res.Total)
	for i := range 5 {
		assert.Equal(t, legacy[i].Filepath, res.Results[i].Filepath)
		assert.Equal(t, fmt.Sprintf("%d-%d", legacy[i].StartLine, legacy[i].EndLine), res.Results[i].Chunk)
		assert.Equal(t, 4, res.Results[i].Line)
		assert.Contains(t, res.Results[i].Snippet, "<b>if</b>")
	}
}
