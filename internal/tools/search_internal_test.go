package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSearchTermsAndMatchPart(t *testing.T) {
	terms := searchTerms("where is auth (middleware)?")
	assert.Equal(t, []string{"where", "is", "auth", "middleware"}, terms)

	tests := []struct {
		name     string
		terms    []string
		filePath string
		expected string
	}{
		{
			name:     "a token that only names the file is a path hit",
			terms:    []string{"000001"},
			filePath: "db/migrations/mysql/000001_init.up.sql",
			expected: "path",
		},
		{
			name:     "a token that is not in the path matched the body",
			terms:    []string{"create"},
			filePath: "db/migrations/mysql/000001_init.up.sql",
			expected: "content",
		},
		{
			name:     "one term in the path and one in the body is both",
			terms:    []string{"mysql", "create"},
			filePath: "db/migrations/mysql/000001_init.up.sql",
			expected: "both",
		},
		{
			name:     "path matching is case-insensitive",
			terms:    []string{"MYSQL"},
			filePath: "db/migrations/mysql/000001_init.up.sql",
			expected: "path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, matchPart(tt.terms, tt.filePath))
		})
	}
}

func TestBuildMatchQuery(t *testing.T) {
	tests := []struct {
		name     string
		keyword  string
		expected string
		ok       bool
	}{
		{
			name:     "single identifier",
			keyword:  "authMiddleware",
			expected: `"authMiddleware"*`,
			ok:       true,
		},
		{
			name:     "underscore survives tokenization",
			keyword:  "auth_middleware",
			expected: `"auth_middleware"*`,
			ok:       true,
		},
		{
			name:     "FTS5 operators are neutralized",
			keyword:  `Auth( AND * NOT`,
			expected: `"Auth"* OR "AND"* OR "NOT"*`,
			ok:       true,
		},
		{
			name:     "natural language question",
			keyword:  "where is auth (middleware)?",
			expected: `"where"* OR "is"* OR "auth"* OR "middleware"*`,
			ok:       true,
		},
		{
			name:     "double quotes are escaped",
			keyword:  `say "hello"`,
			expected: `"say"* OR "hello"*`,
			ok:       true,
		},
		{
			name:    "punctuation only is rejected",
			keyword: "()*&^%$#@!",
			ok:      false,
		},
		{
			name:    "empty input is rejected",
			keyword: "   ",
			ok:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := buildMatchQuery(tt.keyword)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestClampLimitAndCandidateWindow(t *testing.T) {
	tests := []struct {
		limit  int
		clamp  int
		window int
	}{
		{limit: 0, clamp: defaultLimit, window: 200},
		{limit: -3, clamp: defaultLimit, window: 200},
		{limit: 1, clamp: 1, window: minCandidateWindow},
		{limit: 5, clamp: 5, window: 200},
		{limit: 20, clamp: 20, window: 200},
		{limit: maxLimit, clamp: maxLimit, window: maxCandidateWindow},
		{limit: maxLimit + 100, clamp: maxLimit, window: maxCandidateWindow},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.clamp, clampLimit(tt.limit), "limit %d", tt.limit)
		assert.Equal(t, tt.window, candidateWindow(clampLimit(tt.limit)), "limit %d", tt.limit)
	}
}
