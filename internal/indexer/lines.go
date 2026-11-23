package indexer

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/synt4xer/proxer/internal/config"
)

// TruncateLine truncates line if its rune length exceeds maxWidth, appending
// config.TruncationMarker so an agent can see the line was cut rather than
// finished. If maxWidth <= 0 or the line fits, it returns line unchanged.
func TruncateLine(line string, maxWidth int) string {
	if maxWidth <= 0 || len(line) <= maxWidth {
		return line
	}

	count := 0
	for i := range line {
		if count == maxWidth {
			return line[:i] + config.TruncationMarker
		}
		count++
	}
	return line
}

func HasLongLine(content []byte, maxWidth int) bool {
	if maxWidth <= 0 {
		return false
	}

	for len(content) > 0 {
		idx := bytes.IndexByte(content, '\n')
		var line []byte
		if idx >= 0 {
			line = content[:idx]
			content = content[idx+1:]
		} else {
			line = content
			content = nil
		}

		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}

		// A line whose byte length is within maxWidth cannot exceed maxWidth runes.
		if len(line) <= maxWidth {
			continue
		}

		if utf8.RuneCount(line) > maxWidth {
			return true
		}
	}

	return false
}

// PrepareContentForIndex inspects content for lines exceeding maxWidth runes:
//   - If no line exceeds maxWidth, it returns the split lines, true without
//     joining and re-splitting.
//   - If at least half of the non-empty lines exceed maxWidth (typical of
//     minified bundles and single-line generated blobs), it returns nil, false
//     so the file is skipped from chunk storage.
//   - Otherwise (a multi-line source file that happens to contain a few long
//     lines such as embedded SVGs, base64 strings, or fixtures), it truncates
//     only the oversized lines via TruncateLine and returns the sanitized
//     lines, true so surrounding code remains searchable while keeping
//     stored FTS5 chunks bounded.
func PrepareContentForIndex(content []byte, maxWidth int) ([]string, bool) {
	if !HasLongLine(content, maxWidth) {
		return strings.Split(string(content), "\n"), true
	}

	lines := strings.Split(string(content), "\n")
	shortLines := 0
	longLines := 0

	for i, line := range lines {
		rawLine := line
		hasCR := false
		if len(rawLine) > 0 && rawLine[len(rawLine)-1] == '\r' {
			rawLine = rawLine[:len(rawLine)-1]
			hasCR = true
		}

		if len(rawLine) > maxWidth && utf8.RuneCountInString(rawLine) > maxWidth {
			longLines++
			if hasCR {
				lines[i] = TruncateLine(rawLine, maxWidth) + "\r"
			} else {
				lines[i] = TruncateLine(rawLine, maxWidth)
			}
		} else if strings.TrimSpace(rawLine) != "" {
			shortLines++
		}
	}

	if longLines >= shortLines {
		return nil, false
	}

	return lines, true
}
