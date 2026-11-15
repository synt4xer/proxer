package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/config"
)

func writeProjectConfig(t *testing.T, body string) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, config.ConfigFile), []byte(body), 0o644))
	return root
}

func TestLoadIgnoredDirsFallsBackToDefaults(t *testing.T) {
	ignored, err := config.LoadIgnoredDirs(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, config.DefaultIgnoredDirs, ignored)
}

func TestLoadIgnoredDirsTreatsAnEmptyFileAsAbsent(t *testing.T) {
	root := writeProjectConfig(t, "\n  \n")

	ignored, err := config.LoadIgnoredDirs(root)
	require.NoError(t, err)
	assert.Equal(t, config.DefaultIgnoredDirs, ignored)
}

func TestLoadIgnoredDirsExtendsTheDefaults(t *testing.T) {
	root := writeProjectConfig(t, `{"ignored_dirs": ["third_party", "mocks"]}`)

	ignored, err := config.LoadIgnoredDirs(root)
	require.NoError(t, err)
	assert.Len(t, ignored, len(config.DefaultIgnoredDirs)+2, "the project list must add to the defaults")
	assert.Contains(t, ignored, "third_party")
	assert.Contains(t, ignored, "mocks")
	assert.Contains(t, ignored, "node_modules", "the defaults must stay in force")
}

func TestLoadIgnoredDirsDropsDuplicatesOfTheDefaults(t *testing.T) {
	root := writeProjectConfig(t, `{"ignored_dirs": ["node_modules", "third_party"]}`)

	ignored, err := config.LoadIgnoredDirs(root)
	require.NoError(t, err)
	assert.Len(t, ignored, len(config.DefaultIgnoredDirs)+1, "a default repeated by the project must not be listed twice")
}

func TestLoadIgnoredDirsRejectsMalformedConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "NotJSON", body: "{"},
		{name: "WrongType", body: `{"ignored_dirs": "third_party"}`},
		{name: "UnknownField", body: `{"ignored": []}`},
		{name: "EmptyName", body: `{"ignored_dirs": [""]}`},
		{name: "PathInsteadOfName", body: `{"ignored_dirs": ["internal/generated"]}`},
		{name: "Traversal", body: `{"ignored_dirs": [".."]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.LoadIgnoredDirs(writeProjectConfig(t, tt.body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), config.ConfigFile)
		})
	}
}
