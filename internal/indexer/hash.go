package indexer

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContentHash returns the hex SHA-256 of a file's bytes. The manifest stores it
// so a delta build can tell "same timestamp, new bytes" from "same bytes, new
// timestamp", and read hands it to edit so an edit built on a stale read is
// refused instead of applied.
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
