package tools_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/indexer"
	"github.com/synt4xer/proxer/internal/tools"
)

func setupTestEnvironment(t *testing.T) (string, string) {
	// Create a safe, isolated temporary directory
	tempDir := t.TempDir()
	originalWD, err := os.Getwd()
	require.NoError(t, err)

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	// Write a mock code file
	mockCode := `package main
import "fmt"

func ProcessData() {
	fmt.Println("Processing...")
	// We need to change this logic later
	return
}
`
	err = os.WriteFile("main.go", []byte(mockCode), 0644)
	require.NoError(t, err)

	return tempDir, originalWD
}

func TestToolsSuite(t *testing.T) {
	_, originalWD := setupTestEnvironment(t)
	// Ensure we go back to the original directory when tests finish
	defer os.Chdir(originalWD)

	// 1. Setup DB and Index the mock workspace
	db, err := indexer.InitDB()
	require.NoError(t, err)
	defer db.Close()

	err = indexer.IndexProject(db, ".")
	require.NoError(t, err)

	// --- 2. Test PerformSearch ---
	t.Run("PerformSearch", func(t *testing.T) {
		resultJSON, err := tools.PerformSearch(db, "ProcessData")
		require.NoError(t, err)

		// Assert output is a valid JSON and contains our target
		assert.Contains(t, resultJSON, "main.go")
		assert.Contains(t, resultJSON, "ProcessData")

		// Test missing keyword (should return empty array JSON)
		emptyJSON, err := tools.PerformSearch(db, "NonExistentKeyword")
		require.NoError(t, err)
		assert.Equal(t, "[]", emptyJSON)
	})

	// --- 3. Test PerformRead ---
	t.Run("PerformRead", func(t *testing.T) {
		resultJSON, err := tools.PerformRead("main.go", 4, 5)
		require.NoError(t, err)

		// Lines 4-5 should contain "func ProcessData() {" and the print statement
		assert.Contains(t, resultJSON, "func ProcessData()")
		assert.Contains(t, resultJSON, "fmt.Println")

		// Test out of bounds or missing file
		_, err = tools.PerformRead("missing.go", 1, 5)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	// --- 4. Test PerformEdit ---
	t.Run("PerformEdit", func(t *testing.T) {
		newContent := "\tfmt.Println(\"Data Processed Successfully\")\n\treturn true"

		// Edit lines 5 to 7
		resultJSON, err := tools.PerformEdit(db, "main.go", 5, 7, newContent)
		require.NoError(t, err)
		assert.Contains(t, resultJSON, "success")

		// Verify disk file actually changed
		updatedBytes, err := os.ReadFile("main.go")
		require.NoError(t, err)
		updatedContent := string(updatedBytes)
		assert.Contains(t, updatedContent, "Data Processed Successfully")
		assert.NotContains(t, updatedContent, "We need to change this logic later")

		// Verify FTS5 Auto-Reindex worked by searching the new word
		searchResult, err := tools.PerformSearch(db, "Successfully")
		require.NoError(t, err)
		assert.Contains(t, searchResult, "main.go", "Auto-reindex failed to register new content")
	})
}
