package config

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const (
	Version          = "0.7.0"
	DBFile           = ".proxer.db"
	MaxFileSize      = 1024 * 1024 // 1 MB
	MaxLineWidth     = 4096        // 4 KB
	MaxReadBytes     = 64 * 1024   // 64 KB
	TruncationMarker = " [truncated]"
	ChunkSize        = 60
	OverlapSize      = 15
)

// RootEnvVar names the environment variable consulted when no --root flag is
// given. Servers spawned by a harness rarely inherit a useful working
// directory, so the project root has to be resolvable from outside the process.
const RootEnvVar = "PROXER_ROOT"

// ResolveRoot turns the --root flag, the PROXER_ROOT environment variable, or
// the working directory into an absolute directory. The order matters: an
// explicit flag must always beat ambient state, and an ambient variable must
// beat the working directory because harnesses spawn processes from wherever
// the session started.
func ResolveRoot(explicit string) (string, error) {
	candidate := explicit
	if candidate == "" {
		candidate = os.Getenv(RootEnvVar)
	}
	if candidate == "" {
		candidate = "."
	}
	candidate = expandHome(candidate)

	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("failed to resolve the project root %q: %w", candidate, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("project root %q is not usable: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project root %q is not a directory", abs)
	}

	return abs, nil
}

// expandHome resolves a leading tilde so a shell is not required. MCP server
// configuration passes arguments literally, so "~/code/app" would otherwise
// name a directory that does not exist.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// DefaultIgnoredDirs is the built-in skip list of unambiguous tool and cache
// directories. Generic names (build, out, target, deps, dist, bin, obj,
// .github, env) are intentionally excluded and handled via .gitignore or
// .proxer.json. Glob patterns like *.egg-info are matched via path.Match.
var DefaultIgnoredDirs = []string{
	// VCS & editors
	".git", ".agents", ".vscode", ".idea",
	// JS / Web
	"node_modules", ".next", ".nuxt", ".svelte-kit", ".astro", ".turbo", ".parcel-cache", ".yarn", "bower_components",
	// Go / PHP / Ruby deps
	"vendor", ".bundle",
	// Python
	".venv", "venv", "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".tox", ".nox", ".ipynb_checkpoints", "*.egg-info", "site-packages",
	// JVM / Kotlin / Android
	".gradle", ".kotlin", ".mvn",
	// Apple
	"Pods", "DerivedData", ".build", ".swiftpm",
	// Dart / Flutter
	".dart_tool", ".pub-cache",
	// Elixir / Erlang
	"_build", ".elixir_ls",
	// Haskell / OCaml
	".stack-work", "dist-newstyle", "_opam",
	// Infra
	".terraform", ".terragrunt-cache", ".serverless", ".aws-sam",
	// Generic caches
	".cache", ".direnv", "coverage", "tmp",
}

// NoRefreshEnvVar names the environment variable that keeps `search` read-only.
// A harness that mounts the project read-only, or one that deliberately wants
// to query a pinned snapshot, sets it instead of passing a flag it cannot pass
// through the MCP server.
const NoRefreshEnvVar = "PROXER_NO_REFRESH"

// RefreshDisabled reports whether the environment turns the pre-search refresh
// off. Only an explicit truthy value counts, so an unrelated "0" or "false" in
// the environment cannot silently disable freshness.
func RefreshDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(NoRefreshEnvVar))) {
	case "", "0", "false", "no":
		return false
	}
	return true
}

const ConfigFile = ".proxer.json"

type ProjectConfig struct {
	IgnoredDirs    []string `json:"ignored_dirs"`
	IndexSensitive bool     `json:"index_sensitive"`
}

// LoadProjectConfig reads ConfigFile from root and merges its ignored_dirs into
// DefaultIgnoredDirs. A missing or empty file yields the defaults. A malformed
// file is an error: falling back silently would index directories or sensitive
// files the project configured explicitly.
func LoadProjectConfig(root string) (ProjectConfig, error) {
	result := ProjectConfig{
		IgnoredDirs: slices.Clone(DefaultIgnoredDirs),
	}

	data, err := os.ReadFile(filepath.Join(root, ConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return ProjectConfig{}, fmt.Errorf("failed to read %s: %w", ConfigFile, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return result, nil
	}

	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg, json.RejectUnknownMembers(true)); err != nil {
		return ProjectConfig{}, fmt.Errorf("invalid %s: %w", ConfigFile, err)
	}

	for _, dir := range cfg.IgnoredDirs {
		if err := validateIgnoredDir(dir); err != nil {
			return ProjectConfig{}, fmt.Errorf("invalid %s: %w", ConfigFile, err)
		}
		if !slices.Contains(result.IgnoredDirs, dir) {
			result.IgnoredDirs = append(result.IgnoredDirs, dir)
		}
	}
	result.IndexSensitive = cfg.IndexSensitive

	return result, nil
}

func LoadIgnoredDirs(root string) ([]string, error) {
	cfg, err := LoadProjectConfig(root)
	if err != nil {
		return nil, err
	}
	return cfg.IgnoredDirs, nil
}

// validateIgnoredDir rejects entries that can never match. Directories are
// matched by base name (or base-name glob) at any depth, so an entry with a
// path separator or invalid glob syntax fails fast instead of silently never
// applying.
func validateIgnoredDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("ignored_dirs must not contain an empty name")
	}
	if dir == "." || dir == ".." || strings.ContainsAny(dir, `/\`) {
		return fmt.Errorf("ignored_dirs entry %q must be a single directory name, not a path", dir)
	}
	if _, err := path.Match(dir, ""); err != nil {
		return fmt.Errorf("ignored_dirs entry %q has invalid glob pattern: %w", dir, err)
	}
	return nil
}

// FileKind classifies an indexable file in the authoritative extension and
// exact-name registries shared by the filesystem scanner and search ranking.
type FileKind int

const (
	KindCode FileKind = iota
	KindDocs
)

// AllowedExtensions is the single authoritative mapping from lowercase file
// extension to FileKind. Both filesystem indexing and tools kind classification
// read from this map so they cannot drift.
var AllowedExtensions = map[string]FileKind{
	// Code & config
	".go":      KindCode,
	".rb":      KindCode,
	".py":      KindCode,
	".pyi":     KindCode,
	".ipynb":   KindCode,
	".php":     KindCode,
	".rs":      KindCode,
	".java":    KindCode,
	".kt":      KindCode,
	".kts":     KindCode,
	".scala":   KindCode,
	".clj":     KindCode,
	".c":       KindCode,
	".cc":      KindCode,
	".cpp":     KindCode,
	".h":       KindCode,
	".hh":      KindCode,
	".hpp":     KindCode,
	".m":       KindCode,
	".mm":      KindCode,
	".cs":      KindCode,
	".fs":      KindCode,
	".swift":   KindCode,
	".dart":    KindCode,
	".lua":     KindCode,
	".ex":      KindCode,
	".exs":     KindCode,
	".erl":     KindCode,
	".zig":     KindCode,
	".js":      KindCode,
	".jsx":     KindCode,
	".mjs":     KindCode,
	".cjs":     KindCode,
	".ts":      KindCode,
	".tsx":     KindCode,
	".mts":     KindCode,
	".cts":     KindCode,
	".vue":     KindCode,
	".svelte":  KindCode,
	".astro":   KindCode,
	".html":    KindCode,
	".css":     KindCode,
	".scss":    KindCode,
	".sass":    KindCode,
	".less":    KindCode,
	".xml":     KindCode,
	".json":    KindCode,
	".yaml":    KindCode,
	".yml":     KindCode,
	".toml":    KindCode,
	".ini":     KindCode,
	".sql":     KindCode,
	".sh":      KindCode,
	".bash":    KindCode,
	".proto":   KindCode,
	".graphql": KindCode,
	".gql":     KindCode,
	".gradle":  KindCode,
	".tf":      KindCode,
	".tfvars":  KindCode,
	".hcl":     KindCode,

	// Docs
	".md":  KindDocs,
	".mdx": KindDocs,
	".txt": KindDocs,
	".rst": KindDocs,
}

// AllowedExactNames is the single authoritative mapping from exact base
// filename to FileKind. Safe .env templates live here because filepath.Ext
// returns ".example"/".sample"/".template", which are not in AllowedExtensions.
var AllowedExactNames = map[string]FileKind{
	"Dockerfile":          KindCode,
	"Makefile":            KindCode,
	"Gemfile":             KindCode,
	"Rakefile":            KindCode,
	"Podfile":             KindCode,
	"Jenkinsfile":         KindCode,
	"BUILD":               KindCode,
	"WORKSPACE":           KindCode,
	"docker-compose.yml":  KindCode,
	"docker-compose.yaml": KindCode,
	"package.json":        KindCode,
	"go.mod":              KindCode,
	".env.example":        KindCode,
	".env.sample":         KindCode,
	".env.template":       KindCode,
}
