package tools

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/synt4xer/proxer/internal/domain"
)

func PerformRead(path string, startLine, endLine int) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("file '%s' not found. It may have been deleted", path)
	}

	file, err := os.Open(path)
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
		Filepath: path,
		Content:  contentBuilder.String(),
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return string(out), nil
}
