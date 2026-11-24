package main

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/domain"
)

func TestCLIReadOnlyAndUnreadableDB(t *testing.T) {
	root := setupFixtureRoot(t)
	code, stdout, stderr := runCLI([]string{"--root", root, "index"}, "")
	require.Equal(t, 0, code, stderr)
	require.NotEmpty(t, stdout)

	dbPath := filepath.Join(root, ".proxer.db")
	require.NoError(t, os.Chmod(dbPath, 0o444))
	require.NoError(t, os.Chmod(root, 0o555))
	defer func() {
		_ = os.Chmod(root, 0o755)
		_ = os.Chmod(dbPath, 0o600)
	}()

	// Search serves snapshot with stale: true, stale_reason: "readonly".
	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "ComputeAnswer"}, "")
	require.Equal(t, 0, code, stderr)
	var searchResp domain.SearchResponse
	require.NoError(t, json.Unmarshal([]byte(stdout), &searchResp))
	assert.True(t, searchResp.Stale)
	assert.Equal(t, "readonly", searchResp.StaleReason)
	assert.Nil(t, searchResp.Refreshed)
	assert.NotEmpty(t, searchResp.Results)

	// Unreadable DB (0000) exits 1 with PROXER_DB hint.
	require.NoError(t, os.Chmod(root, 0o755))
	require.NoError(t, os.Chmod(dbPath, 0o000))
	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "ComputeAnswer"}, "")
	assert.Equal(t, 1, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "PROXER_DB")
}

func TestCLIStatusDoctorVacuumAndPrune(t *testing.T) {
	skillDir := t.TempDir()
	t.Setenv("PROXER_SKILL_DIR", skillDir)

	// 1. Project mode when .agents/skills/proxer exists.
	projectRoot := setupFixtureRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".agents", "skills", "proxer"), 0o755))
	code, _, stderr := runCLI([]string{"--root", projectRoot, "index"}, "")
	require.Equal(t, 0, code, stderr)

	code, stdout, stderr := runCLI([]string{"--root", projectRoot, "doctor", "--vacuum"}, "")
	require.Equal(t, 0, code, stderr)
	var statusResp domain.StatusResponse
	require.NoError(t, json.Unmarshal([]byte(stdout), &statusResp))
	assert.Equal(t, "project", statusResp.InstallMode)
	assert.Equal(t, "0600", statusResp.DBMode)
	assert.Equal(t, 2, statusResp.FilesIndexed)
	assert.Greater(t, statusResp.ChunksIndexed, 0)
	assert.Contains(t, statusResp.DBPath, ".proxer.db")

	// 2. Harness mode orphan detection & --prune.
	orphanRoot := setupFixtureRoot(t)
	code, _, stderr = runCLI([]string{"--root", orphanRoot, "index"}, "")
	require.Equal(t, 0, code, stderr)

	// Delete orphanRoot on disk, then inspect and prune from projectRoot.
	require.NoError(t, os.RemoveAll(orphanRoot))

	code, stdout, stderr = runCLI([]string{"--root", projectRoot, "status"}, "")
	require.Equal(t, 0, code, stderr)
	require.NoError(t, json.Unmarshal([]byte(stdout), &statusResp))
	require.Len(t, statusResp.OrphanDatabases, 1)
	assert.False(t, statusResp.OrphanDatabases[0].Pruned)
	orphanDB := statusResp.OrphanDatabases[0].DBPath
	assert.Contains(t, orphanDB, filepath.Join(skillDir, "database"))

	code, stdout, stderr = runCLI([]string{"--root", projectRoot, "status", "--prune"}, "")
	require.Equal(t, 0, code, stderr)
	require.NoError(t, json.Unmarshal([]byte(stdout), &statusResp))
	require.Len(t, statusResp.OrphanDatabases, 1)
	assert.True(t, statusResp.OrphanDatabases[0].Pruned)
	_, statErr := os.Stat(orphanDB)
	assert.True(t, os.IsNotExist(statErr))
}

func TestMCPCommandRejectsExtraArgsAndUnwritableDB(t *testing.T) {
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
