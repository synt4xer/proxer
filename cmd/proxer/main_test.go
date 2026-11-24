package main

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/synt4xer/proxer/internal/config"
)

type jsonType string

const (
	jsonString jsonType = "string"
	jsonInt    jsonType = "int"
	jsonBool   jsonType = "bool"
	jsonArray  jsonType = "array"
	jsonObject jsonType = "object"
)

func runCLI(args []string, stdin string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func decodeSingleLineJSON(t *testing.T, code int, stdout, stderr string) map[string]jsontext.Value {
	t.Helper()

	require.Equal(t, 0, code, "expected exit 0, stderr: %q", stderr)
	assert.Empty(t, stderr)
	require.True(t, strings.HasSuffix(stdout, "\n"), "stdout must end with newline: %q", stdout)

	line := strings.TrimSuffix(stdout, "\n")
	assert.NotContains(t, line, "\n", "stdout must be a single line of JSON")

	var obj map[string]jsontext.Value
	require.NoError(t, json.Unmarshal([]byte(line), &obj))
	return obj
}

func unmarshalJSON[T any](t *testing.T, raw jsontext.Value) T {
	t.Helper()

	var out T
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func assertJSONSchema(t *testing.T, obj map[string]jsontext.Value, want map[string]jsonType) {
	t.Helper()

	assert.Len(t, obj, len(want), "unexpected JSON key set: got %v", obj)
	for key, wantType := range want {
		val, ok := obj[key]
		require.True(t, ok, "missing expected JSON key %q", key)
		assertJSONValueType(t, key, val, wantType)
	}
}

func assertJSONValueType(t *testing.T, key string, val jsontext.Value, wantType jsonType) {
	t.Helper()

	kind := val.Kind()
	switch wantType {
	case jsonString:
		assert.Equal(t, jsontext.Kind('"'), kind, "key %q: expected string, got %s", key, val)
	case jsonInt:
		require.Equal(t, jsontext.Kind('0'), kind, "key %q: expected JSON number, got %s", key, val)
		_, err := strconv.ParseInt(string(val), 10, 64)
		assert.NoError(t, err, "key %q: expected integer JSON number, got %s", key, val)
	case jsonBool:
		assert.True(t, kind == 't' || kind == 'f', "key %q: expected bool, got %s", key, val)
	case jsonArray:
		assert.Equal(t, jsontext.Kind('['), kind, "key %q: expected array, got %s", key, val)
	case jsonObject:
		assert.Equal(t, jsontext.Kind('{'), kind, "key %q: expected object, got %s", key, val)
	default:
		t.Fatalf("unknown jsonType %q", wantType)
	}
}

func assertCLIError(t *testing.T, code int, stdout, stderr, wantSubstr string) {
	t.Helper()

	assert.Equal(t, 1, code)
	assert.Empty(t, stdout)
	require.True(t, strings.HasSuffix(stderr, "\n"), "stderr must end with newline: %q", stderr)
	assert.NotContains(t, strings.TrimSuffix(stderr, "\n"), "\n", "stderr must be a single line: %q", stderr)
	if wantSubstr != "" {
		assert.Contains(t, stderr, wantSubstr)
	}
}

func setupFixtureRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	code := "package main\n\nfunc ComputeAnswer() int {\n\treturn 42\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(code), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("# Demo\nComputeAnswer docs\n"), 0o644))
	return root
}

func TestCLIVersion(t *testing.T) {
	code, stdout, stderr := runCLI([]string{"--version"}, "")
	assert.Equal(t, 0, code)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, config.Version)
}

func TestCLIGoldenEnvelopes(t *testing.T) {
	root := setupFixtureRoot(t)

	code, stdout, stderr := runCLI([]string{"--root", root, "index"}, "")
	indexObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assertJSONSchema(t, indexObj, map[string]jsonType{
		"status":        jsonString,
		"refreshed":     jsonObject,
		"db_size_bytes": jsonInt,
	})
	assert.Equal(t, "indexed_successfully", unmarshalJSON[string](t, indexObj["status"]))
	refreshedObj := unmarshalJSON[map[string]jsontext.Value](t, indexObj["refreshed"])
	assertJSONSchema(t, refreshedObj, map[string]jsonType{
		"added":   jsonInt,
		"changed": jsonInt,
		"removed": jsonInt,
		"skipped": jsonInt,
	})
	assert.Equal(t, 2, unmarshalJSON[int](t, refreshedObj["added"]))

	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "ComputeAnswer"}, "")
	assert.Contains(t, stdout, "<b>ComputeAnswer</b>", "HTML tags in snippets must not be escaped")
	assert.NotContains(t, stdout, `\u003c`)
	searchObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assertJSONSchema(t, searchObj, map[string]jsonType{
		"root":       jsonString,
		"indexed_at": jsonString,
		"refreshed":  jsonObject,
		"total":      jsonInt,
		"truncated":  jsonBool,
		"results":    jsonArray,
	})
	assertJSONSchema(t, unmarshalJSON[map[string]jsontext.Value](t, searchObj["refreshed"]), map[string]jsonType{
		"added":   jsonInt,
		"changed": jsonInt,
		"removed": jsonInt,
		"skipped": jsonInt,
	})
	results := unmarshalJSON[[]map[string]jsontext.Value](t, searchObj["results"])
	require.NotEmpty(t, results)
	firstResult := results[0]
	assertJSONSchema(t, firstResult, map[string]jsonType{
		"root":     jsonString,
		"filepath": jsonString,
		"chunk":    jsonString,
		"line":     jsonInt,
		"snippet":  jsonString,
		"match":    jsonString,
		"hash":     jsonString,
	})
	assert.Equal(t, "main.go", unmarshalJSON[string](t, firstResult["filepath"]))
	assert.Equal(t, 3, unmarshalJSON[int](t, firstResult["line"]))

	code, stdout, stderr = runCLI([]string{"--root", root, "read", "-f", "main.go", "-s", "1", "-e", "5"}, "")
	readObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assertJSONSchema(t, readObj, map[string]jsonType{
		"root":        jsonString,
		"filepath":    jsonString,
		"start_line":  jsonInt,
		"end_line":    jsonInt,
		"total_lines": jsonInt,
		"truncated":   jsonBool,
		"content":     jsonString,
		"hash":        jsonString,
	})
	assert.Equal(t, "main.go", unmarshalJSON[string](t, readObj["filepath"]))
	assert.Equal(t, 1, unmarshalJSON[int](t, readObj["start_line"]))
	assert.Equal(t, 5, unmarshalJSON[int](t, readObj["end_line"]))
	assert.Equal(t, 5, unmarshalJSON[int](t, readObj["total_lines"]))
	assert.False(t, unmarshalJSON[bool](t, readObj["truncated"]))
	assert.Contains(t, unmarshalJSON[string](t, readObj["content"]), "return 42")
	hash := unmarshalJSON[string](t, readObj["hash"])
	require.NotEmpty(t, hash)

	code, stdout, stderr = runCLI(
		[]string{"--root", root, "edit", "-f", "main.go", "-s", "3", "-e", "5", "--hash", hash},
		"func ComputeAnswer() int {\n\treturn 99\n}",
	)
	editObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assertJSONSchema(t, editObj, map[string]jsonType{
		"status":  jsonString,
		"message": jsonString,
		"hash":    jsonString,
	})
	assert.Equal(t, "success", unmarshalJSON[string](t, editObj["status"]))
	assert.NotEmpty(t, unmarshalJSON[string](t, editObj["hash"]))

	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "99", "--no-refresh"}, "")
	afterEditObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assert.Equal(t, 1, unmarshalJSON[int](t, afterEditObj["total"]))
}

func TestCLISearchFlagsAndNoRefresh(t *testing.T) {
	root := setupFixtureRoot(t)

	code, stdout, stderr := runCLI([]string{"--root", root, "index"}, "")
	decodeSingleLineJSON(t, code, stdout, stderr)

	require.NoError(t, os.WriteFile(
		filepath.Join(root, "extra.go"),
		[]byte("package main\n\nfunc BrandNewSymbol() {}\n"),
		0o644,
	))

	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "BrandNewSymbol", "--no-refresh"}, "")
	noRefreshObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assertJSONSchema(t, noRefreshObj, map[string]jsonType{
		"root":       jsonString,
		"indexed_at": jsonString,
		"total":      jsonInt,
		"truncated":  jsonBool,
		"results":    jsonArray,
	})
	_, hasRefreshed := noRefreshObj["refreshed"]
	assert.False(t, hasRefreshed)
	assert.Equal(t, 0, unmarshalJSON[int](t, noRefreshObj["total"]))

	t.Setenv(config.NoRefreshEnvVar, "1")
	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "BrandNewSymbol"}, "")
	envObj := decodeSingleLineJSON(t, code, stdout, stderr)
	_, hasRefreshed = envObj["refreshed"]
	assert.False(t, hasRefreshed)
	assert.Equal(t, 0, unmarshalJSON[int](t, envObj["total"]))

	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "ComputeAnswer", "--kind", "docs", "-n", "1"}, "")
	docsObj := decodeSingleLineJSON(t, code, stdout, stderr)
	results := unmarshalJSON[[]map[string]jsontext.Value](t, docsObj["results"])
	require.Len(t, results, 1)
	assert.Equal(t, "README.md", unmarshalJSON[string](t, results[0]["filepath"]))
}

func TestCLIErrorPaths(t *testing.T) {
	root := setupFixtureRoot(t)

	code, stdout, stderr := runCLI([]string{"--root", root, "index"}, "")
	decodeSingleLineJSON(t, code, stdout, stderr)

	t.Run("missing required keyword on search", func(t *testing.T) {
		code, stdout, stderr := runCLI([]string{"--root", root, "search"}, "")
		assertCLIError(t, code, stdout, stderr, "keyword")
	})

	t.Run("empty keyword on search", func(t *testing.T) {
		code, stdout, stderr := runCLI([]string{"--root", root, "search", "-k", ""}, "")
		assertCLIError(t, code, stdout, stderr, "keyword")
	})

	t.Run("missing required filepath on read", func(t *testing.T) {
		code, stdout, stderr := runCLI([]string{"--root", root, "read"}, "")
		assertCLIError(t, code, stdout, stderr, "filepath")
	})

	t.Run("empty filepath on read", func(t *testing.T) {
		code, stdout, stderr := runCLI([]string{"--root", root, "read", "-f", ""}, "")
		assertCLIError(t, code, stdout, stderr, "filepath")
	})

	t.Run("missing required filepath on edit", func(t *testing.T) {
		code, stdout, stderr := runCLI([]string{"--root", root, "edit"}, "")
		assertCLIError(t, code, stdout, stderr, "filepath")
	})

	t.Run("empty filepath on edit", func(t *testing.T) {
		code, stdout, stderr := runCLI([]string{"--root", root, "edit", "-f", ""}, "")
		assertCLIError(t, code, stdout, stderr, "filepath")
	})

	t.Run("unknown flag", func(t *testing.T) {
		code, stdout, stderr := runCLI([]string{"--root", root, "index", "--bogus"}, "")
		assertCLIError(t, code, stdout, stderr, "unknown flag")
	})

	t.Run("nonexistent root", func(t *testing.T) {
		missing := filepath.Join(root, "does-not-exist")
		code, stdout, stderr := runCLI([]string{"--root", missing, "index"}, "")
		assertCLIError(t, code, stdout, stderr, "project root")
	})

	t.Run("malformed proxer json", func(t *testing.T) {
		badRoot := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(badRoot, config.ConfigFile), []byte(`{"unknown": true}`), 0o644))
		code, stdout, stderr := runCLI([]string{"--root", badRoot, "index"}, "")
		assertCLIError(t, code, stdout, stderr, "project config")
	})

	t.Run("read path outside root", func(t *testing.T) {
		code, stdout, stderr := runCLI([]string{"--root", root, "read", "-f", "../outside.go"}, "")
		assertCLIError(t, code, stdout, stderr, "")
	})

	t.Run("edit stale hash mismatch", func(t *testing.T) {
		code, stdout, stderr := runCLI(
			[]string{"--root", root, "edit", "-f", "main.go", "-s", "1", "-e", "1", "--hash", "0000000000"},
			"package main",
		)
		assertCLIError(t, code, stdout, stderr, "changed since it was read")
	})
}

func TestCLIReadOnReadOnlyRootWithoutDB(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "hello.go"), []byte("package main\nfunc Hello() {}\n"), 0o644))
	require.NoError(t, os.Chmod(root, 0o555))
	defer os.Chmod(root, 0o755)

	code, stdout, stderr := runCLI([]string{"--root", root, "read", "-f", "hello.go", "-s", "1", "-e", "2"}, "")
	readObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assert.Equal(t, "hello.go", unmarshalJSON[string](t, readObj["filepath"]))
	assert.Equal(t, 2, unmarshalJSON[int](t, readObj["total_lines"]))

	_, err := os.Stat(filepath.Join(root, config.DBFile))
	assert.True(t, os.IsNotExist(err), "proxer read must not create .proxer.db")
}

func TestCLIReadInHarnessModeDoesNotTouchSkillDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "hello.go"), []byte("package main\nfunc Hello() {}\n"), 0o644))

	skillDir := t.TempDir()
	t.Setenv(config.SkillDirEnvVar, skillDir)

	code, stdout, stderr := runCLI([]string{"--root", root, "read", "-f", "hello.go", "-s", "1", "-e", "2"}, "")
	readObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assert.Equal(t, "hello.go", unmarshalJSON[string](t, readObj["filepath"]))

	_, err := os.Stat(filepath.Join(skillDir, "database"))
	assert.True(t, os.IsNotExist(err), "proxer read must not create <skillDir>/database")

	if os.Geteuid() == 0 {
		return
	}
	roSkillDir := filepath.Join(t.TempDir(), "ro-skill")
	require.NoError(t, os.MkdirAll(roSkillDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(roSkillDir, 0o700) })
	t.Setenv(config.SkillDirEnvVar, roSkillDir)

	code, stdout, stderr = runCLI([]string{"--root", root, "read", "-f", "hello.go", "-s", "1", "-e", "2"}, "")
	readObj = decodeSingleLineJSON(t, code, stdout, stderr)
	assert.Equal(t, "hello.go", unmarshalJSON[string](t, readObj["filepath"]))
}

func TestCLISearchPathOnlyHitOmitsLine(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "pathonlymatch.txt"), []byte("unrelated body text\n"), 0o644))

	code, stdout, stderr := runCLI([]string{"--root", root, "search", "-k", "pathonlymatch"}, "")
	searchObj := decodeSingleLineJSON(t, code, stdout, stderr)
	results := unmarshalJSON[[]map[string]jsontext.Value](t, searchObj["results"])
	require.Len(t, results, 1)
	assert.Equal(t, "path", unmarshalJSON[string](t, results[0]["match"]))
	_, hasLine := results[0]["line"]
	assert.False(t, hasLine, "path-only search hit must omit line from CLI JSON")
}

func TestCLISensitiveExclusionAndOptInToggle(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET_KEY=topsecret123\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "config", "prod.env"), []byte("SECRET_KEY=prodsecret456\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env.example"), []byte("EXAMPLE_KEY=changeme\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".envoy.yaml"), []byte("envoy_cluster: ok\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secrets.go"), []byte("package main\nfunc LoadSecrets() {}\n"), 0o644))

	code, stdout, stderr := runCLI([]string{"--root", root, "index"}, "")
	idxObj := decodeSingleLineJSON(t, code, stdout, stderr)
	refreshed := unmarshalJSON[map[string]jsontext.Value](t, idxObj["refreshed"])
	assert.Equal(t, 3, unmarshalJSON[int](t, refreshed["added"]))

	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "topsecret123", "--no-refresh"}, "")
	searchObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assert.Equal(t, 0, unmarshalJSON[int](t, searchObj["total"]))

	require.NoError(t, os.WriteFile(filepath.Join(root, config.ConfigFile), []byte(`{"index_sensitive": true}`), 0o644))
	code, stdout, stderr = runCLI([]string{"--root", root, "index"}, "")
	optInIdx := decodeSingleLineJSON(t, code, stdout, stderr)
	assert.True(t, unmarshalJSON[bool](t, optInIdx["index_sensitive"]))

	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "topsecret123", "--no-refresh"}, "")
	optInSearch := decodeSingleLineJSON(t, code, stdout, stderr)
	assert.True(t, unmarshalJSON[bool](t, optInSearch["index_sensitive"]))
	assert.Equal(t, 1, unmarshalJSON[int](t, optInSearch["total"]))

	require.NoError(t, os.WriteFile(filepath.Join(root, config.ConfigFile), []byte(`{"index_sensitive": false}`), 0o644))
	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "topsecret123", "--no-refresh"}, "")
	afterToggleSearch := decodeSingleLineJSON(t, code, stdout, stderr)
	assert.Equal(t, 0, unmarshalJSON[int](t, afterToggleSearch["total"]))
	assert.True(t, unmarshalJSON[bool](t, afterToggleSearch["purge_pending"]))

	code, stdout, stderr = runCLI([]string{"--root", root, "index"}, "")
	scrubIdx := decodeSingleLineJSON(t, code, stdout, stderr)
	_, hasPending := scrubIdx["purge_pending"]
	assert.False(t, hasPending, "Stage 2 index should clear purge_pending")

	dbBytes, err := os.ReadFile(filepath.Join(root, config.DBFile))
	require.NoError(t, err)
	assert.NotContains(t, string(dbBytes), "topsecret123")
}

func TestCLISkipBreakdownAndIgnoredDirsGolden(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".venv"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".venv", "site.py"), []byte("x = 1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.go\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "ignored.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=abc\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc SkipCheck() {}\n"), 0o644))

	code, stdout, stderr := runCLI([]string{"--root", root, "index"}, "")
	idxObj := decodeSingleLineJSON(t, code, stdout, stderr)
	refreshed := unmarshalJSON[map[string]jsontext.Value](t, idxObj["refreshed"])
	assertJSONSchema(t, refreshed, map[string]jsonType{
		"added":             jsonInt,
		"changed":           jsonInt,
		"removed":           jsonInt,
		"skipped":           jsonInt,
		"ignored_dirs":      jsonInt,
		"skipped_by_reason": jsonObject,
	})
	assert.Equal(t, 1, unmarshalJSON[int](t, refreshed["added"]))
	assert.Equal(t, 1, unmarshalJSON[int](t, refreshed["ignored_dirs"]))
	assert.Equal(t, 3, unmarshalJSON[int](t, refreshed["skipped"]))

	reasons := unmarshalJSON[map[string]jsontext.Value](t, refreshed["skipped_by_reason"])
	assertJSONSchema(t, reasons, map[string]jsonType{
		"ignored_files": jsonInt,
		"extension":     jsonInt,
		"sensitive":     jsonInt,
	})
	assert.Equal(t, 1, unmarshalJSON[int](t, reasons["ignored_files"]))
	assert.Equal(t, 1, unmarshalJSON[int](t, reasons["extension"]))
	assert.Equal(t, 1, unmarshalJSON[int](t, reasons["sensitive"]))
}

func TestCLIReadNumberedAndAnchorEdit(t *testing.T) {
	root := setupFixtureRoot(t)

	code, stdout, stderr := runCLI([]string{"--root", root, "read", "-f", "main.go", "-s", "1", "-e", "5", "-n"}, "")
	readObj := decodeSingleLineJSON(t, code, stdout, stderr)
	content := unmarshalJSON[string](t, readObj["content"])
	assert.Contains(t, content, "1\tpackage main\n")
	assert.Contains(t, content, "3\tfunc ComputeAnswer() int {\n")

	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "ComputeAnswer"}, "")
	searchObj := decodeSingleLineJSON(t, code, stdout, stderr)
	results := unmarshalJSON[[]map[string]jsontext.Value](t, searchObj["results"])
	require.NotEmpty(t, results)
	searchHash := unmarshalJSON[string](t, results[0]["hash"])
	require.NotEmpty(t, searchHash)

	// Reject combining --old and --start/--end.
	code, stdout, stderr = runCLI([]string{"--root", root, "edit", "-f", "main.go", "--old", "return 42", "-s", "4"}, "return 7")
	assertCLIError(t, code, stdout, stderr, "cannot combine --old anchor mode with --start/--end line range")

	// Anchor edit directly from search hash.
	code, stdout, stderr = runCLI([]string{"--root", root, "edit", "-f", "main.go", "--old", "\treturn 42", "--hash", searchHash}, "\treturn 77")
	editObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assert.Equal(t, "success", unmarshalJSON[string](t, editObj["status"]))
	assert.NotEmpty(t, unmarshalJSON[string](t, editObj["hash"]))
	assert.NotEqual(t, searchHash, unmarshalJSON[string](t, editObj["hash"]))
}
