package tools

import (
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/domain"
)

type StatusOptions struct {
	DBPath         string
	IndexSensitive bool
	ReadOnly       bool
	Vacuum         bool
	Prune          bool
	InstallMode    string
	SkillDir       string
}

func PerformStatus(db *sqlx.DB, root string, opts StatusOptions) (domain.StatusResponse, error) {
	var warning string
	if opts.Vacuum {
		if opts.ReadOnly {
			return domain.StatusResponse{}, fmt.Errorf("cannot vacuum read-only index database %q", opts.DBPath)
		}
		maint, err := database.Maintain(db, opts.DBPath, database.MaintainOptions{
			ForceVacuum: true,
		})
		if err != nil {
			return domain.StatusResponse{}, fmt.Errorf("vacuum failed: %w", err)
		}
		warning = maint.Warning
	}

	meta, err := database.ReadStatusMeta(db)
	if err != nil {
		return domain.StatusResponse{}, fmt.Errorf("failed to read index status: %w", err)
	}

	var dbSize int64
	dbMode := "unknown"
	if opts.DBPath != "" {
		if info, statErr := os.Stat(opts.DBPath); statErr == nil {
			dbSize = info.Size()
			dbMode = fmt.Sprintf("%04o", info.Mode().Perm())
		}
	}

	var skipped *domain.SkippedCounts
	if meta.LastSkippedJSON != "" && meta.LastSkippedJSON != "{}" {
		var parsed domain.SkippedCounts
		if jerr := json.Unmarshal([]byte(meta.LastSkippedJSON), &parsed); jerr == nil {
			skipped = &parsed
		}
	}

	orphans := scanOrphanDatabases(opts.SkillDir, opts.Prune)

	return domain.StatusResponse{
		Root:            root,
		DBPath:          opts.DBPath,
		DBSizeBytes:     dbSize,
		DBMode:          dbMode,
		SchemaVersion:   meta.SchemaVersion,
		IndexedAt:       meta.IndexedAt,
		FilesIndexed:    meta.FilesIndexed,
		ChunksIndexed:   meta.ChunksIndexed,
		IndexSensitive:  opts.IndexSensitive,
		PurgePending:    meta.PurgePending,
		IgnoreSources:   detectIgnoreSources(root),
		IgnoredDirs:     meta.LastIgnoredDirs,
		SkippedByReason: skipped,
		InstallMode:     opts.InstallMode,
		OrphanDatabases: orphans,
		Warning:         warning,
	}, nil
}

func scanOrphanDatabases(skillDir string, prune bool) []domain.OrphanDatabase {
	if skillDir == "" {
		if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
			candidate := filepath.Join(homeDir, ".agents", "skills", "proxer")
			if info, serr := os.Stat(filepath.Join(candidate, "database")); serr == nil && info.IsDir() {
				skillDir = candidate
			}
		}
	}
	if skillDir == "" {
		return nil
	}

	matches, err := filepath.Glob(filepath.Join(skillDir, "database", "*.db"))
	if err != nil || len(matches) == 0 {
		return nil
	}

	var orphans []domain.OrphanDatabase
	for _, dbFile := range matches {
		recordedRoot, err := database.ReadRecordedRoot(dbFile)
		if err != nil || recordedRoot == "" {
			continue
		}
		if _, statErr := os.Stat(recordedRoot); !os.IsNotExist(statErr) {
			continue
		}
		var size int64
		if info, infoErr := os.Stat(dbFile); infoErr == nil {
			size = info.Size()
		}
		entry := domain.OrphanDatabase{
			DBPath:       dbFile,
			RecordedRoot: recordedRoot,
			SizeBytes:    size,
		}
		if prune {
			if rmErr := os.Remove(dbFile); rmErr == nil {
				_ = os.Remove(dbFile + "-wal")
				_ = os.Remove(dbFile + "-shm")
				entry.Pruned = true
			}
		}
		orphans = append(orphans, entry)
	}
	return orphans
}

func detectIgnoreSources(root string) []string {
	sources := []string{"defaults"}
	if fileExists(filepath.Join(root, ".gitignore")) || fileExists(filepath.Join(root, ".git", "info", "exclude")) {
		sources = append(sources, ".gitignore")
	}
	if fileExists(filepath.Join(root, ".proxer.json")) {
		sources = append(sources, ".proxer.json")
	}
	return sources
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
