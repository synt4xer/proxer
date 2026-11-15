package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	DBFile      = ".proxer.db"
	MaxFileSize = 1024 * 1024 // 1 MB
	ChunkSize   = 60
	OverlapSize = 15
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

var IgnoredDirs = []string{
	".git", ".vscode", ".idea", ".github",
	".agents",
	"node_modules", "vendor",
	"dist", "build", "out", "target",
	"bin", "obj", "tmp", "coverage",
	".next", ".nuxt", ".svelte-kit",
	".astro",
}

var AllowedExtensions = []string{
	".go", ".rb", ".py", ".php", ".rs", ".java", ".c", ".cpp", ".cs",
	".js", ".jsx", ".ts", ".tsx", ".vue", ".svelte", ".astro",
	".html", ".css", ".scss", ".sass", ".less", ".tailwind.css",
	".md", ".json", ".yaml", ".yml", ".toml", ".ini", ".env",
	".sql", ".sh", ".bash",
}

var AllowedExactNames = []string{
	"Dockerfile", "Makefile", "docker-compose.yml", "Gemfile", "package.json", "go.mod", "go.sum",
}
