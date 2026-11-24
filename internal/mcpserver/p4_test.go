package mcpserver

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/indexer"
)

func TestMCPCooldownDoesNotSetStaleAndStatusToolWorks(t *testing.T) {
	cs, _ := newTestSession(t)

	// First search refreshes; second search within 2s cooldown skips refresh without setting stale.
	_ = callTool(t, cs, "index", map[string]any{})
	var first domain.SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resultText(t, callTool(t, cs, "search", map[string]any{"keyword": "ProcessData"}))), &first))
	assert.NotNil(t, first.Refreshed)
	assert.False(t, first.Stale)
	assert.Empty(t, first.StaleReason)

	var second domain.SearchResponse
	require.NoError(t, json.Unmarshal([]byte(resultText(t, callTool(t, cs, "search", map[string]any{"keyword": "ProcessData"}))), &second))
	assert.Nil(t, second.Refreshed)
	assert.False(t, second.Stale, "cooldown skip must not set stale=true")
	assert.Empty(t, second.StaleReason)

	var status domain.StatusResponse
	require.NoError(t, json.Unmarshal([]byte(resultText(t, callTool(t, cs, "status", map[string]any{}))), &status))
	assert.Greater(t, status.FilesIndexed, 0)
	assert.Greater(t, status.ChunksIndexed, 0)
}

func TestMCPIndexWithDBPathAndReadOnlyStatusVacuumError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nfunc Target() {}\n"), 0o644))

	dbPath := filepath.Join(root, config.DBFile)
	db, err := database.Open(dbPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	srv := NewWithOptions(db, root, Options{
		DBPath:      dbPath,
		IgnoredDirs: config.DefaultIgnoredDirs,
	})

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	serverSession, err := srv.mcp.Connect(ctx, st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })

	var idxResp domain.IndexResponse
	require.NoError(t, json.Unmarshal([]byte(resultText(t, callTool(t, cs, "index", map[string]any{}))), &idxResp))
	assert.Equal(t, "indexed_successfully", idxResp.Status)
	assert.Greater(t, idxResp.DBSizeBytes, int64(0))

	// Read-only MCP server rejects status --vacuum.
	roSrv := NewWithOptions(db, root, Options{
		DBPath:   dbPath,
		ReadOnly: true,
	})
	ct2, st2 := mcp.NewInMemoryTransports()
	ss2, err := roSrv.mcp.Connect(ctx, st2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { ss2.Close() })
	cs2, err := client.Connect(ctx, ct2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { cs2.Close() })

	res, err := cs2.CallTool(ctx, &mcp.CallToolParams{
		Name:      "status",
		Arguments: map[string]any{"vacuum": true},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}
