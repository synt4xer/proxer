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

const (
	// defaultLimit is the context guard: five results locate a symbol and stay
	// cheap enough to run on every question.
	defaultLimit = 5
	// maxLimit bounds what a caller may ask for. A wider answer stops being a
	// locator and becomes the raw scan the tool exists to replace.
	maxLimit = 50
	// candidateWindowFactor widens the ranked window beyond the requested
	// limit, so dedupe and demotion still have candidates left once the cap
	// applies. Ranking cost is dominated by the match, not the width, so the
	// wider window is nearly free.
	candidateWindowFactor = 40
	minCandidateWindow    = 50
	maxCandidateWindow    = 200
	// pathWeight is the bm25 weight of the filepath column. A token that names
	// a file outranks the same token sitting in the middle of a chunk, which is
	// what lets "000001_init" find a migration whose path is its only identity.
	pathWeight = 5
)

// clampLimit keeps a caller-supplied limit inside the range the tool promises.
// Zero and negatives are the "unset" case, not an error: both interfaces treat
// a missing limit as the default.
func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	}
	return limit
}

// candidateWindow is how many ranked chunks the query fetches for limit results
// before the cap applies. Forty times the limit, floored at 50 rows so a small
// limit still has room to dedupe, and capped at 200 because the window stops
// paying for itself: at 65,800 chunks a 200-row window costs 53 ms against
// 55 ms for 500.
func candidateWindow(limit int) int {
	window := limit * candidateWindowFactor
	if window < minCandidateWindow {
		return minCandidateWindow
	}
	if window > maxCandidateWindow {
		return maxCandidateWindow
	}
	return window
}

// searchTerms splits free-form input into the terms the query will use. It is
// separate from buildMatchQuery because the caller also tests the terms
// against a result's path, and both have to agree on what a term is.
func searchTerms(keyword string) []string {
	return strings.FieldsFunc(keyword, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

// buildMatchQuery converts free-form agent input into a valid FTS5 expression.
// Every term is wrapped in double quotes so operators, parentheses, and stray
// punctuation from natural language cannot reach the FTS5 parser as syntax.
// Each term is also a prefix query, because agents search for fragments like
// "auth" and expect to reach identifiers such as "AuthMiddleware".
// Empty input is reported through the boolean instead of an erroring query.
func buildMatchQuery(keyword string) (string, bool) {
	terms := searchTerms(keyword)

	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"*`)
	}

	if len(quoted) == 0 {
		return "", false
	}
	return strings.Join(quoted, " OR "), true
}

// SearchOptions carries what PerformSearch needs besides the query. NoRefresh
// is opt-out rather than opt-in so the zero value does the safe thing: the index
// is brought up to date, and a caller that must not write asks for that
// explicitly. Limit is clamped rather than validated, so a caller that omits it
// gets the default instead of an error.
type SearchOptions struct {
	NoRefresh   bool
	IgnoredDirs []string
	Limit       int
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

	limit := clampLimit(opts.Limit)

	// total is the honest measure of how much matched, so a capped answer can
	// say so. It counts chunks, not files: count(DISTINCT filepath) costs
	// 233 ms at 65,800 chunks against 5 ms for this one.
	total, err := matchTotal(db, matchQuery)
	if err != nil {
		return "", err
	}
	response.Total = total

	candidates, err := rankedCandidates(db, matchQuery, candidateWindow(limit))
	if err != nil {
		return "", err
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	response.Truncated = total > len(candidates)

	terms := searchTerms(keyword)
	for _, r := range candidates {
		response.Results = append(response.Results, domain.SearchResultJSON{
			Root:     root,
			Filepath: r.Filepath,
			Line:     fmt.Sprintf("%d-%d", r.StartLine, r.EndLine),
			Snippet:  r.Snippet,
			Match:    matchPart(terms, r.Filepath),
		})
	}

	out, err := encodeJSON(response)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return out, nil
}

// rankedCandidates fetches the best-ranked chunks for matchQuery. The match is
// applied to the whole table rather than to the content column, so a token that
// appears only in a file's path is found at all, and bm25() weights the path
// above the body. chunk_search columns are (id, filepath, start_line, end_line,
// content), which is why pathWeight sits second.
func rankedCandidates(db *sqlx.DB, matchQuery string, window int) ([]domain.SearchResultDB, error) {
	query := fmt.Sprintf(`
		SELECT filepath, start_line, end_line, snippet(chunk_search, 4, '<b>', '</b>', '...', 10) as snippet
		FROM chunk_search
		WHERE chunk_search MATCH ?
		ORDER BY bm25(chunk_search, 0, %d, 0, 0, 1)
		LIMIT ?
	`, pathWeight)

	var results []domain.SearchResultDB
	if err := db.Select(&results, query, matchQuery, window); err != nil {
		return nil, fmt.Errorf("SQLite query error: %w", err)
	}
	return results, nil
}

// matchTotal counts every chunk the match reaches, which is what makes a capped
// result set visible to the caller.
func matchTotal(db *sqlx.DB, matchQuery string) (int, error) {
	var total int
	if err := db.Get(&total, "SELECT count(*) FROM chunk_search WHERE chunk_search MATCH ?", matchQuery); err != nil {
		return 0, fmt.Errorf("SQLite count error: %w", err)
	}
	return total, nil
}

// matchPart reports where the terms landed: in the file's path, in its content,
// or in both. FTS5 returns the row without saying which column matched, so the
// terms are tested against the path in Go instead of asking a second question.
// A term that is not in the path did not match there; since terms are OR-ed it
// either reached the content or matched nothing, and "content" is the honest
// answer to give a caller that only sees the match.
func matchPart(terms []string, filePath string) string {
	path := strings.ToLower(filePath)
	pathHit, contentHit := false, false
	for _, term := range terms {
		if strings.Contains(path, strings.ToLower(term)) {
			pathHit = true
		} else {
			contentHit = true
		}
	}

	switch {
	case pathHit && contentHit:
		return "both"
	case pathHit:
		return "path"
	default:
		return "content"
	}
}
