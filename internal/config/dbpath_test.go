package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveDBPath(t *testing.T) {
	t.Run("override via explicit flag and env var", func(t *testing.T) {
		root := t.TempDir()
		override := filepath.Join(t.TempDir(), "custom.db")
		loc, err := ResolveDBPath(root, override, true)
		require.NoError(t, err)
		assert.Equal(t, InstallModeOverride, loc.Mode)
		assert.Equal(t, override, loc.DBPath)

		t.Setenv(DBEnvVar, override)
		loc, err = ResolveDBPath(root, "", false)
		require.NoError(t, err)
		assert.Equal(t, InstallModeOverride, loc.Mode)
		assert.Equal(t, override, loc.DBPath)
	})

	t.Run("project mode when .agents/skills/proxer or .proxer.db exists", func(t *testing.T) {
		root := t.TempDir()
		realRoot := resolveRealPath(root)
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents", "skills", "proxer"), 0o755))

		skillDir := t.TempDir()
		t.Setenv(SkillDirEnvVar, skillDir)

		loc, err := ResolveDBPath(root, "", true)
		require.NoError(t, err)
		assert.Equal(t, InstallModeProject, loc.Mode)
		assert.Equal(t, filepath.Join(realRoot, DBFile), loc.DBPath)

		root2 := t.TempDir()
		realRoot2 := resolveRealPath(root2)
		require.NoError(t, os.WriteFile(filepath.Join(root2, DBFile), []byte{}, 0o600))
		loc2, err := ResolveDBPath(root2, "", true)
		require.NoError(t, err)
		assert.Equal(t, InstallModeProject, loc2.Mode)
		assert.Equal(t, filepath.Join(realRoot2, DBFile), loc2.DBPath)
	})

	t.Run("harness and MCP mode isolate same-named folders", func(t *testing.T) {
		skillDir := t.TempDir()
		t.Setenv(SkillDirEnvVar, skillDir)

		parent1 := t.TempDir()
		parent2 := t.TempDir()
		app1 := filepath.Join(parent1, "app")
		app2 := filepath.Join(parent2, "app")
		require.NoError(t, os.MkdirAll(app1, 0o755))
		require.NoError(t, os.MkdirAll(app2, 0o755))

		loc1, err := ResolveDBPath(app1, "", true)
		require.NoError(t, err)
		loc1Again, err := ResolveDBPath(app1, "", true)
		require.NoError(t, err)
		loc2, err := ResolveDBPath(app2, "", true)
		require.NoError(t, err)

		assert.Equal(t, InstallModeHarness, loc1.Mode)
		assert.Equal(t, loc1.DBPath, loc1Again.DBPath)
		assert.NotEqual(t, loc1.DBPath, loc2.DBPath)
		assert.Contains(t, loc1.DBPath, filepath.Join(resolveRealPath(skillDir), "database", "app-"))

		info, err := os.Stat(filepath.Join(skillDir, "database"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	})

	t.Run("project-local binary is never treated as harness skill dir", func(t *testing.T) {
		t.Setenv(SkillDirEnvVar, "")
		home, err := os.UserHomeDir()
		require.NoError(t, err)

		projectExec := filepath.Join(home, "Documents", "repoA", ".agents", "skills", "proxer", "scripts", "proxer")
		assert.Empty(t, HarnessSkillDir(projectExec))

		userHarnessExec := filepath.Join(home, ".agents", "skills", "proxer", "scripts", "proxer")
		assert.Equal(t, resolveRealPath(filepath.Join(home, ".agents", "skills", "proxer")), HarnessSkillDir(userHarnessExec))
	})
}
