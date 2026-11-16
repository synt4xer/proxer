package tools

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/indexer"
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

// SearchOptions carries the two things PerformSearch needs besides the query.
// NoRefresh is opt-out rather than opt-in so the zero value does the safe
// thing: the index is brought up to date, and a caller that must not write asks
// for that explicitly.
type SearchOptions struct {
	NoRefresh   bool
	IgnoredDirs []string
}

// PerformSearch answers a keyword query from the index. Unless the caller opts
// out, it first brings the index in line with the tree, because a harness tool
// that wrote a file is otherwise invisible to proxer and the stale answer is
// indistinguishable from "no match".
func PerformSearch(db *sqlx.DB, root, keyword string, opts SearchOptions) (string, error) {
	response := domain.SearchResponse{
		Root:    root,
		Results: make([]domain.SearchResultJSON, 0),
	}

	if !opts.NoRefresh {
		refreshed, err := indexer.IndexProject(db, root, opts.IgnoredDirs)
		if err != nil {
			return "", fmt.Errorf("failed to refresh the index: %w", err)
		}
		response.Refreshed = &domain.RefreshSummary{
			Added:   refreshed.Added,
			Changed: refreshed.Changed,
			Removed: refreshed.Removed,
		}
	}

	indexedAt, err := database.IndexedAt(db)
	if err != nil {
		return "", fmt.Errorf("failed to read the index timestamp: %w", err)
	}
	response.IndexedAt = indexedAt

	matchQuery, ok := buildMatchQuery(keyword)
	if !ok {
		return encodeJSON(response)
	}

	query := `
		SELECT filepath, start_line, end_line, snippet(chunk_search, 4, '<b>', '</b>', '...', 10) as snippet
		FROM chunk_search
		WHERE content MATCH $1
		ORDER BY rank LIMIT 5
	`

	var dbResults []domain.SearchResultDB
	if err := db.Select(&dbResults, query, matchQuery); err != nil {
		return "", fmt.Errorf("SQLite query error: %w", err)
	}

	for _, r := range dbResults {
		response.Results = append(response.Results, domain.SearchResultJSON{
			Root:     root,
			Filepath: r.Filepath,
			Line:     fmt.Sprintf("%d-%d", r.StartLine, r.EndLine),
			Snippet:  r.Snippet,
		})
	}

	out, err := encodeJSON(response)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return out, nil
}
