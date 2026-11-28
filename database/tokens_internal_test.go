package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldExtractTailsInternal(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{"internal/tools/search.go", true},
		{"main.rs", true},
		{"bundle.min.js", false},
		{"README.md", false},
		{"docs/guide.mdx", false},
		{"notes.txt", false},
		{"spec.rst", false},
		{"app.js.map", false},
		{"go.sum", false},
		{"pnpm.lock", false},
		{"config.yaml", true},
		{"internal/Server_Config.GO", true},
	}

	for _, tc := range tests {
		t.Run(tc.rel, func(t *testing.T) {
			assert.Equal(t, tc.want, shouldExtractTails(tc.rel))
		})
	}
}

func TestSplitIdentifierEdges(t *testing.T) {
	assert.Nil(t, SplitIdentifier(""), "an empty identifier has no tokens")
	assert.Equal(t, []string{"auth", "middleware"}, SplitIdentifier("AuthMiddleware"))
	assert.Equal(t, []string{"auth", "middleware"}, SplitIdentifier("_Auth_Middleware_"))
	assert.Equal(t, []string{"http", "server"}, SplitIdentifier("HTTPServer"))
	assert.Equal(t, []string{"v2"}, SplitIdentifier("v2"))
	assert.Equal(t, []string{"a"}, SplitIdentifier("A"), "a short segment must still be indexed")
	assert.Equal(t, []string{"a", "bb"}, SplitIdentifier("aBB"), "a 2-character tail is below the split threshold")
}

func TestStringEqualBytes(t *testing.T) {
	assert.True(t, stringEqualBytes("abc", []byte("abc")))
	assert.False(t, stringEqualBytes("abc", []byte("abd")))
	assert.True(t, stringEqualBytes("abc", []byte("")), "an empty needle always matches")
}

func TestFilesReportsBrokenTable(t *testing.T) {
	db := innerDB(t)
	_, err := db.Exec("DROP TABLE indexed_files")
	require.NoError(t, err)

	_, err = Files(db)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read the index manifest")
}

func TestRemoveReportsBrokenManifest(t *testing.T) {
	db := innerDB(t)
	tx, err := db.Beginx()
	require.NoError(t, err)
	defer tx.Rollback()

	_, err = tx.Exec("DROP TABLE indexed_files")
	require.NoError(t, err)

	err = Remove(tx, "a.go")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to drop the manifest row")
}
