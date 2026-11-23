package filesystem

import (
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/synt4xer/proxer/internal/config"
)

type SkipReason string

const (
	SkipNone        SkipReason = ""
	SkipIgnoredFile SkipReason = "ignored_files"
	SkipSensitive   SkipReason = "sensitive"
	SkipExtension   SkipReason = "extension"
	SkipSize        SkipReason = "size"
	SkipMinified    SkipReason = "minified"
)

var (
	allowedExtensions = toSet(config.AllowedExtensions)
	allowedExactNames = toSet(config.AllowedExactNames)

	safeEnvTemplates = map[string]struct{}{
		".env.example":  {},
		".env.sample":   {},
		".env.template": {},
	}

	sensitiveExtensions = map[string]struct{}{
		".pem":      {},
		".key":      {},
		".p12":      {},
		".pfx":      {},
		".jks":      {},
		".keystore": {},
		".kdbx":     {},
	}

	sensitiveExactNames = map[string]struct{}{
		".npmrc":           {},
		".pypirc":          {},
		".netrc":           {},
		".htpasswd":        {},
		"credentials.json": {},
	}

	sensitiveKeyPrefixes = []string{
		"id_rsa",
		"id_ed25519",
		"id_ecdsa",
		"id_dsa",
	}
)

func toSet(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item] = struct{}{}
	}
	return set
}

func IsIgnoredDirName(name string, ignoredDirs []string) bool {
	return slices.Contains(ignoredDirs, name)
}

// PathHasIgnoredDir reports whether any directory segment of a root-relative
// path is ignored. The last segment is a file name and is not checked.
func PathHasIgnoredDir(relPath string, ignoredDirs []string) bool {
	segments := strings.Split(filepath.ToSlash(relPath), "/")
	for _, dir := range segments[:len(segments)-1] {
		if IsIgnoredDirName(dir, ignoredDirs) {
			return true
		}
	}
	return false
}

func IsIndexableDir(p string, info fs.FileInfo, ignoredDirs []string) bool {
	name := info.Name()
	if p == "." || name == "." {
		return true
	}
	return !IsIgnoredDirName(name, ignoredDirs)
}

// IsSensitiveFileName reports whether a single base filename matches the
// credentials/secrets denylist. Safe .env templates (.env.example, .env.sample,
// .env.template) return false.
func IsSensitiveFileName(baseName string) bool {
	if _, ok := safeEnvTemplates[baseName]; ok {
		return false
	}

	// .env family (exact / dot-boundary only so .envoy.yaml, .environment.ts,
	// and .envision.json are never falsely classified as sensitive).
	if baseName == ".env" || baseName == ".envrc" ||
		strings.HasPrefix(baseName, ".env.") || strings.HasSuffix(baseName, ".env") {
		return true
	}

	lower := strings.ToLower(baseName)

	if _, ok := sensitiveExactNames[lower]; ok {
		return true
	}

	ext := strings.ToLower(filepath.Ext(lower))
	if _, ok := sensitiveExtensions[ext]; ok {
		return true
	}

	for _, prefix := range sensitiveKeyPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	if strings.HasPrefix(lower, "service-account") && strings.HasSuffix(lower, ".json") {
		return true
	}

	if strings.HasSuffix(lower, ".tfstate") || strings.HasSuffix(lower, ".tfstate.backup") {
		return true
	}

	return false
}

func IsSensitivePath(relPath string) bool {
	return IsSensitiveFileName(path.Base(filepath.ToSlash(relPath)))
}

// IsIndexableFileName reports whether a file's base name or extension is
// eligible for indexing, without requiring an fs.FileInfo stat call.
func IsIndexableFileName(baseName string, indexSensitive bool) (bool, SkipReason) {
	if _, ok := safeEnvTemplates[baseName]; ok {
		return true, SkipNone
	}

	sensitive := IsSensitiveFileName(baseName)
	if !indexSensitive && sensitive {
		return false, SkipSensitive
	}

	if _, ok := allowedExactNames[baseName]; ok {
		return true, SkipNone
	}
	if indexSensitive && sensitive {
		return true, SkipNone
	}

	ext := strings.ToLower(filepath.Ext(baseName))
	if ext != "" {
		if _, ok := allowedExtensions[ext]; ok {
			return true, SkipNone
		}
	}

	return false, SkipExtension
}

func IsIndexableFile(info fs.FileInfo, indexSensitive bool) (bool, SkipReason) {
	if info.Size() > config.MaxFileSize {
		return false, SkipSize
	}

	return IsIndexableFileName(info.Name(), indexSensitive)
}
