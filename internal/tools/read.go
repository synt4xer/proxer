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

	reqStart := startLine
	if reqStart < 1 {
		reqStart = 1
	}

	reader := bufio.NewReader(bytes.NewReader(data))
	var contentBuilder bytes.Buffer
	totalLines := 0
	actualEnd := 0
	budgetExhausted := false

	for {
		line, err := reader.ReadString('\n')
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return domain.ReadResultJSON{}, fmt.Errorf("error reading file '%s': %w", path, err)
		}

		totalLines++
		if totalLines >= reqStart && totalLines <= endLine && !budgetExhausted {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			truncatedLine := indexer.TruncateLine(line, config.MaxLineWidth)
			lineLen := len(truncatedLine) + 1
			if actualEnd > 0 && contentBuilder.Len()+lineLen > config.MaxReadBytes {
				budgetExhausted = true
			} else {
				contentBuilder.WriteString(truncatedLine)
				contentBuilder.WriteByte('\n')
				actualEnd = totalLines
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return domain.ReadResultJSON{}, fmt.Errorf("error reading file '%s': %w", path, err)
		}
	}

	effectiveEnd := min(endLine, totalLines)
	res := domain.ReadResultJSON{
		Root:       root,
		Filepath:   path,
		TotalLines: totalLines,
		Content:    contentBuilder.String(),
		Hash:       indexer.ContentHash(data),
	}
	if actualEnd > 0 {
		res.StartLine = reqStart
		res.EndLine = actualEnd
		res.Truncated = actualEnd < effectiveEnd
		if actualEnd < totalLines {
			res.NextStart = actualEnd + 1
		}
	}

	return res, nil
}
