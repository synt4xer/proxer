package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
