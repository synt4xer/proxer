package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolvePathRejectsEmptyPath(t *testing.T) {
	root := t.TempDir()

	for _, p := range []string{"", "   "} {
		_, err := resolvePath(root, p)
		require.Error(t, err, "path %q must be rejected", p)
		assert.Contains(t, err.Error(), "a file path is required")
	}
}

func TestResolvePathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	require.NoError(t, os.WriteFile(outside, []byte("package outside\n"), 0o600))

	_, err := resolvePath(root, "../outside.go")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the project root")
}

func TestResolvePathAcceptsARelativeFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600))

	resolved, err := resolvePath(root, "a.go")

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "a.go"), resolved)
}

// TestResolvePathRejectsSymlinkEscape pins the symlink arm of confinement: a
// link inside the project that points outside must not be followed.
func TestResolvePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.go")
	require.NoError(t, os.WriteFile(outside, []byte("package secret\n"), 0o600))

	link := filepath.Join(root, "link.go")
	require.NoError(t, os.Symlink(outside, link))

	_, err := resolvePath(root, "link.go")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the project root")
}

func TestHasPrefixPath(t *testing.T) {
	root := string(filepath.Separator) + filepath.Join("project")

	assert.True(t, hasPrefixPath(root, root), "a path is its own prefix")
	assert.True(t, hasPrefixPath(root, filepath.Join(root, "sub", "a.go")))
	assert.False(t, hasPrefixPath(root, root+"-other"), "a sibling with a shared prefix is not inside")
	assert.False(t, hasPrefixPath(root, filepath.Join(root, "..", "elsewhere")))
}

func TestPerformReadRejectsAMissingFile(t *testing.T) {
	root := t.TempDir()

	_, err := PerformRead(root, "absent.go", 1, 10)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPerformReadRejectsADirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "adir"), 0o700))

	_, err := PerformRead(root, "adir", 1, 10)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open file")
}

func TestPerformReadClampsAPositiveStart(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc a() {}\n"), 0o600))

	result, err := PerformRead(root, "a.go", 0, 2)

	require.NoError(t, err)
	assert.Equal(t, 1, result.StartLine, "a start below 1 must clamp to the first line")
	assert.NotEmpty(t, result.Content)
}

func TestEncodeJSONIsNotHTMLEscaped(t *testing.T) {
	out, err := EncodeJSON(map[string]string{"q": "a<b>&c"})

	require.NoError(t, err)
	assert.Contains(t, out, "a<b>&c", "angle brackets and ampersands must survive for agent consumers")
	assert.Equal(t, out, strings.TrimSpace(out), "the payload must be a single compact line")
}

func TestEncodeJSONReportsUnsupportedValues(t *testing.T) {
	_, err := EncodeJSON(make(chan int))

	require.Error(t, err)
}
