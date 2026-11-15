package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/internal/indexer"
)

// PerformEdit replaces a line range in a file and reindexes it. The path is
// resolved against root, matching PerformRead, so a harness that spawns proxer
// elsewhere still edits the file the agent is looking at. When expectedHash is
// non-empty it must match the file's current content hash; a mismatch means the
// file changed after it was read, and the edit is refused. ignoredDirs is the
// project's skip list, so editing a file inside an ignored directory does not
// add it back to the index.
func PerformEdit(db *sqlx.DB, root, path string, startLine, endLine int, newContent, expectedHash string, ignoredDirs []string) (string, error) {
	resolved, err := resolvePath(root, path)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(resolved); os.IsNotExist(err) {
		return "", fmt.Errorf("file '%s' not found", path)
	}

	contentBytes, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	if expectedHash != "" {
		if current := contentHash(contentBytes); current != expectedHash {
			return "", fmt.Errorf("file '%s' changed since it was read. Read it again before editing", path)
		}
	}

	lines := strings.Split(string(contentBytes), "\n")
	totalLines := len(lines)

	if startLine < 1 || startLine > totalLines || endLine < startLine || endLine > totalLines {
		return "", fmt.Errorf("invalid line range. The file has %d lines", totalLines)
	}

	prefix := lines[:startLine-1]
	suffix := lines[endLine:]

	cleanNewContent := strings.TrimRight(newContent, "\r\n")

	var finalLines []string
	finalLines = append(finalLines, prefix...)
	finalLines = append(finalLines, cleanNewContent)
	finalLines = append(finalLines, suffix...)

	finalContent := strings.Join(finalLines, "\n")

	if err := writeFileAtomically(resolved, []byte(finalContent)); err != nil {
		return "", fmt.Errorf("failed to save file: %w", err)
	}

	err = indexer.ReindexFile(db, root, resolved, ignoredDirs)
	if err != nil {
		return "", fmt.Errorf("file saved, but reindexing it failed: %w", err)
	}

	successMsg := map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Successfully replaced lines %d-%d in %s and reindexed it.", startLine, endLine, path),
	}
	out, err := encodeJSON(successMsg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return out, nil
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
