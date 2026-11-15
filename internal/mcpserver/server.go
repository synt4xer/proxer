// Package mcpserver serves proxer's index, search, read, and edit operations
// over the Model Context Protocol.
package mcpserver

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is reported to MCP clients during initialization.
const Version = "0.2.0"

const instructions = `proxer indexes a local project and exposes four tools: index, search, read, and edit.

Paths passed to read and edit are relative to the project root; absolute paths inside the root are also accepted. A path that resolves outside the root is rejected, never clamped or rewritten. Call index once after checkout, then search to locate code, and read before editing. edit reindexes the file it changes.`

// Server serves one MCP client against one project root.
type Server struct {
	db   *sqlx.DB
	root string
	mcp  *mcp.Server
}

// New builds the MCP server for a project root. The caller owns db and must
// keep it open until Run returns.
func New(db *sqlx.DB, root string) *Server {
	s := &Server{db: db, root: root}
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
