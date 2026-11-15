package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/config"
)

func TestResolveRootPrecedence(t *testing.T) {
	flagRoot := t.TempDir()
	envRoot := t.TempDir()
	workRoot := t.TempDir()

	t.Setenv(config.RootEnvVar, envRoot)
	t.Chdir(workRoot)

	resolved, err := config.ResolveRoot(flagRoot)
	require.NoError(t, err)
	assert.Equal(t, flagRoot, resolved, "the --root flag must beat the environment")

	if realFlagRoot, err := filepath.EvalSymlinks(flagRoot); err == nil {
		realResolved, err := filepath.EvalSymlinks(resolved)
		require.NoError(t, err)
		assert.Equal(t, realFlagRoot, realResolved)
	}
}

func TestResolveRootFallsBackToEnvironment(t *testing.T) {
	envRoot := t.TempDir()
	t.Setenv(config.RootEnvVar, envRoot)
	t.Chdir(t.TempDir())

	resolved, err := config.ResolveRoot("")
	require.NoError(t, err)
	assert.Equal(t, envRoot, resolved, "an empty flag must fall through to the environment")
}

func TestResolveRootFallsBackToWorkingDirectory(t *testing.T) {
	workRoot := t.TempDir()
	t.Setenv(config.RootEnvVar, "")
	t.Chdir(workRoot)

	resolved, err := config.ResolveRoot("")
	require.NoError(t, err)

	expected, err := filepath.EvalSymlinks(workRoot)
	require.NoError(t, err)
	actual, err := filepath.EvalSymlinks(resolved)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)
}

func TestResolveRootRejectsUnusablePaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	_, err := config.ResolveRoot(missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not usable")

	file := filepath.Join(t.TempDir(), "not-a-dir.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	_, err = config.ResolveRoot(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestResolveRootReturnsAbsolutePaths(t *testing.T) {
	t.Setenv(config.RootEnvVar, "")
	t.Chdir(t.TempDir())

	resolved, err := config.ResolveRoot("..")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(resolved), "relative input must come back absolute, got %q", resolved)
}

func TestResolveRootExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	t.Setenv(config.RootEnvVar, "")

	// MCP configuration passes arguments literally, so "~" has to be expanded
	// here rather than by a shell that is not involved.
	resolved, err := config.ResolveRoot("~")
	require.NoError(t, err)

	expected, err := filepath.EvalSymlinks(home)
	require.NoError(t, err)
	actual, err := filepath.EvalSymlinks(resolved)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)
}
