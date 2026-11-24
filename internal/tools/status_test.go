package tools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
	"github.com/synt4xer/proxer/internal/tools"
)

func TestPerformStatusReportsMetadataAndIgnoreSources(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.tmp\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".proxer.json"), []byte(`{"ignored_dirs":["gen"]}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "gen"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "gen", "skip.go"), []byte("package gen\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "min.js"), []byte(strings.Repeat("a", config.MaxLineWidth+10)+"\n"), 0o644))

	dbPath := filepath.Join(root, config.DBFile)
	db, err := database.Open(dbPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = indexer.IndexProject(db, root, append(append([]string{}, config.DefaultIgnoredDirs...), "gen"))
	require.NoError(t, err)

	res, err := tools.PerformStatus(db, root, tools.StatusOptions{
		DBPath:      dbPath,
		InstallMode: string(config.InstallModeProject),
	})
	require.NoError(t, err)
	assert.Equal(t, root, res.Root)
	assert.Equal(t, dbPath, res.DBPath)
	assert.Greater(t, res.DBSizeBytes, int64(0))
	assert.Equal(t, "0600", res.DBMode)
	assert.Equal(t, database.SchemaVersion, res.SchemaVersion)
	assert.NotEmpty(t, res.IndexedAt)
	assert.Equal(t, 3, res.FilesIndexed)
	assert.Equal(t, 2, res.ChunksIndexed)
	assert.Equal(t, []string{"defaults", ".gitignore", ".proxer.json"}, res.IgnoreSources)
	assert.Equal(t, 1, res.IgnoredDirs)
	require.NotNil(t, res.SkippedByReason)
	assert.Equal(t, 1, res.SkippedByReason.Minified)
	assert.Equal(t, "project", res.InstallMode)
}

func TestPerformStatusVacuumAndReadOnlyRejection(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))

	dbPath := filepath.Join(root, config.DBFile)
	db, err := database.Open(dbPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = indexer.IndexProject(db, root, config.DefaultIgnoredDirs)
	require.NoError(t, err)

	_, err = tools.PerformStatus(db, root, tools.StatusOptions{
		DBPath:   dbPath,
		ReadOnly: true,
		Vacuum:   true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot vacuum read-only")

	res, err := tools.PerformStatus(db, root, tools.StatusOptions{
		DBPath: dbPath,
		Vacuum: true,
	})
	require.NoError(t, err)
	assert.Empty(t, res.Warning)
	assert.Equal(t, 1, res.FilesIndexed)
}

func TestPerformStatusDetectsAndPrunesOrphanDatabases(t *testing.T) {
	skillDir := t.TempDir()
	dbDir := filepath.Join(skillDir, "database")
	require.NoError(t, os.MkdirAll(dbDir, 0o700))

	liveRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(liveRoot, "live.go"), []byte("package live\n"), 0o644))
	liveDBPath := filepath.Join(dbDir, "live-111111111111.db")
	liveDB, err := database.Open(liveDBPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	_, err = indexer.IndexProject(liveDB, liveRoot, config.DefaultIgnoredDirs)
	require.NoError(t, err)
	require.NoError(t, liveDB.Close())

	goneRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(goneRoot, "gone.go"), []byte("package gone\n"), 0o644))
	orphanDBPath := filepath.Join(dbDir, "gone-222222222222.db")
	orphanDB, err := database.Open(orphanDBPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	_, err = indexer.IndexProject(orphanDB, goneRoot, config.DefaultIgnoredDirs)
	require.NoError(t, err)
	require.NoError(t, orphanDB.Close())
	require.NoError(t, os.RemoveAll(goneRoot))

	// Create sidecar files to ensure prune cleans them up too.
	require.NoError(t, os.WriteFile(orphanDBPath+"-wal", []byte("wal"), 0o600))
	require.NoError(t, os.WriteFile(orphanDBPath+"-shm", []byte("shm"), 0o600))

	// Corrupt/non-SQLite .db file should be skipped gracefully.
	require.NoError(t, os.WriteFile(filepath.Join(dbDir, "corrupt.db"), []byte("not sqlite"), 0o600))

	activeDB, err := database.Open(liveDBPath, database.OpenOptions{Chunking: indexer.Chunking()})
	require.NoError(t, err)
	t.Cleanup(func() { activeDB.Close() })

	// First status without Prune reports orphan without deleting it.
	res, err := tools.PerformStatus(activeDB, liveRoot, tools.StatusOptions{
		DBPath:      liveDBPath,
		InstallMode: string(config.InstallModeHarness),
		SkillDir:    skillDir,
	})
	require.NoError(t, err)
	require.Len(t, res.OrphanDatabases, 1)
	assert.Equal(t, orphanDBPath, res.OrphanDatabases[0].DBPath)
	assert.Equal(t, goneRoot, res.OrphanDatabases[0].RecordedRoot)
	assert.False(t, res.OrphanDatabases[0].Pruned)
	_, err = os.Stat(orphanDBPath)
	require.NoError(t, err)

	// Second status with Prune deletes the orphan DB and its sidecars.
	res, err = tools.PerformStatus(activeDB, liveRoot, tools.StatusOptions{
		DBPath:      liveDBPath,
		InstallMode: string(config.InstallModeHarness),
		SkillDir:    skillDir,
		Prune:       true,
	})
	require.NoError(t, err)
	require.Len(t, res.OrphanDatabases, 1)
	assert.True(t, res.OrphanDatabases[0].Pruned)

	_, err = os.Stat(orphanDBPath)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(orphanDBPath + "-wal")
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(orphanDBPath + "-shm")
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(liveDBPath)
	assert.NoError(t, err, "live DB must not be pruned")
}
