package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type InstallMode string

const (
	InstallModeProject  InstallMode = "project"
	InstallModeHarness  InstallMode = "harness"
	InstallModeOverride InstallMode = "override"

	DBEnvVar       = "PROXER_DB"
	SkillDirEnvVar = "PROXER_SKILL_DIR"
)

type DBLocation struct {
	DBPath   string
	Mode     InstallMode
	SkillDir string
}

// ResolveDBPath deterministically resolves the SQLite database path and
// installation mode without comparing the executable path against the target root.
func ResolveDBPath(root, explicitDB string, isMCP bool) (DBLocation, error) {
	realRoot := resolveRealPath(root)
	execPath, _ := os.Executable()
	realExec := execPath
	if execPath != "" {
		if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
			realExec = resolved
		}
	}
	detectedSkillDir := HarnessSkillDir(realExec)

	candidate := explicitDB
	if candidate == "" {
		candidate = os.Getenv(DBEnvVar)
	}
	if candidate != "" {
		absDB, err := filepath.Abs(expandHome(candidate))
		if err != nil {
			return DBLocation{}, fmt.Errorf("failed to resolve database path %q: %w", candidate, err)
		}
		return DBLocation{
			DBPath:   absDB,
			Mode:     InstallModeOverride,
			SkillDir: detectedSkillDir,
		}, nil
	}

	if dirExists(filepath.Join(realRoot, ".agents", "skills", "proxer")) ||
		fileExists(filepath.Join(realRoot, DBFile)) {
		return DBLocation{
			DBPath:   filepath.Join(realRoot, DBFile),
			Mode:     InstallModeProject,
			SkillDir: detectedSkillDir,
		}, nil
	}

	if isMCP || detectedSkillDir != "" {
		skillDir := detectedSkillDir
		if skillDir == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return DBLocation{}, fmt.Errorf("failed to resolve home directory for harness database: %w", err)
			}
			skillDir = filepath.Join(homeDir, ".agents", "skills", "proxer")
		}
		dbDir := filepath.Join(skillDir, "database")
		if err := os.MkdirAll(dbDir, 0o700); err != nil {
			return DBLocation{}, fmt.Errorf("failed to create harness database directory %q: %w", dbDir, err)
		}
		name := fmt.Sprintf("%s-%s.db", sanitizeBasename(filepath.Base(realRoot)), sha256Hex(realRoot)[:12])
		return DBLocation{
			DBPath:   filepath.Join(dbDir, name),
			Mode:     InstallModeHarness,
			SkillDir: skillDir,
		}, nil
	}

	return DBLocation{
		DBPath:   filepath.Join(realRoot, DBFile),
		Mode:     InstallModeProject,
		SkillDir: detectedSkillDir,
	}, nil
}

// HarnessSkillDir returns the enclosing <home-or-config>/.../skills/proxer
// directory if realExec resides inside a user-level harness skill directory
// (or PROXER_SKILL_DIR is set), and "" for project-local or standalone binaries.
func HarnessSkillDir(realExec string) string {
	if envDir := os.Getenv(SkillDirEnvVar); envDir != "" {
		return resolveRealPath(expandHome(envDir))
	}
	if realExec == "" {
		return ""
	}

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return ""
	}
	homeDir = resolveRealPath(homeDir)
	configDir, _ := os.UserConfigDir()
	if configDir != "" {
		configDir = resolveRealPath(configDir)
	}

	dir := filepath.Dir(realExec)
	for {
		base := filepath.Base(dir)
		parent := filepath.Dir(dir)
		if base == "proxer" && filepath.Base(parent) == "skills" {
			if isUnderUserHarnessRoot(dir, homeDir, configDir) {
				return dir
			}
			return ""
		}
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func isUnderUserHarnessRoot(skillDir, homeDir, configDir string) bool {
	if configDir != "" {
		if rel, err := filepath.Rel(configDir, skillDir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	if rel, err := filepath.Rel(homeDir, skillDir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		// User-level harness skill directories live under a hidden config dir in $HOME
		// (e.g. ~/.agents/skills/proxer, ~/.claude/skills/proxer, ~/.cursor/skills/proxer).
		return strings.HasPrefix(rel, ".")
	}
	return false
}

func resolveRealPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func sanitizeBasename(name string) string {
	if name == "" || name == "." || name == "/" {
		return "root"
	}
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "root"
	}
	return b.String()
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
