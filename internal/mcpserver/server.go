// Package mcpserver serves proxer's index, search, read, and edit operations
// over the Model Context Protocol.
package mcpserver

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/synt4xer/proxer/internal/config"
)

const defaultRefreshCooldown = 2 * time.Second

// Version is reported to MCP clients during initialization.
const Version = config.Version

const instructions = `proxer indexes a local project and exposes four tools: index, search, read, and edit.

Paths passed to read and edit are relative to the project root; absolute paths inside the root are also accepted. A path that resolves outside the root is rejected, never clamped or rewritten. search refreshes the index before it queries, so files changed by other tools are found without calling index first; index is still there to pay that cost up front. Search to locate code, read before editing, and edit reindexes the file it changes.`

// Server serves one MCP client against one project root.
type Server struct {
	db              *sqlx.DB
	root            string
	ignoredDirs     []string
	noRefresh       bool
	refreshCooldown time.Duration
	now             func() time.Time
	mu              sync.Mutex
	lastRefreshAt   time.Time
	mcp             *mcp.Server
}

// New builds the MCP server for a project root. The caller owns db and must
// keep it open until Run returns. ignoredDirs is the project's index skip list.
func New(db *sqlx.DB, root string, ignoredDirs []string) *Server {
	// A harness spawns the server once, so the environment is read here rather
	// than per tool call. PROXER_NO_REFRESH=1 turns every search into a pure
	// read, which is the escape hatch for a caller that must not write.
	s := &Server{
		db:              db,
		root:            root,
		ignoredDirs:     ignoredDirs,
		noRefresh:       config.RefreshDisabled(),
		refreshCooldown: defaultRefreshCooldown,
		now:             time.Now,
	}
	s.mcp = mcp.NewServer(
		&mcp.Implementation{Name: "proxer", Version: Version},
		&mcp.ServerOptions{Instructions: instructions},
	)
	s.registerTools()
	return s
}

// Run serves the session on t until ctx is cancelled or the client disconnects.
// The shutdown caused by ctx cancellation is reported as a nil error.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	if err := s.mcp.Run(ctx, t); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
