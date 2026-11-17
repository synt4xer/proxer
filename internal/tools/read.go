package tools

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/indexer"
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

	reader := bufio.NewReader(bytes.NewReader(data))
	var contentBuilder bytes.Buffer
	currentLine := 1

	for {
		line, err := reader.ReadString('\n')
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("error reading file '%s': %w", path, err)
		}

		if currentLine >= startLine && currentLine <= endLine {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			contentBuilder.WriteString(indexer.TruncateLine(line, config.MaxLineWidth))
			contentBuilder.WriteByte('\n')
		}
		if currentLine > endLine {
			break
		}
		currentLine++

		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("error reading file '%s': %w", path, err)
		}
	}

	result := domain.ReadResultJSON{
		Root:     root,
		Filepath: path,
		Content:  contentBuilder.String(),
		Hash:     indexer.ContentHash(data),
	}

	out, err := encodeJSON(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return out, nil
}
