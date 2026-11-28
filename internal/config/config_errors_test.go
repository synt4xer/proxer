package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRootRejectsMissingDirectory(t *testing.T) {
	root, err := ResolveRoot(filepath.Join(t.TempDir(), "missing"))

	require.Error(t, err)
	assert.Empty(t, root)
	assert.Contains(t, err.Error(), "is not usable")
}

func TestResolveRootRejectsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	root, err := ResolveRoot(file)

	require.Error(t, err)
	assert.Empty(t, root)
	assert.Contains(t, err.Error(), "is not a directory")
}

func TestResolveRootAcceptsADirectory(t *testing.T) {
	dir := t.TempDir()

	root, err := ResolveRoot(dir)

	require.NoError(t, err)
	assert.Equal(t, dir, root)
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	assert.Equal(t, home, expandHome("~"))
	assert.Equal(t, filepath.Join(home, "code", "app"), expandHome("~/code/app"))
	assert.Equal(t, "relative/path", expandHome("relative/path"), "a path without a tilde must pass through")
	assert.Equal(t, "/absolute/path", expandHome("/absolute/path"))
}

func TestLoadProjectConfigReportsUnreadableFile(t *testing.T) {
	root := t.TempDir()
	// A directory named like the config file is readable in name only.
	require.NoError(t, os.Mkdir(filepath.Join(root, ConfigFile), 0o700))

	_, err := LoadProjectConfig(root)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read")
}

func TestValidateIgnoredDirRejectsBadEntries(t *testing.T) {
	tests := []struct {
		name    string
		dir     string
		wantErr string
	}{
		{"path separator", "a/b", "must be a single directory name"},
		{"backslash", `a\b`, "must be a single directory name"},
		{"dot", ".", "must be a single directory name"},
		{"dotdot", "..", "must be a single directory name"},
		{"bad glob", "[", "invalid glob pattern"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateIgnoredDir(tc.dir)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestValidateIgnoredDirAcceptsAPlainName(t *testing.T) {
	assert.NoError(t, validateIgnoredDir("vendor"))
	assert.NoError(t, validateIgnoredDir("node_modules"))
}
