package database

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// shouldExtractTails limits camelCase/PascalCase sub-token extraction to code
// and config files, skipping documentation and generated/minified assets to keep
// first-build indexing cost bounded.
func shouldExtractTails(rel string) bool {
	base := strings.ToLower(filepath.Base(rel))
	if strings.Contains(base, ".min.") {
		return false
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".md", ".mdx", ".txt", ".rst", ".map", ".lock", ".sum":
		return false
	}
	return true
}

// SplitIdentifier splits an identifier on underscores and camelCase/acronym
// boundaries into lowercase tokens (e.g., "AuthMiddleware" -> ["auth", "middleware"],
// "HTTPServer" -> ["http", "server"], "sqlRepo" -> ["sql", "repo"], "v2" -> ["v2"]).
func SplitIdentifier(ident string) []string {
	if ident == "" {
		return nil
	}

	var parts []string
	for _, seg := range strings.Split(ident, "_") {
		if seg == "" {
			continue
		}
		parts = appendCamelParts(parts, seg)
	}
	return parts
}

func appendCamelParts(parts []string, seg string) []string {
	runes := []rune(seg)
	start := 0
	for i := 1; i < len(runes); i++ {
		prev := runes[i-1]
		curr := runes[i]

		lowerOrDigitToUpper := (unicode.IsLower(prev) || unicode.IsDigit(prev)) && unicode.IsUpper(curr)
		acronymBoundary := unicode.IsUpper(prev) && unicode.IsUpper(curr) && i+1 < len(runes) && unicode.IsLower(runes[i+1])

		if lowerOrDigitToUpper || acronymBoundary {
			parts = append(parts, strings.ToLower(string(runes[start:i])))
			start = i
		}
	}
	parts = append(parts, strings.ToLower(string(runes[start:])))
	return parts
}

// identifierTails extracts unique non-head sub-tokens (length >= 3) from
// camelCase/PascalCase identifiers in content. Underscore-separated words are
// skipped because FTS5 unicode61 already tokenizes on '_', and head tokens are
// skipped because prefix matching ("head"*) already matches the full identifier.
func identifierTails(content string) string {
	var seen []string
	var out strings.Builder
	var lowerBuf [64]byte

	n := len(content)
	i := 0
	for i < n {
		// Advance to start of an identifier word (letter or digit; '_' separates words).
		for i < n && !isWordByteOrRuneStart(content, i) {
			i++
		}
		if i >= n {
			break
		}

		start := i
		hasUpperAfterStart := false
		prevLowerOrDigit := isLowerOrDigitASCII(content[i])
		asciiOnly := content[i] < 0x80
		i++

		for i < n {
			b := content[i]
			if b < 0x80 {
				if !isASCIIAlnum(b) {
					break
				}
				if b >= 'A' && b <= 'Z' {
					if prevLowerOrDigit || (i+1 < n && content[i+1] >= 'a' && content[i+1] <= 'z') {
						hasUpperAfterStart = true
					}
					prevLowerOrDigit = false
				} else {
					prevLowerOrDigit = true
				}
				i++
			} else {
				asciiOnly = false
				break
			}
		}

		if !asciiOnly {
			i = start
			for i < n {
				r, size := utf8.DecodeRuneInString(content[i:])
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					break
				}
				i += size
			}
			word := content[start:i]
			parts := appendCamelParts(nil, word)
			for k := 1; k < len(parts); k++ {
				if len(parts[k]) >= 3 {
					appendUniqueTail(&seen, &out, parts[k])
				}
			}
			continue
		}

		if !hasUpperAfterStart {
			continue
		}

		// ASCII fast path: split [start:i] without rune/slice allocations.
		partStart := start
		first := true
		for j := start + 1; j < i; j++ {
			prev := content[j-1]
			curr := content[j]
			lowerOrDigitToUpper := isLowerOrDigitASCII(prev) && curr >= 'A' && curr <= 'Z'
			acronymBoundary := prev >= 'A' && prev <= 'Z' && curr >= 'A' && curr <= 'Z' && j+1 < i && content[j+1] >= 'a' && content[j+1] <= 'z'
			if lowerOrDigitToUpper || acronymBoundary {
				if !first && j-partStart >= 3 {
					appendLowerASCIIUnique(&seen, &out, content[partStart:j], &lowerBuf)
				}
				first = false
				partStart = j
			}
		}
		if !first && i-partStart >= 3 {
			appendLowerASCIIUnique(&seen, &out, content[partStart:i], &lowerBuf)
		}
	}

	return out.String()
}

func appendLowerASCIIUnique(seen *[]string, out *strings.Builder, s string, buf *[64]byte) {
	var lower string
	if len(s) <= len(buf) {
		for idx := 0; idx < len(s); idx++ {
			b := s[idx]
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			buf[idx] = b
		}
		// Check seen before allocating string
		candidate := buf[:len(s)]
		for _, prev := range *seen {
			if len(prev) == len(candidate) && stringEqualBytes(prev, candidate) {
				return
			}
		}
		lower = string(candidate)
	} else {
		lower = strings.ToLower(s)
		for _, prev := range *seen {
			if prev == lower {
				return
			}
		}
	}
	*seen = append(*seen, lower)
	if out.Len() > 0 {
		out.WriteByte(' ')
	}
	out.WriteString(lower)
}

func stringEqualBytes(s string, b []byte) bool {
	for i := 0; i < len(b); i++ {
		if s[i] != b[i] {
			return false
		}
	}
	return true
}

func appendUniqueTail(seen *[]string, out *strings.Builder, tail string) {
	for _, prev := range *seen {
		if prev == tail {
			return
		}
	}
	*seen = append(*seen, tail)
	if out.Len() > 0 {
		out.WriteByte(' ')
	}
	out.WriteString(tail)
}

func isASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isLowerOrDigitASCII(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func isWordByteOrRuneStart(s string, i int) bool {
	b := s[i]
	if b < 0x80 {
		return isASCIIAlnum(b)
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
