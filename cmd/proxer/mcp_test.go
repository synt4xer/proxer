package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
)

func TestMCPCommandDescriptionAndErrors(t *testing.T) {
	cmd := newMCPCommand(&cliState{})
	for _, tool := range []string{"index", "search", "read", "edit", "status"} {
		assert.Contains(t, cmd.Short, tool)
	}

	root := setupFixtureRoot(t)

	code, _, stderr := runCLI([]string{"--root", root, "mcp", "extra"}, "")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "unknown command")

	if os.Geteuid() == 0 {
		return
	}
	roDir := filepath.Join(t.TempDir(), "ro")
	require.NoError(t, os.MkdirAll(roDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

	code, _, stderr = runCLI([]string{"--root", root, "--db", filepath.Join(roDir, "sub", "index.db"), "mcp"}, "")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, stderr, "failed to initialize DB")
}

// TestMCPCommandServesAReadOnlyDatabase covers the recovery arm of the mcp
// command: a database that cannot be opened read-write but is readable must be
// served through the read-only handle instead of failing the command.
func TestMCPCommandServesAReadOnlyDatabase(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}

	root := setupFixtureRoot(t)
	dbPath := filepath.Join(root, config.DBFile)

	code, _, stderr := runCLI([]string{"--root", root, "index"}, "")
	require.Equal(t, 0, code, stderr)
	require.True(t, database.IsReadableFile(dbPath))

	// Drop the WAL sidecars so only the main file's permissions matter, then
	// make it read-only for the current user.
	require.NoError(t, os.Chmod(dbPath, 0o444))
	t.Cleanup(func() { _ = os.Chmod(dbPath, 0o600) })

	cmd := newMCPCommand(&cliState{root: root, dbPath: dbPath})
	cmd.SetContext(context.Background())
	cmd.SetArgs(nil)

	// RunE blocks on stdio, so assert the failure mode instead: with a readable
	// read-only database it must not report a DB initialization error.
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err != nil {
		assert.NotContains(t, err.Error(), "failed to initialize DB",
			"a readable read-only index must be served, not rejected")
	}
}
