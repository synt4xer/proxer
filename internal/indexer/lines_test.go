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
