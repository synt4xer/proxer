package filesystem_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/filesystem"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestIgnoreMatcherGitignoreSemantics(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "info", "exclude"), "*.excluded\nlocal-only/\n")
	writeFile(t, filepath.Join(root, ".gitignore"), ""+
		"# comment\n"+
		"build/\n"+
		"/rooted.go\n"+
		"*.log\n"+
		"!important.log\n"+
		"a/**/b.go\n"+
		"gen/**\n"+
		"nested/*.tmp\n")
	writeFile(t, filepath.Join(root, "nested", ".gitignore"), ""+
		"*.py\n"+
		"!keep.py\n"+
		"important.log\n")

	m := filesystem.NewIgnoreMatcher(root, config.DefaultIgnoredDirs)

	// DefaultIgnoredDirs including glob patterns
	assert.True(t, m.IsDirIgnored(".venv"))
	assert.True(t, m.IsDirIgnored("pkg/my_app.egg-info"))
	assert.True(t, filesystem.IsIgnoredDirName("foo.egg-info", config.DefaultIgnoredDirs))
	assert.True(t, filesystem.PathHasIgnoredDir("pkg/my_app.egg-info/PKG-INFO", config.DefaultIgnoredDirs))

	// Generic dirs not in DefaultIgnoredDirs unless matched by .gitignore
	assert.False(t, m.IsDirIgnored("target"))
	assert.False(t, m.IsDirIgnored(".github"))
	assert.True(t, m.IsDirIgnored("build"))
	assert.True(t, m.IsDirIgnored("sub/build"))
	assert.False(t, m.IsFileIgnored("build"), "trailing / must match directories only")

	// .git/info/exclude
	assert.True(t, m.IsFileIgnored("foo.excluded"))
	assert.True(t, m.IsDirIgnored("local-only"))

	// Anchored leading /
	assert.True(t, m.IsFileIgnored("rooted.go"))
	assert.False(t, m.IsFileIgnored("sub/rooted.go"))

	// Negation at root and override in nested .gitignore
	assert.True(t, m.IsFileIgnored("app.log"))
	assert.False(t, m.IsFileIgnored("important.log"))
	assert.True(t, m.IsFileIgnored("nested/important.log"), "nested .gitignore must override root negation")

	// Nested .gitignore negation
	assert.True(t, m.IsFileIgnored("nested/drop.py"))
	assert.False(t, m.IsFileIgnored("nested/keep.py"))
	assert.False(t, m.IsFileIgnored("other/drop.py"), "nested .gitignore must not leak outside its subtree")

	// Double-star globs
	assert.True(t, m.IsFileIgnored("a/b.go"))
	assert.True(t, m.IsFileIgnored("a/x/y/b.go"))
	assert.False(t, m.IsDirIgnored("gen"), "gen/** matches inside gen, not gen itself")
	assert.True(t, m.IsFileIgnored("gen/out.go"))
	assert.True(t, m.IsFileIgnored("gen/sub/out.go"))

	// Anchored middle slash
	assert.True(t, m.IsFileIgnored("nested/x.tmp"))
	assert.False(t, m.IsFileIgnored("other/nested/x.tmp"))

	// IsPathIgnored propagates parent dir ignores for single-file reindex
	assert.True(t, m.IsPathIgnored("build/out.go", false))
	assert.True(t, m.IsPathIgnored(".venv/lib/site.py", false))
	assert.False(t, m.IsPathIgnored("nested/keep.py", false))
}

func TestUnifiedRegistryExtensionsAndExactNames(t *testing.T) {
	newCode := []string{
		"mod.mjs", "mod.cjs", "mod.mts", "mod.cts",
		"App.kt", "build.gradle.kts", "App.swift", "Obj.m", "Obj.mm",
		"hdr.h", "hdr.hpp", "hdr.hh", "impl.cc",
		"api.proto", "schema.graphql", "query.gql", "pom.xml",
		"build.gradle", "main.tf", "prod.tfvars", "policy.hcl",
		"main.dart", "init.lua", "app.ex", "test.exs", "srv.erl",
		"main.zig", "Main.scala", "core.clj", "Program.cs", "Lib.fs",
		"App.vue", "App.svelte", "stubs.pyi", "notebook.ipynb",
	}
	for _, name := range newCode {
		ok, reason := filesystem.IsIndexableFileName(name, false)
		assert.True(t, ok, "expected indexable code file: %s", name)
		assert.Equal(t, filesystem.SkipNone, reason)
	}

	docs := []string{"README.md", "guide.mdx", "notes.txt", "manual.rst"}
	for _, name := range docs {
		ok, reason := filesystem.IsIndexableFileName(name, false)
		assert.True(t, ok, "expected indexable doc file: %s", name)
		assert.Equal(t, filesystem.SkipNone, reason)
	}

	exact := []string{
		"Dockerfile", "Makefile", "Gemfile", "Rakefile", "Podfile",
		"Jenkinsfile", "BUILD", "WORKSPACE",
		"docker-compose.yml", "docker-compose.yaml",
		"package.json", "go.mod", "go.sum",
		".env.example", ".env.sample", ".env.template",
	}
	for _, name := range exact {
		ok, reason := filesystem.IsIndexableFileName(name, false)
		assert.True(t, ok, "expected indexable exact filename: %s", name)
		assert.Equal(t, filesystem.SkipNone, reason)
	}
}
