package filesystem_test

import (
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/filesystem"
)

// mockFileInfo implements fs.FileInfo for testing purposes
type mockFileInfo struct {
	name  string
	size  int64
	isDir bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return m.size }
func (m mockFileInfo) Mode() fs.FileMode  { return 0 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return m.isDir }
func (m mockFileInfo) Sys() interface{}   { return nil }

func TestIsIndexableDir(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		dirName  string
		expected bool
	}{
		{name: "Allow root directory", path: ".", dirName: ".", expected: true},
		{name: "Allow standard internal dir", path: "internal", dirName: "internal", expected: true},
		{name: "Block git directory", path: ".git", dirName: ".git", expected: false},
		{name: "Block node_modules", path: "node_modules", dirName: "node_modules", expected: false},
		{name: "Block Next.js build folder", path: ".next", dirName: ".next", expected: false},
		{name: "Block installed skill package", path: ".agents", dirName: ".agents", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := mockFileInfo{name: tt.dirName, isDir: true}
			result := filesystem.IsIndexableDir(tt.path, info)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsIndexableFile(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		fileSize int64
		expected bool
	}{
		{name: "Allow normal Go file", fileName: "main.go", fileSize: 1024, expected: true},
		{name: "Allow exact name Dockerfile", fileName: "Dockerfile", fileSize: 500, expected: true},
		{name: "Allow dot env file", fileName: ".env.local", fileSize: 200, expected: true},
		{name: "Block file over size limit", fileName: "large.go", fileSize: int64(config.MaxFileSize + 1), expected: false},
		{name: "Block unsupported extension", fileName: "image.png", fileSize: 1024, expected: false},
		{name: "Block file with no extension", fileName: "unknown_binary", fileSize: 1024, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := mockFileInfo{name: tt.fileName, size: tt.fileSize, isDir: false}
			result := filesystem.IsIndexableFile(info)
			assert.Equal(t, tt.expected, result)
		})
	}
}
