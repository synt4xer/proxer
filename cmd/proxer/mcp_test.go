package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
