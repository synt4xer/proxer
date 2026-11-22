package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitIdentifier(t *testing.T) {
	tests := []struct {
		ident    string
		expected []string
	}{
		{ident: "AuthMiddleware", expected: []string{"auth", "middleware"}},
		{ident: "HTTPServer", expected: []string{"http", "server"}},
		{ident: "sqlRepo", expected: []string{"sql", "repo"}},
		{ident: "v2", expected: []string{"v2"}},
		{ident: "user_id", expected: []string{"user", "id"}},
		{ident: "newHTTPServer", expected: []string{"new", "http", "server"}},
		{ident: "v2Handler", expected: []string{"v2", "handler"}},
		{ident: "CONST_VALUE", expected: []string{"const", "value"}},
		{ident: "", expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.ident, func(t *testing.T) {
			assert.Equal(t, tt.expected, SplitIdentifier(tt.ident))
		})
	}
}

func TestIdentifierTails(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "pascal and camel case emit tails only",
			content:  "func AuthMiddleware(r sqlRepo) *HTTPServer { return nil }",
			expected: "middleware repo server",
		},
		{
			name:     "deduplicates repeated tails within a chunk",
			content:  "sqlRepo.Save(sqlRepo.Load()) // AuthMiddleware AuthMiddleware",
			expected: "repo middleware",
		},
		{
			name:     "plain snake_case and single-case words emit nothing",
			content:  "user_id := get_user(v2, HTTP, plain)",
			expected: "",
		},
		{
			name:     "mixed snake_case and camelCase extracts camel tail",
			content:  "my_sqlRepo_v2",
			expected: "repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, identifierTails(tt.content))
		})
	}
}
