// Package mcpserver serves proxer's index, search, read, edit, and status
// operations over the Model Context Protocol.
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

const instructions = `proxer indexes a local project and exposes five tools: index, search, read, edit, and status.

Paths passed to read and edit are relative to the project root; absolute paths inside the root are also accepted. A path that resolves outside the root is rejected, never clamped or rewritten. search refreshes the index before it queries (with a short cooldown), so files changed by other tools are found without calling index first; index is still there to pay that cost up front. Search to locate code, read before editing (or use search's hash directly with anchor-mode edit), edit reindexes the file it changes, and status inspects index health and maintenance state.`

type Options struct {
	IgnoredDirs    []string
	IndexSensitive bool
	DBPath         string
	ReadOnly       bool
	InstallMode    string
	SkillDir       string
}

type Server struct {
	db              *sqlx.DB
	root            string
	dbPath          string
	ignoredDirs     []string
	indexSensitive  bool
	readOnly        bool
	installMode     string
	skillDir        string
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
	return NewWithOptions(db, root, Options{IgnoredDirs: ignoredDirs})
}

func NewWithOptions(db *sqlx.DB, root string, opts Options) *Server {
	// A harness spawns the server once, so the environment is read here rather
	// than per tool call. PROXER_NO_REFRESH=1 turns every search into a pure
	// read, which is the escape hatch for a caller that must not write.
	s := &Server{
		db:              db,
		root:            root,
		dbPath:          opts.DBPath,
		ignoredDirs:     opts.IgnoredDirs,
		indexSensitive:  opts.IndexSensitive,
		readOnly:        opts.ReadOnly,
		installMode:     opts.InstallMode,
		skillDir:        opts.SkillDir,
		noRefresh:       config.RefreshDisabled(),
		refreshCooldown: defaultRefreshCooldown,
		now:             time.Now,
	}
	s.mcp = mcp.NewServer(
		&mcp.Implementation{Name: "proxer", Version: config.Version},
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
