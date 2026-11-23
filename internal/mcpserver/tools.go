package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/indexer"
	"github.com/synt4xer/proxer/internal/tools"
)

type indexInput struct{}

type searchInput struct {
	Keyword    string `json:"keyword" jsonschema:"terms to search for; each term matches as a token prefix (and camelCase sub-tokens on code/config), using AND-first with OR fallback"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum number of results to return, clamped to 1..50 (default 5)"`
	Kind       string `json:"kind,omitempty" jsonschema:"which files to search: code, docs, or all (default all, which orders production code before test files before docs before generated assets)"`
	MaxPerFile int    `json:"max_per_file,omitempty" jsonschema:"maximum non-overlapping chunks returned per file, clamped to 1..5 (default 1)"`
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

func (s *Server) registerTools() {
	s.addIndexTool()
	s.addSearchTool()
	s.addReadTool()
	s.addEditTool()
}

func (s *Server) addIndexTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "index",
		Description: "Scan the project root and bring the full-text index up to date. " +
			"It is incremental: only new, changed, and deleted files are touched, so running it is cheap. " +
			"search does this automatically, so the tool exists for callers that want to pay the cost up front. " +
			"No parameters are needed: the root is fixed when the server starts.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ indexInput) (*mcp.CallToolResult, domain.IndexResponse, error) {
		refreshed, err := indexer.IndexProjectWithOptions(s.db, s.root, indexer.IndexOptions{
			IgnoredDirs:    s.ignoredDirs,
			IndexSensitive: s.indexSensitive,
		})
		if err != nil {
			return nil, domain.IndexResponse{}, err
		}
		var scrub database.ScrubResult
		if s.dbPath != "" {
			scrub, err = database.ScrubPurgedData(s.db, s.dbPath)
			if err != nil {
				return nil, domain.IndexResponse{}, err
			}
		}
		return nil, domain.IndexResponse{
			Status: "indexed_successfully",
			Refreshed: domain.RefreshSummary{
				Added:   refreshed.Added,
				Changed: refreshed.Changed,
				Removed: refreshed.Removed,
				Skipped: refreshed.Skipped,
			},
			IndexSensitive: s.indexSensitive,
			PurgePending:   scrub.PurgePending,
			Warning:        scrub.Warning,
		}, nil
	})
}

func (s *Server) addSearchTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "search",
		Description: "Search the full-text index. Every term matches as a token prefix and multiple terms run AND-first with OR fallback. " +
			"Terms are matched against file paths as well as content, so a token that only names a file still finds it. " +
			"On code and config files, camelCase and PascalCase identifiers are also sub-tokenized (3+ chars), so both 'auth' and 'middleware' reach 'AuthMiddleware'. " +
			"Before querying it brings the index up to date, so files changed by other tools are found without calling index first. " +
			"Returns the absolute project root, the indexed_at timestamp of the last index change, the refreshed counts for this call, " +
			"the total number of matching chunks, whether the result list was truncated, and up to `limit` results (default 5), " +
			"ordered production code first, then test files, then documentation, then generated assets unless `kind` narrows it. " +
			"Each result carries a filepath relative to that root, a line range, a snippet where matches are wrapped in <b> tags, " +
			"and whether its terms matched the path, the content, or both. " +
			"Empty or punctuation-only input returns no matches.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, domain.SearchResponse, error) {
		noRefresh := s.shouldSkipRefresh()
		response, err := tools.PerformSearch(s.db, s.root, in.Keyword, tools.SearchOptions{
			NoRefresh:      noRefresh,
			IgnoredDirs:    s.ignoredDirs,
			IndexSensitive: s.indexSensitive,
			Limit:          in.Limit,
			Kind:           in.Kind,
			MaxPerFile:     in.MaxPerFile,
		})
		if err != nil {
			return nil, domain.SearchResponse{}, err
		}
		if !noRefresh {
			s.recordRefresh()
		}
		if response.Results == nil {
			response.Results = []domain.SearchResultJSON{}
		}
		return nil, response, nil
	})
}

func (s *Server) shouldSkipRefresh() bool {
	if s.noRefresh {
		return true
	}
	if s.refreshCooldown <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.lastRefreshAt.IsZero() && s.now().Sub(s.lastRefreshAt) < s.refreshCooldown
}

func (s *Server) recordRefresh() {
	s.mu.Lock()
	s.lastRefreshAt = s.now()
	s.mu.Unlock()
}

func (s *Server) invalidateRefresh() {
	s.mu.Lock()
	s.lastRefreshAt = time.Time{}
	s.mu.Unlock()
}

func (s *Server) addReadTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "read",
		Description: "Read an inclusive line range from a file. The filepath is relative to the project root; " +
			"absolute paths inside the root are accepted and paths outside it are rejected. " +
			"Returns the absolute project root, the filepath as given, the requested lines (each terminated by a newline; lines exceeding 4096 characters are truncated with \" [truncated]\"), " +
			"and a hash of the file. Pass that hash to edit so a change between the read and the edit is refused.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, domain.ReadResultJSON, error) {
		start, end := in.Start, in.End
		if start <= 0 {
			start = 1
		}
		if end <= 0 {
			end = 60
		}

		result, err := tools.PerformRead(s.root, in.Filepath, start, end)
		if err != nil {
			return nil, domain.ReadResultJSON{}, err
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
			"Pass the hash from a prior read of the same file: if the file changed since that read, the edit is rejected and nothing is written. " +
			"Replacements containing the \" [truncated]\" marker or targeting lines exceeding 4096 characters are also rejected.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editInput) (*mcp.CallToolResult, domain.EditResultJSON, error) {
		result, err := tools.PerformEditWithOptions(ctx, s.db, s.root, in.Filepath, in.Start, in.End, in.Content, in.Hash, tools.EditOptions{
			IgnoredDirs:    s.ignoredDirs,
			IndexSensitive: s.indexSensitive,
		})
		if err != nil {
			return nil, domain.EditResultJSON{}, err
		}
		s.invalidateRefresh()
		return nil, result, nil
	})
}
