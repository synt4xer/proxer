package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
	"github.com/synt4xer/proxer/internal/mcpserver"
)

func newMCPCommand(state *cliState) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve index, search, read, and edit over the MCP stdio transport",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			manageDBAnnotation: "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath := state.dbPath
			if dbPath == "" {
				dbPath = filepath.Join(state.root, config.DBFile)
			}
			serverDB, err := database.Open(dbPath, database.OpenOptions{
				Chunking:       indexer.Chunking(),
				IndexSensitive: state.cfg.IndexSensitive,
			})
			if err != nil {
				return fmt.Errorf("failed to initialize DB: %w", err)
			}
			defer serverDB.Close()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return mcpserver.NewWithOptions(serverDB, state.root, mcpserver.Options{
				IgnoredDirs:    state.cfg.IgnoredDirs,
				IndexSensitive: state.cfg.IndexSensitive,
				DBPath:         dbPath,
			}).Run(ctx, &mcp.StdioTransport{})
		},
	}
}
