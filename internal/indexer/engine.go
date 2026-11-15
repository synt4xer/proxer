package indexer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/filesystem"

	_ "modernc.org/sqlite"
)

// InitDB opens the index database at dbPath and creates the FTS5 table when it
// is missing. The path is a parameter because the index belongs to a project
// root, not to whatever directory the process happens to run in.
func InitDB(dbPath string) (*sqlx.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create the index directory: %w", err)
		}
	}

	db, err := sqlx.Connect("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, p := range pragmas {
		db.MustExec(p)
	}

	query := `
		CREATE VIRTUAL TABLE IF NOT EXISTS chunk_search USING fts5(
			id UNINDEXED,
			filepath,
			start_line UNINDEXED,
			end_line UNINDEXED,
			content
		);
	`
	db.MustExec(query)
	return db, nil
}

func IndexProject(db *sqlx.DB, rootPath string) error {
	db.MustExec("DELETE FROM chunk_search")

	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	insertQuery := `INSERT INTO chunk_search (id, filepath, start_line, end_line, content) VALUES ($1, $2, $3, $4, $5)`
	stmt, err := tx.Preparex(insertQuery)
	if err != nil {
		return err
	}
	defer stmt.Close()

	filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if !filesystem.IsIndexableDir(path, info) {
				return filepath.SkipDir
			}
			return nil
		}

		if !filesystem.IsIndexableFile(info) {
			return nil
		}

		contentBytes, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		lines := strings.Split(string(contentBytes), "\n")
		totalLines := len(lines)
		step := config.ChunkSize - config.OverlapSize

		for i := 0; i < totalLines; i += step {
			startLine := i + 1
			endLine := i + config.ChunkSize
			if endLine > totalLines {
				endLine = totalLines
			}

			chunkContent := strings.Join(lines[startLine-1:endLine], "\n")
			id := fmt.Sprintf("%s:%d-%d", path, startLine, endLine)
			stmt.MustExec(id, path, startLine, endLine, chunkContent)

			if endLine >= totalLines {
				break
			}
		}

		return nil
	})

	return tx.Commit()
}
