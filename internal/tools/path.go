package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolvePath interprets an agent-supplied path against the project root and
// refuses anything that lands outside it.
//
// The index only describes files under the root, so a path that escapes it can
// never be kept consistent: no search entry exists for it, and reindexing it
// stores a "../" key that no later walk of the root will ever clean up. Refusing
// the path is the only honest answer; the harness already offers read and bash
// for files that belong to another project.
func resolvePath(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("a file path is required")
	}

	resolved := path
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(root, resolved)
	}
	resolved = filepath.Clean(resolved)

	if !isWithin(root, resolved) {
		return "", outsideRootError(root, path)
	}

	return resolved, nil
}

// isWithin reports whether path stays inside root. It compares the literal
// paths first, then the resolved symlinks, because a link created inside the
// root can still point somewhere else. A path that does not exist yet cannot
// escape through a symlink, so the literal answer stands.
func isWithin(root, path string) bool {
	if !hasPrefixPath(filepath.Clean(root), path) {
		return false
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return true
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return true
	}

	return hasPrefixPath(realRoot, realPath)
}

// hasPrefixPath reports whether path is root itself or sits underneath it.
// A plain string prefix is not enough: "/project-other" would pass "/project".
func hasPrefixPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func outsideRootError(root, path string) error {
	return fmt.Errorf("path '%s' is outside the project root %s. proxer only serves files under that root", path, root)
}
