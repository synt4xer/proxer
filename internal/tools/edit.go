package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/indexer"
)

// reindexFileContent is overridden in tests to simulate post-write reindex failures.
var reindexFileContent = indexer.ReindexFileContentWithOptions

type EditOptions struct {
	IgnoredDirs    []string
	IndexSensitive bool
}

// PerformEdit replaces a line range in a file and reindexes it. The path is
// resolved against root, matching PerformRead, so a harness that spawns proxer
// elsewhere still edits the file the agent is looking at. When expectedHash is
// non-empty it must match the file's current content hash; a mismatch means the
// file changed after it was read, and the edit is refused. ignoredDirs is the
// project's skip list, so editing a file inside an ignored directory does not
// add it back to the index.
func PerformEdit(db *sqlx.DB, root, path string, startLine, endLine int, newContent, expectedHash string, ignoredDirs []string) (domain.EditResultJSON, error) {
	return PerformEditWithOptions(context.Background(), db, root, path, startLine, endLine, newContent, expectedHash, EditOptions{
		IgnoredDirs: ignoredDirs,
	})
}

// PerformEditWithOptions replaces a line range in a file after verifying DB
// writability, writes atomically, and reports any post-write reindex failure
// non-fatally in EditResultJSON.
func PerformEditWithOptions(ctx context.Context, db *sqlx.DB, root, path string, startLine, endLine int, newContent, expectedHash string, opts EditOptions) (domain.EditResultJSON, error) {
	resolved, err := resolvePath(root, path)
	if err != nil {
		return domain.EditResultJSON{}, err
	}

	if _, err := os.Stat(resolved); os.IsNotExist(err) {
		return domain.EditResultJSON{}, fmt.Errorf("file '%s' not found", path)
	}

	contentBytes, err := os.ReadFile(resolved)
	if err != nil {
		return domain.EditResultJSON{}, fmt.Errorf("failed to read file: %w", err)
	}

	if expectedHash != "" {
		if current := indexer.ContentHash(contentBytes); current != expectedHash {
			return domain.EditResultJSON{}, fmt.Errorf("file '%s' changed since it was read. Read it again before editing", path)
		}
	}

	lines := strings.Split(string(contentBytes), "\n")
	totalLines := len(lines)

	if startLine < 1 || startLine > totalLines || endLine < startLine || endLine > totalLines {
		return domain.EditResultJSON{}, fmt.Errorf("invalid line range. The file has %d lines", totalLines)
	}

	if strings.Contains(newContent, config.TruncationMarker) {
		return domain.EditResultJSON{}, fmt.Errorf("refusing to edit '%s': replacement content contains truncation marker %q", path, config.TruncationMarker)
	}

	for i := startLine; i <= endLine; i++ {
		line := strings.TrimSuffix(lines[i-1], "\r")
		if len(line) > config.MaxLineWidth && utf8.RuneCountInString(line) > config.MaxLineWidth {
			return domain.EditResultJSON{}, fmt.Errorf("refusing to edit '%s': line %d exceeds %d characters and is truncated by read", path, i, config.MaxLineWidth)
		}
	}

	prefix := lines[:startLine-1]
	suffix := lines[endLine:]

	cleanNewContent := strings.TrimRight(newContent, "\r\n")

	var finalLines []string
	finalLines = append(finalLines, prefix...)
	finalLines = append(finalLines, cleanNewContent)
	finalLines = append(finalLines, suffix...)

	finalBytes := []byte(strings.Join(finalLines, "\n"))

	if err := database.CheckWritable(ctx, db); err != nil {
		if database.IsReadOnlyError(err) {
			return domain.EditResultJSON{}, fmt.Errorf("refusing to edit '%s': index database is read-only or not writable (%w); set PROXER_DB=<writable-path> to use a writable database", path, err)
		}
		if database.IsBusyError(err) {
			return domain.EditResultJSON{}, fmt.Errorf("refusing to edit '%s': index database is locked by another writer (%w); retry shortly", path, err)
		}
		return domain.EditResultJSON{}, fmt.Errorf("refusing to edit '%s': index database is not writable: %w", path, err)
	}

	if err := writeFileAtomically(resolved, finalBytes); err != nil {
		return domain.EditResultJSON{}, fmt.Errorf("failed to save file: %w", err)
	}

	err = reindexFileContent(db, root, resolved, finalBytes, indexer.IndexOptions{
		IgnoredDirs:    opts.IgnoredDirs,
		IndexSensitive: opts.IndexSensitive,
	})
	if err != nil {
		return domain.EditResultJSON{
			Status:        "success",
			Message:       fmt.Sprintf("Saved lines %d-%d in %s on disk, but reindexing failed; run proxer index or wait for the next search refresh to reconcile.", startLine, endLine, path),
			ReindexFailed: true,
			ReindexError:  err.Error(),
		}, nil
	}

	return domain.EditResultJSON{
		Status:  "success",
		Message: fmt.Sprintf("Successfully replaced lines %d-%d in %s and reindexed it.", startLine, endLine, path),
	}, nil
}

// writeFileAtomically replaces path through a temporary file in the same
// directory. A plain write truncates first, so a second process that reads while
// the write is in flight sees a partial file and can splice garbage into it.
// The rename makes the new content appear in one step.
//
// Two processes editing the same file still race on the content they read: the
// last writer wins. Serializing that would need a lock, which is out of scope
// here; preventing the corruption is not.
func writeFileAtomically(path string, content []byte) error {
	// A symlink is edited through its target, so the link itself must survive
	// the replacement.
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".proxer-edit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return err
	}

	return os.Rename(tmpName, target)
}
