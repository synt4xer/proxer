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
func PerformRead(root, path string, startLine, endLine int) (domain.ReadResultJSON, error) {
	resolved, err := resolvePath(root, path)
	if err != nil {
		return domain.ReadResultJSON{}, err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return domain.ReadResultJSON{}, fmt.Errorf("file '%s' not found. It may have been deleted", path)
		}
		return domain.ReadResultJSON{}, fmt.Errorf("failed to open file: %w", err)
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
			return domain.ReadResultJSON{}, fmt.Errorf("error reading file '%s': %w", path, err)
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
			return domain.ReadResultJSON{}, fmt.Errorf("error reading file '%s': %w", path, err)
		}
	}

	return domain.ReadResultJSON{
		Root:     root,
		Filepath: path,
		Content:  contentBuilder.String(),
		Hash:     indexer.ContentHash(data),
	}, nil
}
