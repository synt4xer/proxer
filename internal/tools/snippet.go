package tools

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const snippetTargetRunes = 180

// locateHitAndBuildSnippet finds the 1-based line number of the first content
// match inside a chunk and builds a UTF-8-safe 160-200 char snippet centered
// on that hit with matched terms wrapped in <b>...</b>. For path-only hits, it
// returns hitLine 0 and the leading snippet without <b> highlights.
func locateHitAndBuildSnippet(content string, startLine int, terms []string, match string) (int, string) {
	if match == "path" || len(terms) == 0 {
		return 0, leadingSnippet(content, snippetTargetRunes)
	}

	hitLine, matchRune := findFirstHit(content, startLine, terms)
	runes := []rune(content)
	total := len(runes)
	if total == 0 {
		return hitLine, ""
	}

	startRune := 0
	endRune := total
	if total > snippetTargetRunes {
		startRune = max(0, matchRune-snippetTargetRunes/3)
		endRune = min(total, startRune+snippetTargetRunes)
		if endRune == total {
			startRune = max(0, endRune-snippetTargetRunes)
		}
	}

	window := string(runes[startRune:endRune])
	highlighted := highlightTerms(window, terms)

	var b strings.Builder
	if startRune > 0 {
		b.WriteString("...")
	}
	b.WriteString(highlighted)
	if endRune < total {
		b.WriteString("...")
	}
	return hitLine, b.String()
}

func leadingSnippet(content string, maxRunes int) string {
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content
	}
	return string(runes[:maxRunes]) + "..."
}

func findFirstHit(content string, startLine int, terms []string) (hitLine int, matchRune int) {
	lowerTerms := make([][]rune, 0, len(terms))
	for _, t := range terms {
		if t != "" {
			lowerTerms = append(lowerTerms, []rune(strings.ToLower(t)))
		}
	}
	if len(lowerTerms) == 0 {
		return 0, 0
	}

	lineNum := startLine
	byteOffset := 0
	for byteOffset <= len(content) {
		relEnd := strings.IndexByte(content[byteOffset:], '\n')
		var line string
		var nextOffset int
		if relEnd < 0 {
			line = content[byteOffset:]
			nextOffset = len(content) + 1
		} else {
			line = content[byteOffset : byteOffset+relEnd]
			nextOffset = byteOffset + relEnd + 1
		}

		if idx := firstTermRuneIndex(line, lowerTerms); idx >= 0 {
			lineStartRune := utf8.RuneCountInString(content[:byteOffset])
			return lineNum, lineStartRune + idx
		}

		lineNum++
		byteOffset = nextOffset
	}
	return 0, 0
}

func firstTermRuneIndex(line string, lowerTerms [][]rune) int {
	lineRunes := []rune(line)
	best := -1
	for _, term := range lowerTerms {
		if idx := indexRunesFold(lineRunes, term); idx >= 0 {
			if best < 0 || idx < best {
				best = idx
			}
		}
	}
	return best
}

func indexRunesFold(haystack, lowerNeedle []rune) int {
	n := len(lowerNeedle)
	if n == 0 || len(haystack) < n {
		return -1
	}
	for i := 0; i <= len(haystack)-n; i++ {
		if runesEqualFold(haystack[i:i+n], lowerNeedle) {
			return i
		}
	}
	return -1
}

func runesEqualFold(a, lowerB []rune) bool {
	for i := range lowerB {
		if unicode.ToLower(a[i]) != lowerB[i] {
			return false
		}
	}
	return true
}

// highlightTerms wraps case-insensitive occurrences of terms in <b>...</b>
// using rune-level matching so UTF-8 boundaries and non-ASCII case folding
// never corrupt the output.
func highlightTerms(text string, terms []string) string {
	lowerTerms := make([][]rune, 0, len(terms))
	for _, t := range terms {
		if t != "" {
			lowerTerms = append(lowerTerms, []rune(strings.ToLower(t)))
		}
	}
	if len(lowerTerms) == 0 {
		return text
	}

	runes := []rune(text)
	var b strings.Builder
	b.Grow(len(text) + 16)

	for i := 0; i < len(runes); {
		matchLen := 0
		for _, term := range lowerTerms {
			if len(term) > matchLen && i+len(term) <= len(runes) && runesEqualFold(runes[i:i+len(term)], term) {
				matchLen = len(term)
			}
		}
		if matchLen > 0 {
			b.WriteString("<b>")
			b.WriteString(string(runes[i : i+matchLen]))
			b.WriteString("</b>")
			i += matchLen
		} else {
			b.WriteRune(runes[i])
			i++
		}
	}
	return b.String()
}
