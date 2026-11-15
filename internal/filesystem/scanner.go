package filesystem

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/synt4xer/proxer/internal/config"
)

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func IsIndexableDir(path string, info fs.FileInfo) bool {
	name := info.Name()
	if path == "." || name == "." {
		return true
	}
	if contains(config.IgnoredDirs, name) {
		return false
	}
	return true
}

func IsIndexableFile(info fs.FileInfo) bool {
	if info.Size() > config.MaxFileSize {
		return false
	}

	name := info.Name()
	if contains(config.AllowedExactNames, name) || strings.HasPrefix(name, ".env") {
		return true
	}

	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}

	return contains(config.AllowedExtensions, ext)
}
