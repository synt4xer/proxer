package tools

import (
	"crypto/sha256"
	"encoding/hex"
)

// contentHash returns the hex SHA-256 of a file's bytes. PerformRead hands this
// token back to the agent, and PerformEdit compares it before writing, so an
// edit built on a stale read is refused instead of silently applied to a file
// that has moved on.
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
