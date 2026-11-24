package mcpserver

import (
	"context"
	"os"
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
	Numbered bool   `json:"numbered,omitempty" jsonschema:"prefix each returned line with its 1-based line number and a tab (default false)"`
}

type editInput struct {
	Filepath   string `json:"filepath" jsonschema:"path to the file, relative to the project root; absolute paths inside the root are accepted and paths outside it are rejected"`
	Start      int    `json:"start,omitempty" jsonschema:"first line to replace, inclusive (line-range mode)"`
	End        int    `json:"end,omitempty" jsonschema:"last line to replace, inclusive (line-range mode)"`
	OldContent string `json:"old_content,omitempty" jsonschema:"exact text block to replace (anchor mode; mutually exclusive with start/end)"`
	Occurrence int    `json:"occurrence,omitempty" jsonschema:"1-based occurrence index when old_content matches multiple times (default 0 requires a unique match)"`
	Content    string `json:"content" jsonschema:"replacement text for the line range or anchor block"`
	Hash       string `json:"hash,omitempty" jsonschema:"hash returned by an earlier search or read of this file; when set, the edit is rejected if the file changed since that read"`
}

type statusInput struct {
	Vacuum bool `json:"vacuum,omitempty" jsonschema:"run FTS5 optimize, VACUUM, and WAL checkpoint on the database"`
}

func (s *Server) registerTools() {
	s.addIndexTool()
	s.addSearchTool()
	s.addReadTool()
	s.addEditTool()
	s.addStatusTool()
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
		hadChunkChanges := refreshed.Added > 0 || refreshed.Changed > 0 || refreshed.Removed > 0
		var maint database.MaintainResult
		var dbSize int64
		if s.dbPath != "" {
			maint, err = database.Maintain(s.db, s.dbPath, database.MaintainOptions{
				HadDiff: hadChunkChanges,
			})
			if err != nil {
				return nil, domain.IndexResponse{}, err
			}
			if info, statErr := os.Stat(s.dbPath); statErr == nil {
				dbSize = info.Size()
			}
		}
		return nil, domain.IndexResponse{
			Status:         "indexed_successfully",
			Refreshed:      refreshed.ToSummary(),
			DBSizeBytes:    dbSize,
			IndexSensitive: s.indexSensitive,
			PurgePending:   maint.PurgePending,
			Warning:        maint.Warning,
		}, nil
	})
}

func (s *Server) addSearchTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "search",
		Description: "Search the full-text index. Every term matches as a token prefix and multiple terms run AND-first with OR fallback. " +
			"Terms are matched against file paths as well as content, so a token that only names a file still finds it. " +
			"On code and config files, camelCase and PascalCase identifiers are also sub-tokenized (3+ chars), so both 'auth' and 'middleware' reach 'AuthMiddleware'. " +
			"Before querying it brings the index up to date (subject to a short cooldown), so files changed by other tools are found without calling index first; " +
			"on read-only or busy databases it serves the existing snapshot with stale=true and stale_reason. " +
			"Returns the absolute project root, indexed_at, refreshed counts when refreshed, total matching chunks, truncated, optional index_sensitive/purge_pending/filtered_sensitive flags, and up to `limit` results (default 5), " +
			"ordered production code first, then test files, then documentation, then generated assets unless `kind` narrows it. " +
			"Each result carries a relative filepath, chunk line span, first matching 1-based line (omitted on path-only hits), a snippet with matches wrapped in <b> tags, match kind (path, content, or both), and the current full-file SHA-256 hash. " +
			"Empty or punctuation-only input returns no matches.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, domain.SearchResponse, error) {
		noRefresh := s.shouldSkipRefresh()
		response, err := tools.PerformSearch(s.db, s.root, in.Keyword, tools.SearchOptions{
			Ctx:            ctx,
			NoRefresh:      noRefresh,
			ReadOnly:       s.readOnly,
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
			"Returns root, filepath, start_line, end_line, total_lines, truncated, next_start (when more lines remain), content, and the full-file SHA-256 hash. " +
			"Responses are capped at 64 KB on a whole-line boundary, lines exceeding 4096 characters are truncated with \" [truncated]\", and numbered=true prefixes each line with its 1-based line number and a tab. " +
			"Pass the returned hash to edit so a change between read and edit is refused.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, domain.ReadResultJSON, error) {
		start, end := in.Start, in.End
		if start <= 0 {
			start = 1
		}
		if end <= 0 {
			end = 60
		}

		result, err := tools.PerformReadWithOptions(s.root, in.Filepath, start, end, tools.ReadOptions{
			Numbered: in.Numbered,
		})
		if err != nil {
			return nil, domain.ReadResultJSON{}, err
		}
		return nil, result, nil
	})
}

func (s *Server) addEditTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "edit",
		Description: "Replace an inclusive line range (start/end) or an exact text block (old_content with optional 1-based occurrence) in a file and reindex the file. " +
			"The two modes are mutually exclusive. The filepath is relative to the project root; absolute paths inside the root are accepted and paths outside it are rejected. " +
			"The file is written atomically and the new full-file SHA-256 hash is returned for chaining edits. " +
			"Pass the hash from a prior search or read of the same file: if the file changed since then, the edit is rejected and nothing is written. " +
			"Replacements containing the \" [truncated]\" marker or targeting lines exceeding 4096 characters are rejected. " +
			"If post-write reindexing fails, the write succeeds and reindex_failed/reindex_error are reported non-fatally.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editInput) (*mcp.CallToolResult, domain.EditResultJSON, error) {
		result, err := tools.PerformEditWithOptions(ctx, s.db, s.root, in.Filepath, in.Start, in.End, in.Content, in.Hash, tools.EditOptions{
			IgnoredDirs:    s.ignoredDirs,
			IndexSensitive: s.indexSensitive,
			OldContent:     in.OldContent,
			Occurrence:     in.Occurrence,
			HasRange:       in.Start != 0 || in.End != 0,
		})
		if err != nil {
			return nil, domain.EditResultJSON{}, err
		}
		s.invalidateRefresh()
		return nil, result, nil
	})
}

func (s *Server) addStatusTool() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "status",
		Description: "Inspect index metadata, database size, file permissions, and maintenance state without walking the workspace.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in statusInput) (*mcp.CallToolResult, domain.StatusResponse, error) {
		res, err := tools.PerformStatus(s.db, s.root, tools.StatusOptions{
			DBPath:         s.dbPath,
			IndexSensitive: s.indexSensitive,
			ReadOnly:       s.readOnly,
			Vacuum:         in.Vacuum,
			InstallMode:    s.installMode,
			SkillDir:       s.skillDir,
		})
		if err != nil {
			return nil, domain.StatusResponse{}, err
		}
		return nil, res, nil
	})
}
