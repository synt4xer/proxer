package filesystem

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/synt4xer/proxer/internal/config"
)

var (
	allowedExtensions = toSet(config.AllowedExtensions)
	allowedExactNames = toSet(config.AllowedExactNames)
)

func toSet(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item] = struct{}{}
	}
	return set
}

// IsIgnoredDirName reports whether a directory name is one the index must skip.
// Names are matched whole, at any depth.
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

func IsIndexableDir(path string, info fs.FileInfo, ignoredDirs []string) bool {
	name := info.Name()
	if path == "." || name == "." {
		return true
	}
	return !IsIgnoredDirName(name, ignoredDirs)
}

// IsIndexableFileName reports whether a file's name or extension is eligible
// for indexing, without requiring an fs.FileInfo stat call.
func IsIndexableFileName(name string) bool {
	if _, ok := allowedExactNames[name]; ok || strings.HasPrefix(name, ".env") {
		return true
	}

	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}

	_, ok := allowedExtensions[ext]
	return ok
}

func IsIndexableFile(info fs.FileInfo) bool {
	if info.Size() > config.MaxFileSize {
		return false
	}

	return IsIndexableFileName(info.Name())
}
