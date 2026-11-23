package indexer_test

import (
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/indexer"
)

func mustIndex(t *testing.T, db *sqlx.DB, rootPath string, ignoredDirs []string) indexer.Refreshed {
	t.Helper()

	refreshed, err := indexer.IndexProject(db, rootPath, ignoredDirs)
	require.NoError(t, err)
	return refreshed
}
