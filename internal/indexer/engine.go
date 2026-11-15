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

const insertChunkSQL = `
	INSERT INTO chunk_search (id, filepath, start_line, end_line, content)
	VALUES (?, ?, ?, ?, ?)
`

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

// IndexProject rebuilds the entire index for rootPath. Chunks are keyed by
// their path inside the project, so indexing an absolute root and indexing a
// relative one produce the same database.
func IndexProject(db *sqlx.DB, rootPath string) error {
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM chunk_search"); err != nil {
		return fmt.Errorf("failed to clear the index: %w", err)
	}

	stmt, err := tx.Preparex(insertChunkSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()

	walkErr := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		if d.IsDir() {
			// The root itself is never skipped: an ignored directory name such
			// as "build" is legitimate when it is the project being indexed.
			if path != rootPath && !filesystem.IsIndexableDir(path, info) {
				return filepath.SkipDir
			}
			return nil
		}

		if !filesystem.IsIndexableFile(info) {
			return nil
		}

		// A file that disappears or cannot be read mid-walk is skipped rather
		// than aborting the whole rebuild.
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		rel, err := relativePath(rootPath, path)
		if err != nil {
			return err
		}

		return insertChunks(stmt, rel, string(content))
	})
	if walkErr != nil {
		return walkErr
	}

	return tx.Commit()
}

// insertChunks splits content into overlapping windows and stores each window
// under rel. Callers produce rel once so every path in the index has the same
// shape regardless of how the root was supplied.
func insertChunks(stmt *sqlx.Stmt, rel, content string) error {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	step := config.ChunkSize - config.OverlapSize

	for i := 0; i < totalLines; i += step {
		startLine := i + 1
		endLine := i + config.ChunkSize
		if endLine > totalLines {
			endLine = totalLines
		}

		chunkContent := strings.Join(lines[startLine-1:endLine], "\n")
		id := fmt.Sprintf("%s:%d-%d", rel, startLine, endLine)

		if _, err := stmt.Exec(id, rel, startLine, endLine, chunkContent); err != nil {
			return fmt.Errorf("failed to store chunk %s: %w", id, err)
		}

		if endLine >= totalLines {
			break
		}
	}

	return nil
}

// relativePath keys a file by its location inside the project. Absolute keys
// would break as soon as the project moves, and mixed absolute and relative
// keys would prevent single-file reindexing from finding its own rows.
func relativePath(rootPath, path string) (string, error) {
	if rootPath == "" {
		rootPath = "."
	}

	rel, err := filepath.Rel(rootPath, path)
	if err != nil {
		return "", fmt.Errorf("failed to relativize %s against %s: %w", path, rootPath, err)
	}
	return filepath.ToSlash(rel), nil
}
