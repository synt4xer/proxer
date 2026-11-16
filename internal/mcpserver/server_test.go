package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/indexer"
)

const mockCode = `package main

func ProcessData() string {
	return "processed"
}
`

func newTestSession(t *testing.T) (*mcp.ClientSession, string) {
	t.Helper()
	return newTestSessionWithIgnores(t, config.DefaultIgnoredDirs)
}

func newTestSessionWithIgnores(t *testing.T, ignoredDirs []string) (*mcp.ClientSession, string) {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(mockCode), 0o644))

	db, err := indexer.InitDB(filepath.Join(root, config.DBFile))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := New(db, root, ignoredDirs).mcp.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "proxer-test", Version: "0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { clientSession.Close() })

	return clientSession, root
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()

	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "expected text content, got %T", res.Content[0])
	return text.Text
}

func TestServerExposesFourTools(t *testing.T) {
	cs, _ := newTestSession(t)

	list, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	assert.ElementsMatch(t, []string{"index", "search", "read", "edit"}, names)
}

func TestServerWorkflow(t *testing.T) {
	cs, root := newTestSession(t)

	indexRes := callTool(t, cs, "index", map[string]any{})
	require.False(t, indexRes.IsError)
	assert.Contains(t, resultText(t, indexRes), "indexed_successfully")

	searchRes := callTool(t, cs, "search", map[string]any{"keyword": "ProcessData"})
	require.False(t, searchRes.IsError)
	var found domain.SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resultText(t, searchRes)), &found))
	require.Len(t, found.Results, 1)
	assert.Equal(t, "main.go", found.Results[0].Filepath)
	assert.Equal(t, root, found.Results[0].Root)
	assert.NotEmpty(t, found.IndexedAt, "search must report when the index last moved")
	require.NotNil(t, found.Refreshed)
	assert.Zero(t, found.Refreshed.Added, "index ran first, so the search refresh must find nothing to do")

	readRes := callTool(t, cs, "read", map[string]any{"filepath": "main.go"})
	require.False(t, readRes.IsError)
	var read domain.ReadResultJSON
	require.NoError(t, json.Unmarshal([]byte(resultText(t, readRes)), &read))
	assert.Contains(t, read.Content, "ProcessData")
	assert.Equal(t, root, read.Root)
	assert.Len(t, read.Hash, 64, "read must return a hash for edit to check")

	editRes := callTool(t, cs, "edit", map[string]any{
		"filepath": "main.go",
		"start":    3,
		"end":      3,
		"content":  "\treturn \"edited\"",
		"hash":     read.Hash,
	})
	require.False(t, editRes.IsError)
	assert.Contains(t, resultText(t, editRes), "success")

	updated, err := os.ReadFile(filepath.Join(root, "main.go"))
	require.NoError(t, err)
	assert.Contains(t, string(updated), `return "edited"`)

	reindexed := callTool(t, cs, "search", map[string]any{"keyword": "edited"})
	require.False(t, reindexed.IsError)
	assert.Contains(t, resultText(t, reindexed), "main.go")
}

func TestServerRejectsPathsOutsideTheRoot(t *testing.T) {
	cs, _ := newTestSession(t)

	res := callTool(t, cs, "read", map[string]any{"filepath": "../secret.go"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "outside the project root")
}

func TestServerRejectsAStaleEditHash(t *testing.T) {
	cs, _ := newTestSession(t)

	res := callTool(t, cs, "edit", map[string]any{
		"filepath": "main.go",
		"start":    1,
		"end":      1,
		"content":  "package hijacked",
		"hash":     "deadbeef",
	})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "changed since it was read")
}

func TestServerReturnsNoMatchesWithoutError(t *testing.T) {
	cs, _ := newTestSession(t)

	res := callTool(t, cs, "search", map[string]any{"keyword": "no-such-identifier"})
	require.False(t, res.IsError)

	var response domain.SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &response))
	assert.Empty(t, response.Results)
	require.NotNil(t, response.Refreshed)
	assert.Equal(t, 1, response.Refreshed.Added, "a missing database must be built, never reported as an empty answer")
}

func TestServerIndexHonorsTheProjectSkipList(t *testing.T) {
	ignored := append(append([]string{}, config.DefaultIgnoredDirs...), "generated")
	cs, root := newTestSessionWithIgnores(t, ignored)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "generated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "generated", "gen.go"), []byte("func GeneratedMarker() {}\n"), 0o644))

	require.False(t, callTool(t, cs, "index", map[string]any{}).IsError)

	res := callTool(t, cs, "search", map[string]any{"keyword": "GeneratedMarker"})
	require.False(t, res.IsError)

	var response domain.SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &response))
	assert.Empty(t, response.Results, "the index must honor the skip list the server was built with")
	assert.Zero(t, response.Refreshed.Added, "a file inside an ignored directory must not be picked up by the refresh")
}
