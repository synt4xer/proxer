package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
)

func openTestDB(t *testing.T, root string) func() {
	t.Helper()
	return func() {}
}

func TestPerformReadNumberedAndByteCap(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 1500; i++ {
		fmt.Fprintf(&sb, "line_%04d := %q\n", i, strings.Repeat("x", 60))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.go"), []byte(sb.String()), 0o644))

	unnumbered, err := PerformRead(root, "big.go", 1, 1500)
	require.NoError(t, err)
	numbered, err := PerformReadWithOptions(root, "big.go", 1, 1500, ReadOptions{Numbered: true})
	require.NoError(t, err)

	assert.Equal(t, unnumbered.Hash, numbered.Hash)
	assert.True(t, numbered.Truncated)
	assert.LessOrEqual(t, len(numbered.Content), config.MaxReadBytes)
	assert.Equal(t, numbered.EndLine+1, numbered.NextStart)
	// Numbered prefixes consume extra bytes, so fewer lines fit in 64 KB.
	assert.Less(t, numbered.EndLine, unnumbered.EndLine)

	lines := strings.Split(strings.TrimSuffix(numbered.Content, "\n"), "\n")
	require.Len(t, lines, numbered.EndLine)
	assert.True(t, strings.HasPrefix(lines[0], "1\tline_0001 := "))
	assert.True(t, strings.HasPrefix(lines[len(lines)-1], fmt.Sprintf("%d\tline_%04d := ", numbered.EndLine, numbered.EndLine)))
}

func TestSearchHitLineSnippetHashAndDirectAnchorEdit(t *testing.T) {
	root := t.TempDir()
	// Build a file > 200 chars with UTF-8 runes and a camelCase identifier.
	body := strings.Join([]string{
		"package main",
		"",
		"// Café — leading comment padding to push the match past 180 characters so snippet centering is exercised cleanly.",
		"// More padding text with unicode αβγδ and extra context before the actual function declaration appears below.",
		"func AuthMiddleware(session string) bool {",
		"\treturn session != \"\"",
		"}",
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "middleware.go"), []byte(body), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "notesonly.txt"), []byte("nothing matching inside"), 0o644))

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	defer db.Close()

	// Content / camelCase hit.
	res, err := PerformSearch(db, root, "auth middleware", SearchOptions{IgnoredDirs: config.DefaultIgnoredDirs})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)

	hit := res.Results[0]
	assert.Equal(t, "middleware.go", hit.Filepath)
	assert.Equal(t, "1-8", hit.Chunk)
	assert.Equal(t, 5, hit.Line)
	assert.NotEmpty(t, hit.Hash)
	assert.True(t, utf8.ValidString(hit.Snippet))
	assert.Contains(t, hit.Snippet, "<b>Auth</b>")
	assert.Contains(t, hit.Snippet, "<b>Middleware</b>")
	assert.LessOrEqual(t, utf8.RuneCountInString(hit.Snippet), 220)

	readRes, err := PerformRead(root, "middleware.go", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, readRes.Hash, hit.Hash)

	// Edit directly from search hash using anchor mode without an intermediate read.
	editRes, err := PerformEditWithOptions(context.Background(), db, root, hit.Filepath, 0, 0,
		"func AuthMiddleware(session string) bool {\n\treturn len(session) > 0\n}",
		hit.Hash,
		EditOptions{
			IgnoredDirs: config.DefaultIgnoredDirs,
			OldContent:  "func AuthMiddleware(session string) bool {\n\treturn session != \"\"\n}",
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "success", editRes.Status)
	assert.NotEmpty(t, editRes.Hash)
	assert.NotEqual(t, hit.Hash, editRes.Hash)

	// Path-only hit: line must be 0 (omitted) and snippet must have no <b> tags.
	pathRes, err := PerformSearch(db, root, "notesonly", SearchOptions{IgnoredDirs: config.DefaultIgnoredDirs})
	require.NoError(t, err)
	require.Len(t, pathRes.Results, 1)
	assert.Equal(t, "path", pathRes.Results[0].Match)
	assert.Equal(t, 0, pathRes.Results[0].Line)
	assert.NotContains(t, pathRes.Results[0].Snippet, "<b>")
}

func TestPerformEditAnchorModeAndChainedHash(t *testing.T) {
	root := t.TempDir()
	src := "package main\r\n\r\nfunc a() int {\r\n\treturn 1\r\n}\r\n\r\nfunc b() int {\r\n\treturn 1\r\n}\r\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "dup.go"), []byte(src), 0o644))

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	defer db.Close()

	// Mutual exclusivity check.
	_, err = PerformEditWithOptions(context.Background(), db, root, "dup.go", 1, 2, "x", "", EditOptions{
		OldContent: "return 1",
		HasRange:   true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot combine --old anchor mode with --start/--end line range")

	// 0 matches.
	_, err = PerformEditWithOptions(context.Background(), db, root, "dup.go", 0, 0, "x", "", EditOptions{
		OldContent: "does not exist",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "old content not found in dup.go")

	// Duplicate matches with occurrence == 0.
	_, err = PerformEditWithOptions(context.Background(), db, root, "dup.go", 0, 0, "return 2", "", EditOptions{
		OldContent: "return 1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "old content matched 2 locations in dup.go at lines [4, 8]")

	// Disambiguate with occurrence == 2, then chain another edit using returned hash.
	res1, err := PerformEditWithOptions(context.Background(), db, root, "dup.go", 0, 0, "return 2", "", EditOptions{
		IgnoredDirs: config.DefaultIgnoredDirs,
		OldContent:  "return 1",
		Occurrence:  2,
	})
	require.NoError(t, err)
	require.NotEmpty(t, res1.Hash)

	// Chained edit using res1.Hash (now "return 1" is unique at line 4).
	res2, err := PerformEditWithOptions(context.Background(), db, root, "dup.go", 0, 0, "return 3", res1.Hash, EditOptions{
		IgnoredDirs: config.DefaultIgnoredDirs,
		OldContent:  "return 1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, res2.Hash)

	updated, err := os.ReadFile(filepath.Join(root, "dup.go"))
	require.NoError(t, err)
	// CRLF preserved.
	assert.Equal(t, "package main\r\n\r\nfunc a() int {\r\n\treturn 3\r\n}\r\n\r\nfunc b() int {\r\n\treturn 2\r\n}\r\n", string(updated))
}

func TestSearchDefinitionOutranksImportBlock(t *testing.T) {
	root := t.TempDir()

	// Import-heavy file repeating AuthMiddleware and session.
	routes := strings.Join([]string{
		"import { AuthMiddleware } from './auth'",
		"import { session } from './session'",
		"import { AuthMiddleware as requireAuth } from './auth'",
		"import { session as userSession } from './session'",
		"export const routes = [requireAuth, userSession]",
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "routes.ts"), []byte(routes), 0o644))

	// Definition file with a single declaration.
	auth := strings.Join([]string{
		"export async function AuthMiddleware(session: string): Promise<boolean> {",
		"  return session.length > 0",
		"}",
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "auth.ts"), []byte(auth), 0o644))

	db, err := database.Open(filepath.Join(root, config.DBFile), database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	defer db.Close()

	res, err := PerformSearch(db, root, "AuthMiddleware session", SearchOptions{IgnoredDirs: config.DefaultIgnoredDirs})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(res.Results), 2)
	assert.Equal(t, "auth.ts", res.Results[0].Filepath)
	assert.Equal(t, "routes.ts", res.Results[1].Filepath)
}
