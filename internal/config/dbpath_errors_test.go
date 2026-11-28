package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeBasename(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "root"},
		{".", "root"},
		{"/", "root"},
		{"my-app", "my-app"},
		{"my app", "my_app"},
		{"a/b", "a_b"},
		{"üñî", "___"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeBasename(tc.in))
		})
	}
}

func TestSha256HexIsStableAndShortenable(t *testing.T) {
	a := sha256Hex("/tmp/project")
	b := sha256Hex("/tmp/project")

	assert.Equal(t, a, b, "the same root must always hash the same")
	assert.NotEqual(t, a, sha256Hex("/tmp/other"))
	assert.Len(t, a[:12], 12)
}

func TestHarnessSkillDirPrefersExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(SkillDirEnvVar, dir)

	assert.Equal(t, resolveRealPath(dir), HarnessSkillDir("/usr/local/bin/proxer"))
}

func TestHarnessSkillDirIgnoresStandaloneBinary(t *testing.T) {
	t.Setenv(SkillDirEnvVar, "")

	assert.Empty(t, HarnessSkillDir(""), "an unknown executable cannot live in a harness skill dir")
	assert.Empty(t, HarnessSkillDir("/usr/local/bin/proxer"))
}

func TestHarnessSkillDirRecognizesUserLevelSkill(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	t.Setenv(SkillDirEnvVar, "")

	skillBin := filepath.Join(home, ".agents", "skills", "proxer", "proxer")

	assert.Equal(t, filepath.Join(home, ".agents", "skills", "proxer"), HarnessSkillDir(skillBin))
}

func TestHarnessSkillDirRejectsProjectLocalBinary(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	t.Setenv(SkillDirEnvVar, "")

	// A skill directory that is not hidden is project-local, not a user harness.
	projectBin := filepath.Join(home, "code", "app", ".agents", "skills", "proxer", "proxer")

	assert.Empty(t, HarnessSkillDir(projectBin))
}

func TestResolveDBPathExplicitWinsOverEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv(DBEnvVar, filepath.Join(t.TempDir(), "env.db"))

	loc, err := ResolveDBPath(root, filepath.Join(t.TempDir(), "explicit.db"), false)

	require.NoError(t, err)
	assert.Equal(t, InstallModeOverride, loc.Mode)
	assert.True(t, strings.HasSuffix(loc.DBPath, "explicit.db"))
}

func TestResolveDBPathUsesEnvOverride(t *testing.T) {
	root := t.TempDir()
	envDB := filepath.Join(t.TempDir(), "env.db")
	t.Setenv(DBEnvVar, envDB)

	loc, err := ResolveDBPath(root, "", false)

	require.NoError(t, err)
	assert.Equal(t, InstallModeOverride, loc.Mode)
	assert.Equal(t, envDB, loc.DBPath)
}

func TestResolveDBPathProjectLocalWhenDBFileExists(t *testing.T) {
	root := t.TempDir()
	t.Setenv(DBEnvVar, "")
	require.NoError(t, os.WriteFile(filepath.Join(root, DBFile), []byte("x"), 0o600))

	loc, err := ResolveDBPath(root, "", false)

	require.NoError(t, err)
	assert.Equal(t, InstallModeProject, loc.Mode)
	assert.Equal(t, filepath.Join(resolveRealPath(root), DBFile), loc.DBPath)
}

func TestResolveDBPathProjectLocalForPlainCLI(t *testing.T) {
	root := t.TempDir()
	t.Setenv(DBEnvVar, "")
	t.Setenv(SkillDirEnvVar, "")
	t.Setenv("HOME", t.TempDir())

	loc, err := ResolveDBPath(root, "", false)

	require.NoError(t, err)
	assert.Equal(t, InstallModeProject, loc.Mode)
	assert.Equal(t, filepath.Join(resolveRealPath(root), DBFile), loc.DBPath)
}

// TestResolveDBPathHarnessCreatesDatabaseDir pins the harness layout: one
// database per project, named from the project basename and a root hash, so two
// projects never share an index.
func TestResolveDBPathHarnessCreatesDatabaseDir(t *testing.T) {
	root := t.TempDir()
	skillDir := t.TempDir()
	t.Setenv(DBEnvVar, "")
	t.Setenv(SkillDirEnvVar, skillDir)

	loc, err := ResolveDBPath(root, "", true)

	require.NoError(t, err)
	assert.Equal(t, InstallModeHarness, loc.Mode)
	assert.Equal(t, resolveRealPath(filepath.Join(skillDir, "database")), filepath.Dir(loc.DBPath))
	assert.True(t, strings.HasSuffix(loc.DBPath, ".db"))
	assert.Contains(t, filepath.Base(loc.DBPath), sha256Hex(resolveRealPath(root))[:12])

	info, err := os.Stat(filepath.Dir(loc.DBPath))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "the harness database directory must stay private")
}

func TestResolveDBPathHarnessFallsBackToHome(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv(DBEnvVar, "")
	t.Setenv(SkillDirEnvVar, "")
	t.Setenv("HOME", home)

	loc, err := ResolveDBPath(root, "", true)

	require.NoError(t, err)
	assert.Equal(t, InstallModeHarness, loc.Mode)
	assert.Equal(t, filepath.Join(home, ".agents", "skills", "proxer"), loc.SkillDir)
}

func TestResolveRealPathFallsBackWhenMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")

	assert.NotEmpty(t, resolveRealPath(missing))
	assert.Contains(t, resolveRealPath(missing), "nope")
}
