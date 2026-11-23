package domain_test

import (
	json "encoding/json/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/tools"
)

func TestIndexResponseJSONContract(t *testing.T) {
	in := domain.IndexResponse{
		Status: "indexed_successfully",
		Refreshed: domain.RefreshSummary{
			Added:   3,
			Changed: 1,
			Removed: 0,
			Skipped: 2,
		},
	}

	raw, err := tools.EncodeJSON(in)
	require.NoError(t, err)
	assert.NotContains(t, raw, "\n")

	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &obj))
	assert.Equal(t, []string{"refreshed", "status"}, sortedKeys(obj))

	refreshed, ok := obj["refreshed"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []string{"added", "changed", "removed", "skipped"}, sortedKeys(refreshed))

	var out domain.IndexResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	assert.Equal(t, in, out)
}

func TestSearchResponseJSONContractAndOmitEmpty(t *testing.T) {
	t.Run("with refreshed and indexed_at", func(t *testing.T) {
		in := domain.SearchResponse{
			Root:      "/repo",
			IndexedAt: "2026-11-23T12:00:00Z",
			Refreshed: &domain.RefreshSummary{
				Added:   1,
				Changed: 0,
				Removed: 0,
				Skipped: 0,
			},
			Total:     1,
			Truncated: false,
			Results: []domain.SearchResultJSON{
				{
					Root:     "/repo",
					Filepath: "main.go",
					Line:     "1-10",
					Snippet:  "func <b>Handle</b>()",
					Match:    "content",
				},
			},
		}

		raw, err := tools.EncodeJSON(in)
		require.NoError(t, err)
		assert.NotContains(t, raw, "\n")
		assert.Contains(t, raw, "<b>Handle</b>")
		assert.NotContains(t, raw, `\u003c`)
		assert.NotContains(t, raw, `\u003e`)

		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &obj))
		assert.Equal(t, []string{"indexed_at", "refreshed", "results", "root", "total", "truncated"}, sortedKeys(obj))

		var out domain.SearchResponse
		require.NoError(t, json.Unmarshal([]byte(raw), &out))
		assert.Equal(t, in, out)
	})

	t.Run("omits empty indexed_at and nil refreshed", func(t *testing.T) {
		in := domain.SearchResponse{
			Root:      "/repo",
			Total:     0,
			Truncated: false,
			Results:   []domain.SearchResultJSON{},
		}

		raw, err := tools.EncodeJSON(in)
		require.NoError(t, err)

		var obj map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &obj))
		assert.Equal(t, []string{"results", "root", "total", "truncated"}, sortedKeys(obj))
		_, hasIndexedAt := obj["indexed_at"]
		_, hasRefreshed := obj["refreshed"]
		assert.False(t, hasIndexedAt)
		assert.False(t, hasRefreshed)

		var out domain.SearchResponse
		require.NoError(t, json.Unmarshal([]byte(raw), &out))
		assert.Equal(t, in, out)
	})
}

func TestReadResultJSONContract(t *testing.T) {
	in := domain.ReadResultJSON{
		Root:       "/repo",
		Filepath:   "main.go",
		StartLine:  1,
		EndLine:    2,
		TotalLines: 2,
		Truncated:  false,
		Content:    "if a < b && b > c {\n}",
		Hash:       "deadbeef",
	}

	raw, err := tools.EncodeJSON(in)
	require.NoError(t, err)
	assert.NotContains(t, raw, "\n")
	assert.Contains(t, raw, "if a < b && b > c")
	assert.NotContains(t, raw, `\u003c`)

	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &obj))
	assert.Equal(t, []string{"content", "end_line", "filepath", "hash", "root", "start_line", "total_lines", "truncated"}, sortedKeys(obj))
	_, hasNextStart := obj["next_start"]
	assert.False(t, hasNextStart, "next_start must be omitted at EOF")

	var out domain.ReadResultJSON
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	assert.Equal(t, in, out)
}

func TestEditResultJSONContract(t *testing.T) {
	in := domain.EditResultJSON{
		Status:  "success",
		Message: "Successfully replaced lines 1 to 2 in main.go and re-indexed",
	}

	raw, err := tools.EncodeJSON(in)
	require.NoError(t, err)
	assert.NotContains(t, raw, "\n")

	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &obj))
	assert.Equal(t, []string{"message", "status"}, sortedKeys(obj))

	var out domain.EditResultJSON
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	assert.Equal(t, in, out)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
