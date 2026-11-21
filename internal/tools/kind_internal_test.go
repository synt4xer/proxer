package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/domain"
)

func TestClassifyPath(t *testing.T) {
	tests := []struct {
		path     string
		expected fileKind
	}{
		{path: "internal/tools/search.go", expected: kindCode},
		{path: "internal/tools/tools_test.go", expected: kindTest},
		{path: "web/auth.test.ts", expected: kindTest},
		{path: "web/auth.spec.tsx", expected: kindTest},
		{path: "web/__tests__/helpers.ts", expected: kindTest},
		{path: "tests/test_auth.py", expected: kindTest},
		{path: "tests/auth_test.py", expected: kindTest},
		{path: "spec/auth_spec.rb", expected: kindTest},
		{path: "PLAN.md", expected: kindDocs},
		{path: "docs/notes.mdx", expected: kindDocs},
		{path: "NOTES.TXT", expected: kindDocs},
		{path: "manual.rst", expected: kindDocs},
		// Docs and assets win over test substrings in the filename.
		{path: "docs/auth.test.md", expected: kindDocs},
		{path: "dist/auth.test.js.map", expected: kindAssets},
		{path: "static/htmx.min.js", expected: kindAssets},
		{path: "web/app.Min.CSS", expected: kindAssets},
		{path: "dist/app.js.map", expected: kindAssets},
		{path: "package-lock.json", expected: kindAssets},
		{path: "go.sum", expected: kindAssets},
		{path: "web/pnpm-lock.yaml", expected: kindAssets},
		// A lockfile name only counts as a file name, not as a suffix.
		{path: "notes/go.sum.md", expected: kindDocs},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.expected, classifyPath(tt.path))
		})
	}
}

func TestParseKind(t *testing.T) {
	for value, expected := range map[string]fileKind{
		"":       kindAll,
		"all":    kindAll,
		"ALL":    kindAll,
		"code":   kindCode,
		" docs ": kindDocs,
	} {
		kind, err := parseKind(value)
		require.NoError(t, err, "value %q", value)
		assert.Equal(t, expected, kind, "value %q", value)
	}

	_, err := parseKind("tests")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid kind")
}

func TestOrderByKind(t *testing.T) {
	// Ranked as they come out of the query: a doc first, then an asset, then
	// a test file, then two production code files.
	candidates := []domain.SearchResultDB{
		{Filepath: "PLAN.md"},
		{Filepath: "static/htmx.min.js"},
		{Filepath: "internal/tools/tools_test.go"},
		{Filepath: "internal/tools/search.go"},
		{Filepath: "internal/tools/kind.go"},
	}

	ordered := orderByKind(candidates, kindAll)
	require.Len(t, ordered, 5)
	assert.Equal(t, []string{
		"internal/tools/search.go",
		"internal/tools/kind.go",
		"internal/tools/tools_test.go",
		"PLAN.md",
		"static/htmx.min.js",
	}, paths(ordered), "production code must lead, then tests, then docs, then assets")

	docs := orderByKind(candidates, kindDocs)
	require.Len(t, docs, 1)
	assert.Equal(t, "PLAN.md", docs[0].Filepath)

	code := orderByKind(candidates, kindCode)
	require.Len(t, code, 3)
	assert.Equal(t, []string{
		"internal/tools/search.go",
		"internal/tools/kind.go",
		"internal/tools/tools_test.go",
	}, paths(code), "--kind code keeps both production code and tests, production first")
}

func paths(results []domain.SearchResultDB) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.Filepath)
	}
	return out
}
