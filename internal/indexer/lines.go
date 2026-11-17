package indexer

import (
	"bytes"
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

// HasLongLine reports whether any line in content exceeds maxWidth runes. It is
// used by the indexer to skip minified bundles and generated single-line files
// without pulling them into the search index.
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
