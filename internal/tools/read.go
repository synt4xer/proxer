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
// calls with the same spelling they already have in context. The hash covers
// the whole file, so edit can tell whether anything shifted since the read.
func PerformRead(root, path string, startLine, endLine int) (string, error) {
	resolved, err := resolvePath(root, path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file '%s' not found. It may have been deleted", path)
		}
		return "", fmt.Errorf("failed to open file: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
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
		Hash:     contentHash(data),
	}

	out, err := encodeJSON(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return out, nil
}
