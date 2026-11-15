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

type readInput struct {
	Filepath string `json:"filepath" jsonschema:"path to the file, relative to the project root; absolute paths inside the root are accepted and paths outside it are rejected"`
	Start    int    `json:"start,omitempty" jsonschema:"first line to read, inclusive (default 1)"`
	End      int    `json:"end,omitempty" jsonschema:"last line to read, inclusive (default 60)"`
}

type editInput struct {
	Filepath string `json:"filepath" jsonschema:"path to the file, relative to the project root; absolute paths inside the root are accepted and paths outside it are rejected"`
	Start    int    `json:"start" jsonschema:"first line to replace, inclusive"`
	End      int    `json:"end" jsonschema:"last line to replace, inclusive"`
	Content  string `json:"content" jsonschema:"replacement text for the line range; it need not have the same number of lines as the range"`
	Hash     string `json:"hash,omitempty" jsonschema:"hash returned by an earlier read of this file; when set, the edit is rejected if the file changed since that read"`
}

type editOutput struct {
	Status  string `json:"status" jsonschema:"human-readable result status"`
	Message string `json:"message" jsonschema:"details of the edit"`
}

func (s *Server) registerTools() {
	s.addIndexTool()
	s.addSearchTool()
	s.addReadTool()
	s.addEditTool()
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
			"Returns up to 5 matches, each with the absolute project root, a filepath relative to that root, a line range, " +
			"and a snippet where matches are wrapped in <b> tags. Empty or punctuation-only input returns no matches.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, []domain.SearchResultJSON, error) {
		resultJSON, err := tools.PerformSearch(s.db, s.root, in.Keyword)
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

func (s *Server) addReadTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "read",
		Description: "Read an inclusive line range from a file. The filepath is relative to the project root; " +
			"absolute paths inside the root are accepted and paths outside it are rejected. " +
			"Returns the absolute project root, the filepath as given, the requested lines (each terminated by a newline), " +
			"and a hash of the file. Pass that hash to edit so a change between the read and the edit is refused.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, domain.ReadResultJSON, error) {
		start, end := in.Start, in.End
		if start <= 0 {
			start = 1
		}
		if end <= 0 {
			end = 60
		}

		resultJSON, err := tools.PerformRead(s.root, in.Filepath, start, end)
		if err != nil {
			return nil, domain.ReadResultJSON{}, err
		}

		var result domain.ReadResultJSON
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, domain.ReadResultJSON{}, fmt.Errorf("decode read result: %w", err)
		}
		return nil, result, nil
	})
}

func (s *Server) addEditTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "edit",
		Description: "Replace an inclusive line range in a file and reindex the file. The filepath is relative to the project root; " +
			"absolute paths inside the root are accepted and paths outside it are rejected. " +
			"The replacement need not have the same number of lines as the range. The file is written atomically. " +
			"Pass the hash from a prior read of the same file: if the file changed since that read, the edit is rejected and nothing is written.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in editInput) (*mcp.CallToolResult, editOutput, error) {
		resultJSON, err := tools.PerformEdit(s.db, s.root, in.Filepath, in.Start, in.End, in.Content, in.Hash)
		if err != nil {
			return nil, editOutput{}, err
		}

		var result editOutput
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, editOutput{}, fmt.Errorf("decode edit result: %w", err)
		}
		return nil, result, nil
	})
}
