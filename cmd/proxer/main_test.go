package main

import (
	"bytes"
	"encoding/json"
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

func decodeSingleLineJSON(t *testing.T, code int, stdout, stderr string) map[string]any {
	t.Helper()

	require.Equal(t, 0, code, "expected exit 0, stderr: %q", stderr)
	assert.Empty(t, stderr)
	require.True(t, strings.HasSuffix(stdout, "\n"), "stdout must end with newline: %q", stdout)

	line := strings.TrimSuffix(stdout, "\n")
	assert.NotContains(t, line, "\n", "stdout must be a single line of JSON")

	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()

	var obj map[string]any
	require.NoError(t, dec.Decode(&obj))
	return obj
}

func assertJSONSchema(t *testing.T, obj map[string]any, want map[string]jsonType) {
	t.Helper()

	assert.Len(t, obj, len(want), "unexpected JSON key set: got %v", obj)
	for key, wantType := range want {
		val, ok := obj[key]
		require.True(t, ok, "missing expected JSON key %q", key)
		assertJSONValueType(t, key, val, wantType)
	}
}

func assertJSONValueType(t *testing.T, key string, val any, wantType jsonType) {
	t.Helper()

	switch wantType {
	case jsonString:
		_, ok := val.(string)
		assert.True(t, ok, "key %q: expected string, got %T (%v)", key, val, val)
	case jsonInt:
		num, ok := val.(json.Number)
		require.True(t, ok, "key %q: expected JSON number, got %T (%v)", key, val, val)
		_, err := strconv.ParseInt(num.String(), 10, 64)
		assert.NoError(t, err, "key %q: expected integer JSON number, got %q", key, num.String())
	case jsonBool:
		_, ok := val.(bool)
		assert.True(t, ok, "key %q: expected bool, got %T (%v)", key, val, val)
	case jsonArray:
		_, ok := val.([]any)
		assert.True(t, ok, "key %q: expected array, got %T (%v)", key, val, val)
	case jsonObject:
		_, ok := val.(map[string]any)
		assert.True(t, ok, "key %q: expected object, got %T (%v)", key, val, val)
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

	// 1. index
	code, stdout, stderr := runCLI([]string{"--root", root, "index"}, "")
	indexObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assertJSONSchema(t, indexObj, map[string]jsonType{
		"status":    jsonString,
		"refreshed": jsonObject,
	})
	assert.Equal(t, "indexed_successfully", indexObj["status"])
	refreshedObj := indexObj["refreshed"].(map[string]any)
	assertJSONSchema(t, refreshedObj, map[string]jsonType{
		"added":   jsonInt,
		"changed": jsonInt,
		"removed": jsonInt,
		"skipped": jsonInt,
	})
	assert.Equal(t, json.Number("2"), refreshedObj["added"])

	// 2. search (with refresh)
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
	assertJSONSchema(t, searchObj["refreshed"].(map[string]any), map[string]jsonType{
		"added":   jsonInt,
		"changed": jsonInt,
		"removed": jsonInt,
		"skipped": jsonInt,
	})
	results := searchObj["results"].([]any)
	require.NotEmpty(t, results)
	firstResult, ok := results[0].(map[string]any)
	require.True(t, ok)
	assertJSONSchema(t, firstResult, map[string]jsonType{
		"root":     jsonString,
		"filepath": jsonString,
		"line":     jsonString,
		"snippet":  jsonString,
		"match":    jsonString,
	})
	assert.Equal(t, "main.go", firstResult["filepath"])

	// 3. read
	code, stdout, stderr = runCLI([]string{"--root", root, "read", "-f", "main.go", "-s", "1", "-e", "5"}, "")
	readObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assertJSONSchema(t, readObj, map[string]jsonType{
		"root":     jsonString,
		"filepath": jsonString,
		"content":  jsonString,
		"hash":     jsonString,
	})
	assert.Equal(t, "main.go", readObj["filepath"])
	assert.Contains(t, readObj["content"].(string), "return 42")
	hash := readObj["hash"].(string)
	require.NotEmpty(t, hash)

	// 4. edit (with --hash and stdin replacement)
	code, stdout, stderr = runCLI(
		[]string{"--root", root, "edit", "-f", "main.go", "-s", "3", "-e", "5", "--hash", hash},
		"func ComputeAnswer() int {\n\treturn 99\n}",
	)
	editObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assertJSONSchema(t, editObj, map[string]jsonType{
		"status":  jsonString,
		"message": jsonString,
	})
	assert.Equal(t, "success", editObj["status"])

	// Verify edit reindexed the file immediately
	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "99", "--no-refresh"}, "")
	afterEditObj := decodeSingleLineJSON(t, code, stdout, stderr)
	assert.Equal(t, json.Number("1"), afterEditObj["total"])
}

func TestCLISearchFlagsAndNoRefresh(t *testing.T) {
	root := setupFixtureRoot(t)

	code, stdout, stderr := runCLI([]string{"--root", root, "index"}, "")
	decodeSingleLineJSON(t, code, stdout, stderr)

	// Add a new file on disk after indexing.
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "extra.go"),
		[]byte("package main\n\nfunc BrandNewSymbol() {}\n"),
		0o644,
	))

	// --no-refresh omits refreshed key and does not see the new file.
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
	assert.Equal(t, json.Number("0"), noRefreshObj["total"])

	// PROXER_NO_REFRESH=1 has the same effect without the flag.
	t.Setenv(config.NoRefreshEnvVar, "1")
	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "BrandNewSymbol"}, "")
	envObj := decodeSingleLineJSON(t, code, stdout, stderr)
	_, hasRefreshed = envObj["refreshed"]
	assert.False(t, hasRefreshed)
	assert.Equal(t, json.Number("0"), envObj["total"])

	// --kind and -n flags filter and bound results.
	code, stdout, stderr = runCLI([]string{"--root", root, "search", "-k", "ComputeAnswer", "--kind", "docs", "-n", "1"}, "")
	docsObj := decodeSingleLineJSON(t, code, stdout, stderr)
	results := docsObj["results"].([]any)
	require.Len(t, results, 1)
	assert.Equal(t, "README.md", results[0].(map[string]any)["filepath"])
}

func TestCLIErrorPaths(t *testing.T) {
	root := setupFixtureRoot(t)

	// Index once so DB exists for read/edit error tests.
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
