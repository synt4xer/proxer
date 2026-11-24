package indexer_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/indexer"
)

func indexedFilepaths(t *testing.T, db interface {
	Select(dest any, query string, args ...any) error
}) []string {
	t.Helper()
	var paths []string
	require.NoError(t, db.Select(&paths, "SELECT filepath FROM indexed_files ORDER BY filepath"))
	return paths
}

func TestEcosystemDefaultsWithoutGitignore(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)

	// Unambiguous tool/cache directories across ecosystems (must be pruned by default).
	write(t, filepath.Join(root, ".venv", "lib", "site.py"), "def venv_code(): pass\n")
	write(t, filepath.Join(root, "__pycache__", "mod.py"), "def cached(): pass\n")
	write(t, filepath.Join(root, "myapp.egg-info", "info.txt"), "egg info\n")
	write(t, filepath.Join(root, ".gradle", "daemon.gradle"), "tasks {}\n")
	write(t, filepath.Join(root, "Pods", "AFNetworking", "net.swift"), "struct Net {}\n")
	write(t, filepath.Join(root, "DerivedData", "cache.swift"), "struct Cache {}\n")
	write(t, filepath.Join(root, ".dart_tool", "package_config.json"), "{}\n")
	write(t, filepath.Join(root, "_build", "dev", "app.ex"), "defmodule Dev do end\n")
	write(t, filepath.Join(root, ".terraform", "providers.tf"), "resource \"a\" \"b\" {}\n")

	// Generic directory names intentionally excluded from DefaultIgnoredDirs
	// (must NOT be silently hidden when no .gitignore or .proxer.json excludes them).
	write(t, filepath.Join(root, "build", "script.sh"), "echo build\n")
	write(t, filepath.Join(root, "target", "generated.kt"), "fun gen() {}\n")
	write(t, filepath.Join(root, "deps", "helper.ex"), "defmodule Helper do end\n")
	write(t, filepath.Join(root, "dist", "entry.mjs"), "export const x = 1\n")
	write(t, filepath.Join(root, "out", "notes.rst"), "Heading\n=======\n")
	write(t, filepath.Join(root, ".github", "workflows", "ci.yml"), "name: CI\n")

	// Regular multi-language source files.
	write(t, filepath.Join(root, "app.py"), "def run(): pass\n")
	write(t, filepath.Join(root, "Main.swift"), "func main() {}\n")
	write(t, filepath.Join(root, "main.dart"), "void main() {}\n")

	refreshed := mustIndex(t, db, root, config.DefaultIgnoredDirs)
	assert.Equal(t, 9, refreshed.Added)
	assert.Equal(t, 9, refreshed.IgnoredDirs, "each pruned ecosystem cache root must count once in ignored_dirs")
	assert.Equal(t, 0, refreshed.Skipped, "pruned directory roots must never be mixed into skipped files")
	assert.Nil(t, refreshed.SkippedByReason)

	got := indexedFilepaths(t, db)
	assert.Equal(t, []string{
		".github/workflows/ci.yml",
		"Main.swift",
		"app.py",
		"build/script.sh",
		"deps/helper.ex",
		"dist/entry.mjs",
		"main.dart",
		"out/notes.rst",
		"target/generated.kt",
	}, got)
}

func TestGitignoreAndProxerConfigPrecedence(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)

	// Root .gitignore ignores generic build directories and *.tmp files.
	write(t, filepath.Join(root, ".gitignore"), "build/\ntarget/\ndeps/\ndist/\n*.tmp.py\n")
	// Nested .gitignore ignores *.py except !keep.py.
	write(t, filepath.Join(root, "pkg", ".gitignore"), "*.py\n!keep.py\n")
	// .git/info/exclude ignores local.go.
	write(t, filepath.Join(root, ".git", "info", "exclude"), "local.go\n")
	// .proxer.json explicitly ignores custom_out.
	write(t, filepath.Join(root, config.ConfigFile), `{"ignored_dirs":["custom_out"]}`)

	write(t, filepath.Join(root, "build", "a.go"), "package build\n")
	write(t, filepath.Join(root, "target", "b.kt"), "fun b() {}\n")
	write(t, filepath.Join(root, "deps", "c.ex"), "defmodule C do end\n")
	write(t, filepath.Join(root, "dist", "d.mjs"), "export const d = 1\n")
	write(t, filepath.Join(root, "custom_out", "e.go"), "package custom\n")
	write(t, filepath.Join(root, "local.go"), "package local\n")
	write(t, filepath.Join(root, "scratch.tmp.py"), "x = 1\n")
	write(t, filepath.Join(root, "pkg", "drop.py"), "x = 2\n")
	write(t, filepath.Join(root, "pkg", "keep.py"), "def keep(): return 1\n")
	write(t, filepath.Join(root, "main.go"), "package main\n")

	cfg, err := config.LoadProjectConfig(root)
	require.NoError(t, err)

	refreshed := mustIndex(t, db, root, cfg.IgnoredDirs)
	// Indexed: .proxer.json, main.go, pkg/keep.py
	assert.Equal(t, 3, refreshed.Added)
	// Pruned dirs: .git, build, custom_out, deps, dist, target = 6
	assert.Equal(t, 6, refreshed.IgnoredDirs)
	// Skipped files:
	// - ignored_files: local.go, scratch.tmp.py, pkg/drop.py = 3
	// - extension: .gitignore, pkg/.gitignore = 2
	require.NotNil(t, refreshed.SkippedByReason)
	assert.Equal(t, 3, refreshed.SkippedByReason.IgnoredFiles)
	assert.Equal(t, 2, refreshed.SkippedByReason.Extension)
	assert.Equal(t, 5, refreshed.Skipped)

	got := indexedFilepaths(t, db)
	assert.Equal(t, []string{".proxer.json", "main.go", "pkg/keep.py"}, got)

	// Single-file ReindexFile must honor .gitignore and never smuggle ignored files into SQLite.
	require.NoError(t, indexer.ReindexFile(db, root, filepath.Join(root, "pkg", "drop.py"), cfg.IgnoredDirs))
	require.NoError(t, indexer.ReindexFile(db, root, filepath.Join(root, "build", "a.go"), cfg.IgnoredDirs))
	assert.Equal(t, []string{".proxer.json", "main.go", "pkg/keep.py"}, indexedFilepaths(t, db))
}

func TestSkippedBreakdownUnitInvariant(t *testing.T) {
	root := t.TempDir()
	db := openWorkspaceDB(t, root)

	// Pruned dir roots (must count in ignored_dirs, never in skipped).
	write(t, filepath.Join(root, ".venv", "a.py"), "x = 1\n")
	write(t, filepath.Join(root, "node_modules", "b.js"), "const b = 1\n")

	// Ignored file via .gitignore.
	write(t, filepath.Join(root, ".gitignore"), "ignored_file.go\n")
	write(t, filepath.Join(root, "ignored_file.go"), "package main\n")

	// Sensitive files (default index_sensitive: false).
	write(t, filepath.Join(root, ".env"), "API_KEY=secret\n")
	write(t, filepath.Join(root, "id_rsa"), "PRIVATE KEY\n")

	// Unsupported extension (plus .gitignore itself).
	write(t, filepath.Join(root, "logo.png"), "pngbytes")

	// Oversized file (> MaxFileSize).
	oversized := strings.Repeat("a\n", (config.MaxFileSize/2)+10)
	write(t, filepath.Join(root, "huge.go"), oversized)

	// Minified / long-line file.
	write(t, filepath.Join(root, "bundle.min.js"), strings.Repeat("x", config.MaxLineWidth+100)+"\n")

	// Valid indexable file.
	write(t, filepath.Join(root, "main.go"), "package main\nfunc Main() {}\n")

	refreshed := mustIndex(t, db, root, config.DefaultIgnoredDirs)
	require.NotNil(t, refreshed.SkippedByReason)

	assert.Equal(t, 1, refreshed.Added)
	assert.Equal(t, 2, refreshed.IgnoredDirs)
	assert.Equal(t, 1, refreshed.SkippedByReason.IgnoredFiles)
	assert.Equal(t, 2, refreshed.SkippedByReason.Sensitive)
	assert.Equal(t, 2, refreshed.SkippedByReason.Extension) // .gitignore + logo.png
	assert.Equal(t, 1, refreshed.SkippedByReason.Size)
	assert.Equal(t, 1, refreshed.SkippedByReason.Minified)

	sum := refreshed.SkippedByReason.IgnoredFiles +
		refreshed.SkippedByReason.Extension +
		refreshed.SkippedByReason.Sensitive +
		refreshed.SkippedByReason.Minified +
		refreshed.SkippedByReason.Size
	assert.Equal(t, sum, refreshed.Skipped, "skipped must equal the exact sum of skipped_by_reason.* in files")

	// Verify .proxer.db files are not counted as skipped extension files on second run.
	require.FileExists(t, filepath.Join(root, config.DBFile))
	refreshedAgain := mustIndex(t, db, root, config.DefaultIgnoredDirs)
	assert.Equal(t, 0, refreshedAgain.Added)
	assert.Equal(t, 2, refreshedAgain.IgnoredDirs)
	require.NotNil(t, refreshedAgain.SkippedByReason)
	assert.Equal(t, 2, refreshedAgain.SkippedByReason.Extension, "internal .proxer.db* files must not inflate skipped_by_reason.extension")
	// Note: bundle.min.js is recorded in manifest so it is not re-read on second walk.
	assert.Equal(t, 6, refreshedAgain.Skipped)
}
