package database

import (
	"fmt"

	"github.com/jmoiron/sqlx"
)

// FileRecord is one manifest row: the cheap gate (size and modification time)
// that decides whether a file has to be read again, and the content hash that
// decides whether a file whose stat moved has to be re-chunked.
type FileRecord struct {
	Path    string
	Size    int64
	MTimeNS int64
	Hash    string
}

// Files reads the whole manifest into memory. Every delta build needs it, and
// one row per file is small next to the chunks it describes.
func Files(db *sqlx.DB) (map[string]FileRecord, error) {
	type manifestRow struct {
		Filepath string `db:"filepath"`
		Size     int64  `db:"size"`
		MtimeNS  int64  `db:"mtime_ns"`
		Hash     string `db:"hash"`
	}

	var rows []manifestRow
	if err := db.Select(&rows, "SELECT filepath, size, mtime_ns, hash FROM indexed_files"); err != nil {
		return nil, fmt.Errorf("failed to read the index manifest: %w", err)
	}

	manifest := make(map[string]FileRecord, len(rows))
	for _, row := range rows {
		manifest[row.Filepath] = FileRecord{
			Path:    row.Filepath,
			Size:    row.Size,
			MTimeNS: row.MtimeNS,
			Hash:    row.Hash,
		}
	}
	return manifest, nil
}

// Remove drops a file from both the chunks and the manifest. A path the caller
// stopped seeing must not survive in either store, or the next delta would treat
// the leftover chunks as current.
func Remove(tx *sqlx.Tx, rel string) error {
	if err := DeleteChunks(tx, rel); err != nil {
		return err
	}

	if _, err := tx.Exec("DELETE FROM indexed_files WHERE filepath = ?", rel); err != nil {
		return fmt.Errorf("failed to drop the manifest row for %s: %w", rel, err)
	}

	return nil
}
