package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/indexer"
	"github.com/synt4xer/proxer/internal/tools"
)

type indexInput struct{}

type indexOutput struct {
	Status string `json:"status" jsonschema:"human-readable result of the index run"`
}

type searchInput struct {
	Keyword string `json:"keyword" jsonschema:"terms to search for; each term matches as a token prefix and multiple terms are OR-ed"`
}

func (s *Server) registerTools() {
	s.addIndexTool()
	s.addSearchTool()
}

func (s *Server) addIndexTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "index",
		Description: "Scan the project root and rebuild the full-text index from scratch. " +
			"Run this once after checkout and again after changes made outside proxer; it replaces the existing index. " +
			"No parameters are needed: the root is fixed when the server starts.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ indexInput) (*mcp.CallToolResult, indexOutput, error) {
		if err := indexer.IndexProject(s.db, s.root); err != nil {
			return nil, indexOutput{}, err
		}
		return nil, indexOutput{Status: "indexed_successfully"}, nil
	})
}

func (s *Server) addSearchTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "search",
		Description: "Search the full-text index. Every term matches as a token prefix and multiple terms are OR-ed. " +
			"camelCase is not split, so 'auth' reaches 'AuthMiddleware' but 'middleware' does not. " +
			"Returns up to 5 matches, each with a filepath relative to the project root, a line range, and a snippet " +
			"where matches are wrapped in <b> tags. Empty or punctuation-only input returns no matches.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, []domain.SearchResultJSON, error) {
		resultJSON, err := tools.PerformSearch(s.db, in.Keyword)
		if err != nil {
			return nil, nil, err
		}

		var results []domain.SearchResultJSON
		if err := json.Unmarshal([]byte(resultJSON), &results); err != nil {
			return nil, nil, fmt.Errorf("decode search result: %w", err)
		}
		if results == nil {
			results = []domain.SearchResultJSON{}
		}
		return nil, results, nil
	})
}
