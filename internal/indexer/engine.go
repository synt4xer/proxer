package indexer

import (
	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/internal/config"

	_ "modernc.org/sqlite"
)

func InitDB() (*sqlx.DB, error) {
	db, err := sqlx.Connect("sqlite", config.DBFile)
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
