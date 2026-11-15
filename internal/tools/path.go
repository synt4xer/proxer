package tools

import "path/filepath"

// resolvePath interprets an agent-supplied path against the project root. A
// harness can spawn proxer from any directory, so the root — not the working
// directory — defines what a relative path means.
func resolvePath(root, path string) string {
	if filepath.IsAbs(path) || root == "" {
		return path
	}
	return filepath.Join(root, path)
}
