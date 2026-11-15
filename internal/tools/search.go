package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/internal/domain"
)

// buildMatchQuery converts free-form agent input into a valid FTS5 expression.
// Every term is wrapped in double quotes so operators, parentheses, and stray
// punctuation from natural language cannot reach the FTS5 parser as syntax.
// Each term is also a prefix query, because agents search for fragments like
// "auth" and expect to reach identifiers such as "AuthMiddleware".
// Empty input is reported through the boolean instead of an erroring query.
func buildMatchQuery(keyword string) (string, bool) {
	terms := strings.FieldsFunc(keyword, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})

	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"*`)
	}

	if len(quoted) == 0 {
		return "", false
	}
	return strings.Join(quoted, " OR "), true
}

func PerformSearch(db *sqlx.DB, root, keyword string) (string, error) {
	matchQuery, ok := buildMatchQuery(keyword)
	if !ok {
		return "[]", nil
	}

	query := `
		SELECT filepath, start_line, end_line, snippet(chunk_search, 4, '<b>', '</b>', '...', 10) as snippet
		FROM chunk_search
		WHERE content MATCH $1
		ORDER BY rank LIMIT 5
	`

	var dbResults []domain.SearchResultDB
	err := db.Select(&dbResults, query, matchQuery)
	if err != nil {
		return "", fmt.Errorf("SQLite query error: %w", err)
	}

	if len(dbResults) == 0 {
		return "[]", nil
	}

	var jsonResults []domain.SearchResultJSON
	for _, r := range dbResults {
		jsonResults = append(jsonResults, domain.SearchResultJSON{
			Root:     root,
			Filepath: r.Filepath,
			Line:     fmt.Sprintf("%d-%d", r.StartLine, r.EndLine),
			Snippet:  r.Snippet,
		})
	}

	out, err := json.Marshal(jsonResults)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return string(out), nil
}
