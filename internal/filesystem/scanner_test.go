package filesystem_test

import (
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/filesystem"
)

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
			result := filesystem.IsIndexableDir(tt.path, info, config.DefaultIgnoredDirs)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsIndexableFile(t *testing.T) {
	tests := []struct {
		name           string
		fileName       string
		fileSize       int64
		indexSensitive bool
		expected       bool
		wantReason     filesystem.SkipReason
	}{
		{name: "Allow normal Go file", fileName: "main.go", fileSize: 1024, expected: true, wantReason: filesystem.SkipNone},
		{name: "Allow exact name Dockerfile", fileName: "Dockerfile", fileSize: 500, expected: true, wantReason: filesystem.SkipNone},
		{name: "Allow safe env template", fileName: ".env.example", fileSize: 200, expected: true, wantReason: filesystem.SkipNone},
		{name: "Allow tailwind css via .css", fileName: "app.tailwind.css", fileSize: 512, expected: true, wantReason: filesystem.SkipNone},
		{name: "Block dot env local by default", fileName: ".env.local", fileSize: 200, expected: false, wantReason: filesystem.SkipSensitive},
		{name: "Allow dot env local when opted in", fileName: ".env.local", fileSize: 200, indexSensitive: true, expected: true, wantReason: filesystem.SkipNone},
		{name: "Block file over size limit", fileName: "large.go", fileSize: int64(config.MaxFileSize + 1), expected: false, wantReason: filesystem.SkipSize},
		{name: "Block unsupported extension", fileName: "image.png", fileSize: 1024, expected: false, wantReason: filesystem.SkipExtension},
		{name: "Block file with no extension", fileName: "unknown_binary", fileSize: 1024, expected: false, wantReason: filesystem.SkipExtension},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := mockFileInfo{name: tt.fileName, size: tt.fileSize, isDir: false}
			result, reason := filesystem.IsIndexableFile(info, tt.indexSensitive)
			assert.Equal(t, tt.expected, result)
			assert.Equal(t, tt.wantReason, reason)
			if tt.fileSize <= config.MaxFileSize {
				nameOK, nameReason := filesystem.IsIndexableFileName(tt.fileName, tt.indexSensitive)
				assert.Equal(t, tt.expected, nameOK)
				assert.Equal(t, tt.wantReason, nameReason)
			}
		})
	}
}

func TestSensitiveDenylistAndEnvBoundary(t *testing.T) {
	denied := []string{
		".env",
		".envrc",
		".env.local",
		".env.production",
		".env.yaml",
		".env.json",
		"prod.env",
		"config/.env",
		"sub/prod.env",
		"id_rsa",
		"id_rsa.pub",
		"id_ed25519",
		"id_ecdsa",
		"id_dsa",
		"server.pem",
		"tls.key",
		"cert.p12",
		"cert.pfx",
		"trust.jks",
		"app.keystore",
		"vault.kdbx",
		".npmrc",
		".pypirc",
		".netrc",
		".htpasswd",
		"credentials.json",
		"service-account-prod.json",
		"terraform.tfstate",
		"terraform.tfstate.backup",
	}
	for _, p := range denied {
		assert.True(t, filesystem.IsSensitivePath(p), "expected sensitive: %s", p)
		ok, reason := filesystem.IsIndexableFileName(filepathBase(p), false)
		assert.False(t, ok, "expected excluded by default: %s", p)
		assert.Equal(t, filesystem.SkipSensitive, reason, "expected SkipSensitive for %s", p)
	}

	allowed := []string{
		".envoy.yaml",
		".environment.ts",
		".envision.json",
		"secrets.go",
		"credentials.py",
		"CredentialsProvider.java",
		"secret_manager.rs",
		".env.example",
		".env.sample",
		".env.template",
	}
	for _, p := range allowed {
		assert.False(t, filesystem.IsSensitivePath(p), "expected non-sensitive: %s", p)
		ok, reason := filesystem.IsIndexableFileName(p, false)
		assert.True(t, ok, "expected indexable: %s", p)
		assert.Equal(t, filesystem.SkipNone, reason, "expected SkipNone for %s", p)
	}
}

func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func TestIsIndexableDirHonorsAProjectSkipList(t *testing.T) {
	ignored := append(append([]string{}, config.DefaultIgnoredDirs...), "third_party")

	info := mockFileInfo{name: "third_party", isDir: true}
	assert.False(t, filesystem.IsIndexableDir("third_party", info, ignored))
	assert.True(t, filesystem.IsIndexableDir("third_party", info, config.DefaultIgnoredDirs),
		"a name outside the effective list must stay indexable")
}

func TestPathHasIgnoredDir(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{name: "RootFile", path: "main.go", expected: false},
		{name: "CleanSubdir", path: "internal/pkg/file.go", expected: false},
		{name: "IgnoredDirectly", path: "generated/file.go", expected: true},
		{name: "IgnoredAtDepth", path: "internal/generated/file.go", expected: true},
		{name: "IgnoredNameAsFile", path: "pkg/generated", expected: false},
	}

	ignored := append(append([]string{}, config.DefaultIgnoredDirs...), "generated")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, filesystem.PathHasIgnoredDir(tt.path, ignored))
		})
	}
}
