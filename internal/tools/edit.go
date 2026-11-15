package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/internal/indexer"
)

func PerformEdit(db *sqlx.DB, filepath string, startLine, endLine int, newContent string) (string, error) {
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		return "", fmt.Errorf("file '%s' not found", filepath)
	}

	contentBytes, err := os.ReadFile(filepath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	lines := strings.Split(string(contentBytes), "\n")
	totalLines := len(lines)

	if startLine < 1 || startLine > totalLines || endLine < startLine || endLine > totalLines {
		return "", fmt.Errorf("invalid line range. The file has %d lines", totalLines)
	}

	prefix := lines[:startLine-1]
	suffix := lines[endLine:]

	cleanNewContent := strings.TrimRight(newContent, "\r\n")

	var finalLines []string
	finalLines = append(finalLines, prefix...)
	finalLines = append(finalLines, cleanNewContent)
	finalLines = append(finalLines, suffix...)

	finalContent := strings.Join(finalLines, "\n")

	err = os.WriteFile(filepath, []byte(finalContent), 0644)
	if err != nil {
		return "", fmt.Errorf("failed to save file: %w", err)
	}

	err = indexer.IndexProject(db, ".")
	if err != nil {
		return "", fmt.Errorf("file saved, but auto-index failed: %w", err)
	}

	successMsg := map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Successfully replaced lines %d-%d in %s and auto-updated the search index.", startLine, endLine, filepath),
	}
	out, err := json.Marshal(successMsg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return string(out), nil
}
