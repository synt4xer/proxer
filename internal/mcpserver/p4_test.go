package mcpserver

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/domain"
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
