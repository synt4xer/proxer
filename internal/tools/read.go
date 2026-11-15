package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"os"

	"github.com/synt4xer/proxer/internal/domain"
)

// PerformRead returns the requested line range of a file. The path is resolved
// against root but echoed back as given, so agents can chain read and edit
// calls with the same spelling they already have in context.
func PerformRead(root, path string, startLine, endLine int) (string, error) {
	resolved, err := resolvePath(root, path)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(resolved); os.IsNotExist(err) {
		return "", fmt.Errorf("file '%s' not found. It may have been deleted", path)
	}

	file, err := os.Open(resolved)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var contentBuilder bytes.Buffer
	currentLine := 1

	for scanner.Scan() {
		if currentLine >= startLine && currentLine <= endLine {
			contentBuilder.WriteString(scanner.Text())
			contentBuilder.WriteByte('\n')
		}
		if currentLine > endLine {
			break
		}
		currentLine++
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading file '%s': %w", path, err)
	}

	result := domain.ReadResultJSON{
		Root:     root,
		Filepath: path,
		Content:  contentBuilder.String(),
	}

	out, err := encodeJSON(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return out, nil
}
