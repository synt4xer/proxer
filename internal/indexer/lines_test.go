package indexer_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
)

func TestTruncateLine(t *testing.T) {
	t.Run("returns line unchanged when within limit", func(t *testing.T) {
		assert.Equal(t, "short line", indexer.TruncateLine("short line", 20))
		assert.Equal(t, "exact length", indexer.TruncateLine("exact length", 12))
		assert.Equal(t, "", indexer.TruncateLine("", 10))
	})

	t.Run("returns line unchanged when limit is non-positive", func(t *testing.T) {
		assert.Equal(t, "any line", indexer.TruncateLine("any line", 0))
		assert.Equal(t, "any line", indexer.TruncateLine("any line", -5))
	})

	t.Run("truncates line exceeding limit and appends marker", func(t *testing.T) {
		line := "1234567890"
		truncated := indexer.TruncateLine(line, 5)
		assert.Equal(t, "12345"+config.TruncationMarker, truncated)
	})

	t.Run("handles multi-byte utf8 runes correctly", func(t *testing.T) {
		line := "日本語テストです" // 8 runes
		assert.Equal(t, line, indexer.TruncateLine(line, 10))
		assert.Equal(t, line, indexer.TruncateLine(line, 8))
		assert.Equal(t, "日本語テス"+config.TruncationMarker, indexer.TruncateLine(line, 5))
	})
}

func TestHasLongLine(t *testing.T) {
	t.Run("returns false when all lines fit", func(t *testing.T) {
		content := []byte("line one\nline two\nline three\n")
		assert.False(t, indexer.HasLongLine(content, 20))
	})

	t.Run("returns false for non-positive limit", func(t *testing.T) {
		content := []byte("any content\n")
		assert.False(t, indexer.HasLongLine(content, 0))
		assert.False(t, indexer.HasLongLine(content, -1))
	})

	t.Run("returns true when a line exceeds limit", func(t *testing.T) {
		content := []byte("short\n" + strings.Repeat("x", 50) + "\nshort again\n")
		assert.True(t, indexer.HasLongLine(content, 20))
	})

	t.Run("detects long line without trailing newline", func(t *testing.T) {
		content := []byte(strings.Repeat("a", 100))
		assert.True(t, indexer.HasLongLine(content, 50))
		assert.False(t, indexer.HasLongLine(content, 150))
	})

	t.Run("handles CRLF endings without counting CR", func(t *testing.T) {
		content := []byte("12345\r\n67890\r\n")
		assert.False(t, indexer.HasLongLine(content, 5))
		assert.True(t, indexer.HasLongLine(content, 4))
	})

	t.Run("handles multi-byte utf8 runes correctly", func(t *testing.T) {
		// "日本語" is 3 runes, 9 bytes
		content := []byte("日本語\n")
		assert.False(t, indexer.HasLongLine(content, 3))
		assert.True(t, indexer.HasLongLine(content, 2))
	})
}

func TestPrepareContentForIndex(t *testing.T) {
	t.Run("returns content unchanged when all lines fit", func(t *testing.T) {
		content := []byte("package main\n\nfunc Hello() {}\n")
		out, ok := indexer.PrepareContentForIndex(content, 20)
		assert.True(t, ok)
		assert.Equal(t, string(content), out)
	})

	t.Run("skips single-line minified file", func(t *testing.T) {
		content := []byte("function min(){" + strings.Repeat("a", 50) + "}\n")
		out, ok := indexer.PrepareContentForIndex(content, 20)
		assert.False(t, ok)
		assert.Empty(t, out)
	})

	t.Run("skips minified file with short license header and trailing blank lines", func(t *testing.T) {
		content := []byte("/* MIT License */\n" + strings.Repeat("a", 50) + "\n\n\n")
		out, ok := indexer.PrepareContentForIndex(content, 20)
		assert.False(t, ok)
		assert.Empty(t, out)
	})

	t.Run("indexes multi-line source file and truncates only the embedded long line", func(t *testing.T) {
		longLiteral := "const svg = \"" + strings.Repeat("x", 60) + "\""
		content := []byte("package main\n\n" + longLiteral + "\n\nfunc ImportantBusinessLogic() {}\n")
		out, ok := indexer.PrepareContentForIndex(content, 40)
		assert.True(t, ok)
		expected := "package main\n\n" + longLiteral[:40] + config.TruncationMarker + "\n\nfunc ImportantBusinessLogic() {}\n"
		assert.Equal(t, expected, out)
	})

	t.Run("preserves CRLF endings when truncating embedded long lines", func(t *testing.T) {
		content := []byte("line1\r\n" + strings.Repeat("z", 30) + "\r\nline3\r\n")
		out, ok := indexer.PrepareContentForIndex(content, 10)
		assert.True(t, ok)
		expected := "line1\r\n" + strings.Repeat("z", 10) + config.TruncationMarker + "\r\nline3\r\n"
		assert.Equal(t, expected, out)
	})
}
