package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/filelock"
	"github.com/synt4xer/proxer/internal/indexer"
)

// reindexFileContent is overridden in tests to simulate post-write reindex failures.
var reindexFileContent = indexer.ReindexFileContentWithOptions

// editLockTimeout bounds how long an edit waits for another proxer writer to
// release the project lock before failing, so a stuck peer cannot hang an agent.
const editLockTimeout = 10 * time.Second

type EditOptions struct {
	IgnoredDirs    []string
	IndexSensitive bool
	OldContent     string
	Occurrence     int
	HasRange       bool
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

// PerformEditWithOptions replaces a line range (or an anchor block when
// opts.OldContent is set) in a file after verifying hash and DB writability,
// writes atomically, and reports any post-write reindex failure non-fatally.
func PerformEditWithOptions(ctx context.Context, db *sqlx.DB, root, path string, startLine, endLine int, newContent, expectedHash string, opts EditOptions) (domain.EditResultJSON, error) {
	if opts.OldContent != "" && opts.HasRange {
		return domain.EditResultJSON{}, fmt.Errorf("cannot combine --old anchor mode with --start/--end line range")
	}
	if opts.Occurrence < 0 {
		return domain.EditResultJSON{}, fmt.Errorf("occurrence must be >= 0, got %d", opts.Occurrence)
	}

	resolved, err := resolvePath(root, path)
	if err != nil {
		return domain.EditResultJSON{}, err
	}

	if _, err := os.Stat(resolved); os.IsNotExist(err) {
		return domain.EditResultJSON{}, fmt.Errorf("file '%s' not found", path)
	}

	// Serialize the whole read-hash-check-write cycle across processes. The
	// hash guard is only meaningful if no other writer can land between the
	// check and the rename, so the lock is taken before the file is read.
	lock, err := filelock.Acquire(root, editLockTimeout)
	if err != nil {
		return domain.EditResultJSON{}, fmt.Errorf("failed to acquire write lock: %w", err)
	}
	defer lock.Release()

	contentBytes, err := os.ReadFile(resolved)
	if err != nil {
		return domain.EditResultJSON{}, fmt.Errorf("failed to read file: %w", err)
	}

	if expectedHash != "" {
		if current := indexer.ContentHash(contentBytes); current != expectedHash {
			return domain.EditResultJSON{}, fmt.Errorf("file '%s' changed since it was read (current hash %s). Read it again before editing", path, current)
		}
	}

	if strings.Contains(newContent, config.TruncationMarker) {
		return domain.EditResultJSON{}, fmt.Errorf("refusing to edit '%s': replacement content contains truncation marker %q", path, config.TruncationMarker)
	}

	var finalBytes []byte
	var editStartLine, editEndLine int

	if opts.OldContent != "" {
		finalBytes, editStartLine, editEndLine, err = applyAnchorEdit(path, contentBytes, opts.OldContent, newContent, opts.Occurrence)
		if err != nil {
			return domain.EditResultJSON{}, err
		}
	} else {
		finalBytes, editStartLine, editEndLine, err = applyRangeEdit(path, contentBytes, startLine, endLine, newContent)
		if err != nil {
			return domain.EditResultJSON{}, err
		}
	}

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

	newHash := indexer.ContentHash(finalBytes)
	err = database.WithRetry(database.DefaultRetryConfig, func() error {
		return reindexFileContent(db, root, resolved, finalBytes, indexer.IndexOptions{
			IgnoredDirs:    opts.IgnoredDirs,
			IndexSensitive: opts.IndexSensitive,
		})
	})
	if err != nil {
		return domain.EditResultJSON{
			Status:        "success",
			Message:       fmt.Sprintf("Saved lines %d-%d in %s on disk, but reindexing failed; run proxer index or wait for the next search refresh to reconcile.", editStartLine, editEndLine, path),
			Hash:          newHash,
			ReindexFailed: true,
			ReindexError:  err.Error(),
		}, nil
	}

	return domain.EditResultJSON{
		Status:  "success",
		Message: fmt.Sprintf("Successfully updated %s (lines %d-%d)", path, editStartLine, editEndLine),
		Hash:    newHash,
	}, nil
}

func applyRangeEdit(path string, contentBytes []byte, startLine, endLine int, newContent string) ([]byte, int, int, error) {
	lines := strings.Split(string(contentBytes), "\n")
	totalLines := len(lines)

	if startLine < 1 || startLine > totalLines || endLine < startLine || endLine > totalLines {
		return nil, 0, 0, fmt.Errorf("invalid line range. The file has %d lines", totalLines)
	}

	for i := startLine; i <= endLine; i++ {
		line := strings.TrimSuffix(lines[i-1], "\r")
		if len(line) > config.MaxLineWidth && utf8.RuneCountInString(line) > config.MaxLineWidth {
			return nil, 0, 0, fmt.Errorf("refusing to edit '%s': line %d exceeds %d characters and is truncated by read", path, i, config.MaxLineWidth)
		}
	}

	prefix := lines[:startLine-1]
	suffix := lines[endLine:]
	cleanNewContent := strings.TrimRight(newContent, "\r\n")

	var finalLines []string
	finalLines = append(finalLines, prefix...)
	finalLines = append(finalLines, cleanNewContent)
	finalLines = append(finalLines, suffix...)

	return []byte(strings.Join(finalLines, "\n")), startLine, endLine, nil
}

type anchorMatch struct {
	start     int
	end       int
	startLine int
	endLine   int
}

func applyAnchorEdit(path string, contentBytes []byte, oldContent, newContent string, occurrence int) ([]byte, int, int, error) {
	hasCRLF := bytes.Contains(contentBytes, []byte("\r\n"))
	normContent := strings.ReplaceAll(string(contentBytes), "\r\n", "\n")
	normOld := strings.ReplaceAll(oldContent, "\r\n", "\n")
	normNew := strings.ReplaceAll(newContent, "\r\n", "\n")

	var matches []anchorMatch
	searchFrom := 0
	for {
		idx := strings.Index(normContent[searchFrom:], normOld)
		if idx < 0 {
			break
		}
		absStart := searchFrom + idx
		absEnd := absStart + len(normOld)
		startLine := 1 + strings.Count(normContent[:absStart], "\n")
		endLine := startLine + strings.Count(normOld, "\n")
		matches = append(matches, anchorMatch{
			start:     absStart,
			end:       absEnd,
			startLine: startLine,
			endLine:   endLine,
		})
		searchFrom = absEnd
	}

	if len(matches) == 0 {
		return nil, 0, 0, fmt.Errorf("old content not found in %s", path)
	}
	if len(matches) > 1 && occurrence == 0 {
		lineStrs := make([]string, len(matches))
		for i, m := range matches {
			lineStrs[i] = strconv.Itoa(m.startLine)
		}
		return nil, 0, 0, fmt.Errorf("old content matched %d locations in %s at lines [%s]; pass --occurrence or a more specific block", len(matches), path, strings.Join(lineStrs, ", "))
	}

	targetIdx := 0
	if occurrence > 0 {
		if occurrence > len(matches) {
			return nil, 0, 0, fmt.Errorf("occurrence %d out of range: old content matched %d location(s) in %s", occurrence, len(matches), path)
		}
		targetIdx = occurrence - 1
	}

	m := matches[targetIdx]
	lines := strings.Split(normContent, "\n")
	for i := m.startLine; i <= m.endLine; i++ {
		if i >= 1 && i <= len(lines) {
			if len(lines[i-1]) > config.MaxLineWidth && utf8.RuneCountInString(lines[i-1]) > config.MaxLineWidth {
				return nil, 0, 0, fmt.Errorf("refusing to edit '%s': line %d exceeds %d characters and is truncated by read", path, i, config.MaxLineWidth)
			}
		}
	}

	updated := normContent[:m.start] + normNew + normContent[m.end:]
	if hasCRLF {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	newEndLine := m.startLine + strings.Count(normNew, "\n")
	return []byte(updated), m.startLine, newEndLine, nil
}

// writeFileAtomically replaces path through a temporary file in the same
// directory. A plain write truncates first, so a second process that reads while
// the write is in flight sees a partial file and can splice garbage into it.
// The rename makes the new content appear in one step.
//
// Two processes editing the same file are serialized by the caller's lock, so
// the check-then-write cycle cannot interleave; this function only guarantees
// that no reader ever observes a half-written file.
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
