package config

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	Version          = "0.6.0"
	DBFile           = ".proxer.db"
	MaxFileSize      = 1024 * 1024 // 1 MB
	MaxLineWidth     = 4096        // 4 KB
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

// DefaultIgnoredDirs is the built-in skip list. A project extends it through
// ConfigFile; the defaults always stay in force so an unconfigured project
// cannot accidentally index node_modules or a build directory.
var DefaultIgnoredDirs = []string{
	".git", ".vscode", ".idea", ".github",
	".agents",
	"node_modules", "vendor",
	"dist", "build", "out", "target",
	"bin", "obj", "tmp", "coverage",
	".next", ".nuxt", ".svelte-kit",
	".astro",
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

// ConfigFile is the optional per-project configuration read from the project
// root.
const ConfigFile = ".proxer.json"

// ProjectConfig is the on-disk shape of ConfigFile.
type ProjectConfig struct {
	IgnoredDirs []string `json:"ignored_dirs"`
}

// LoadIgnoredDirs returns DefaultIgnoredDirs extended with the directories the
// project lists in ConfigFile. A missing or empty file yields the defaults, so
// an unconfigured project behaves exactly as before. A malformed file is an
// error: falling back silently would index directories the project excluded.
func LoadIgnoredDirs(root string) ([]string, error) {
	ignored := slices.Clone(DefaultIgnoredDirs)

	data, err := os.ReadFile(filepath.Join(root, ConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return ignored, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", ConfigFile, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return ignored, nil
	}

	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", ConfigFile, err)
	}

	for _, dir := range cfg.IgnoredDirs {
		if err := validateIgnoredDir(dir); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", ConfigFile, err)
		}
		if !slices.Contains(ignored, dir) {
			ignored = append(ignored, dir)
		}
	}

	return ignored, nil
}

// validateIgnoredDir rejects entries that can never match. Directories are
// matched by base name at any depth, so an entry with a path separator would
// silently never apply, which is worse than refusing to start.
func validateIgnoredDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("ignored_dirs must not contain an empty name")
	}
	if dir == "." || dir == ".." || strings.ContainsAny(dir, `/\`) {
		return fmt.Errorf("ignored_dirs entry %q must be a single directory name, not a path", dir)
	}
	return nil
}

var AllowedExtensions = []string{
	".go", ".rb", ".py", ".php", ".rs", ".java", ".c", ".cpp", ".cs",
	".js", ".jsx", ".ts", ".tsx", ".vue", ".svelte", ".astro",
	".html", ".css", ".scss", ".sass", ".less",
	".md", ".json", ".yaml", ".yml", ".toml", ".ini", ".env",
	".sql", ".sh", ".bash",
}

var AllowedExactNames = []string{
	"Dockerfile", "Makefile", "docker-compose.yml", "Gemfile", "package.json", "go.mod", "go.sum",
}
