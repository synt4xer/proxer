package indexer_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/indexer"
)

func TestIndexerEngine(t *testing.T) {
	// 1. Setup Temporary Workspace
	// This ensures our tests do not pollute the actual project directory
	tempDir := t.TempDir()

	// Change working directory to tempDir so config.DBFile is created there
	originalWD, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(originalWD)

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	// 2. Create mock files to index
	err = os.Mkdir("internal", 0755)
	require.NoError(t, err)

	mockGoCode := `package main
import "fmt"
func hello() {
	fmt.Println("Hello Agent!")
}
`
	err = os.WriteFile("internal/hello.go", []byte(mockGoCode), 0644)
	require.NoError(t, err)

	// Create an ignored file to ensure it gets skipped
	err = os.WriteFile("ignore_me.png", []byte("fake png binary data"), 0644)
	require.NoError(t, err)

	// 3. Test Database Initialization
	db, err := indexer.InitDB()
	require.NoError(t, err, "InitDB should not produce an error")
	defer db.Close()

	// Verify table was created
	var tableName string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='chunk_search'").Scan(&tableName)
	assert.NoError(t, err)
	assert.Equal(t, "chunk_search", tableName)

	// 4. Test Project Indexing
	err = indexer.IndexProject(db, ".")
	require.NoError(t, err, "IndexProject should complete without errors")

	// 5. Verify the data was indexed correctly
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM chunk_search").Scan(&count)
	assert.NoError(t, err)

	// We expect exactly 1 chunk because the .png is ignored and the .go file is small
	assert.Equal(t, 1, count, "Only internal/hello.go should be indexed")

	// Verify content matching
	var filepath string
	var startLine, endLine int
	err = db.QueryRow("SELECT filepath, start_line, end_line FROM chunk_search WHERE content MATCH 'Agent'").Scan(&filepath, &startLine, &endLine)

	assert.NoError(t, err)
	assert.Equal(t, "internal/hello.go", filepath)
	assert.Equal(t, 1, startLine)
	assert.True(t, endLine > 1)
}
