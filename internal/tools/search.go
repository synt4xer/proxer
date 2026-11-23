package tools

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/jmoiron/sqlx"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/filesystem"
	"github.com/synt4xer/proxer/internal/indexer"
)

const (
	// defaultLimit is the context guard: five results locate a symbol and stay
	// cheap enough to run on every question.
	defaultLimit = 5
	// maxLimit bounds what a caller may ask for. A wider answer stops being a
	// locator and becomes the raw scan the tool exists to replace.
	maxLimit = 50
	// defaultMaxPerFile keeps search slots focused on distinct files by default.
	defaultMaxPerFile = 1
	// maxChunksPerFile caps how many non-overlapping chunks one file can take.
	maxChunksPerFile = 5
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

func clampMaxPerFile(maxPerFile int) int {
	switch {
	case maxPerFile <= 0:
		return defaultMaxPerFile
	case maxPerFile > maxChunksPerFile:
		return maxChunksPerFile
	}
	return maxPerFile
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

// buildMatchQuery converts free-form agent input into an OR FTS5 expression.
func buildMatchQuery(keyword string) (string, bool) {
	_, orQuery, _, ok := buildMatchQueries(keyword)
	return orQuery, ok
}

// buildMatchQueries converts free-form agent input into valid FTS5 AND/OR
// expressions. Every term is wrapped in double quotes so operators, parentheses,
// and stray punctuation from natural language cannot reach the FTS5 parser as
// syntax. Each term is also a prefix query, because agents search for fragments
// like "auth" and expect to reach identifiers such as "AuthMiddleware".
func buildMatchQueries(keyword string) (andQuery, orQuery string, multiTerm bool, ok bool) {
	terms := searchTerms(keyword)

	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"*`)
	}

	if len(quoted) == 0 {
		return "", "", false, false
	}
	if len(quoted) == 1 {
		return quoted[0], quoted[0], false, true
	}
	return strings.Join(quoted, " AND "), strings.Join(quoted, " OR "), true, true
}

// SearchOptions carries what PerformSearch needs besides the query. NoRefresh
// is opt-out rather than opt-in so the zero value does the safe thing: the index
// is brought up to date, and a caller that must not write asks for that
// explicitly. Limit is clamped rather than validated, so a caller that omits it
// gets the default instead of an error. Kind filters and orders the results by
// path class: the empty value keeps all of them, code first.
type SearchOptions struct {
	NoRefresh      bool
	IgnoredDirs    []string
	IndexSensitive bool
	Limit          int
	Kind           string
	MaxPerFile     int
}

// PerformSearch answers a keyword query from the index. Unless the caller opts
// out, it first brings the index in line with the tree, because a harness tool
// that wrote a file is otherwise invisible to proxer and the stale answer is
// indistinguishable from "no match".
func PerformSearch(db *sqlx.DB, root, keyword string, opts SearchOptions) (domain.SearchResponse, error) {
	// The kind is validated before the refresh so a typo costs a parse, not a
	// walk over the tree.
	kind, err := parseKind(opts.Kind)
	if err != nil {
		return domain.SearchResponse{}, err
	}

	response := domain.SearchResponse{
		Root:           root,
		IndexSensitive: opts.IndexSensitive,
		Results:        make([]domain.SearchResultJSON, 0),
	}

	if !opts.NoRefresh {
		refreshed, err := indexer.IndexProjectWithOptions(db, root, indexer.IndexOptions{
			IgnoredDirs:    opts.IgnoredDirs,
			IndexSensitive: opts.IndexSensitive,
		})
		if err != nil {
			return domain.SearchResponse{}, fmt.Errorf("failed to refresh the index: %w", err)
		}
		response.Refreshed = &domain.RefreshSummary{
			Added:   refreshed.Added,
			Changed: refreshed.Changed,
			Removed: refreshed.Removed,
			Skipped: refreshed.Skipped,
		}
	}

	indexedAt, err := database.IndexedAt(db)
	if err != nil {
		return domain.SearchResponse{}, fmt.Errorf("failed to read the index timestamp: %w", err)
	}
	response.IndexedAt = indexedAt

	purgePending, err := database.PurgePending(db)
	if err != nil {
		return domain.SearchResponse{}, fmt.Errorf("failed to read purge state: %w", err)
	}
	response.PurgePending = purgePending

	andQuery, orQuery, multiTerm, ok := buildMatchQueries(keyword)
	if !ok {
		return response, nil
	}

	limit := clampLimit(opts.Limit)
	maxPerFile := clampMaxPerFile(opts.MaxPerFile)
	window := candidateWindow(limit)

	candidates, total, filteredSensitive, err := fetchCandidates(db, andQuery, orQuery, multiTerm, kind, limit, maxPerFile, window, opts.IndexSensitive)
	if err != nil {
		return domain.SearchResponse{}, err
	}

	eligible := candidates

	// Demotion can only reorder what the window contains. Fifty small files
	// that mention a token densely outrank one mention inside a large source
	// file, so a full window can hold no code at all. Ask once more with
	// documentation excluded; on a tree where the kinds are interleaved the
	// ordinary query already answered and this costs nothing.
	if kind == kindAll && total >= window {
		parts := partitionByKind(eligible)
		if len(parts[kindCode]) == 0 && len(parts[kindTest]) == 0 {
			hidden, err := rankedCandidates(db, excludeDocs(orQuery), window)
			if err != nil {
				return domain.SearchResponse{}, err
			}
			hidden, _ = filterSensitiveCandidates(hidden, opts.IndexSensitive)
			eligible = append(filterKind(dedupeByFile(hidden, maxPerFile), kindCode), eligible...)
		}
	}

	if filteredSensitive > 0 {
		adjusted := total - filteredSensitive
		if adjusted < len(eligible) {
			adjusted = len(eligible)
		}
		total = adjusted
		response.FilteredSensitive = filteredSensitive
	}
	response.Total = total

	// A window that was cut short may hide files, and a slice that was capped
	// hides some it already found. Either way the caller is not seeing the
	// whole match, and truncated is the field that says so.
	response.Truncated = len(eligible) > limit || total > window
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}

	terms := searchTerms(keyword)
	for _, r := range eligible {
		response.Results = append(response.Results, domain.SearchResultJSON{
			Root:     root,
			Filepath: r.Filepath,
			Line:     fmt.Sprintf("%d-%d", r.StartLine, r.EndLine),
			Snippet:  r.Snippet,
			Match:    matchPart(terms, r.Filepath, r.Content),
		})
	}

	return response, nil
}

func applyKindQuery(matchQuery string, kind fileKind) string {
	switch kind {
	case kindCode:
		return excludeDocs(matchQuery)
	case kindDocs:
		return restrictToDocs(matchQuery)
	default:
		return matchQuery
	}
}

// fetchCandidates runs AND-first for multi-term queries so co-occurring matches
// rank first without single-token noise flooding, falling back to OR only when
// AND returns fewer unique files than limit.
func fetchCandidates(db *sqlx.DB, andQuery, orQuery string, multiTerm bool, kind fileKind, limit, maxPerFile, window int, indexSensitive bool) ([]domain.SearchResultDB, int, int, error) {
	if !multiTerm {
		candidates, err := rankedCandidates(db, applyKindQuery(orQuery, kind), window)
		if err != nil {
			return nil, 0, 0, err
		}
		total := 0
		if len(candidates) > 0 {
			total = candidates[0].Total
		}
		candidates, filtered := filterSensitiveCandidates(candidates, indexSensitive)
		return orderByKind(dedupeByFile(candidates, maxPerFile), kind), total, filtered, nil
	}

	andCandidates, err := rankedCandidates(db, applyKindQuery(andQuery, kind), window)
	if err != nil {
		return nil, 0, 0, err
	}
	andTotal := 0
	if len(andCandidates) > 0 {
		andTotal = andCandidates[0].Total
	}
	andCandidates, andFiltered := filterSensitiveCandidates(andCandidates, indexSensitive)
	andOrdered := orderByKind(dedupeByFile(andCandidates, maxPerFile), kind)
	if len(andOrdered) >= limit {
		return andOrdered, andTotal, andFiltered, nil
	}

	orCandidates, err := rankedCandidates(db, applyKindQuery(orQuery, kind), window)
	if err != nil {
		return nil, 0, 0, err
	}
	total := 0
	if len(orCandidates) > 0 {
		total = orCandidates[0].Total
	}
	orCandidates, orFiltered := filterSensitiveCandidates(orCandidates, indexSensitive)
	orOrdered := orderByKind(dedupeByFile(orCandidates, maxPerFile), kind)
	if len(andOrdered) == 0 {
		return orOrdered, total, orFiltered, nil
	}

	kept := make(map[string][]domain.SearchResultDB, len(andOrdered))
	for _, r := range andOrdered {
		kept[r.Filepath] = append(kept[r.Filepath], r)
	}
	merged := make([]domain.SearchResultDB, len(andOrdered), len(andOrdered)+len(orOrdered))
	copy(merged, andOrdered)
	for _, r := range orOrdered {
		if !canAcceptChunk(kept[r.Filepath], r, maxPerFile) {
			continue
		}
		kept[r.Filepath] = append(kept[r.Filepath], r)
		merged = append(merged, r)
	}
	return merged, total, orFiltered, nil
}

func filterSensitiveCandidates(candidates []domain.SearchResultDB, indexSensitive bool) ([]domain.SearchResultDB, int) {
	if indexSensitive || len(candidates) == 0 {
		return candidates, 0
	}
	kept := make([]domain.SearchResultDB, 0, len(candidates))
	filtered := 0
	for _, c := range candidates {
		if filesystem.IsSensitivePath(c.Filepath) {
			filtered++
			continue
		}
		kept = append(kept, c)
	}
	return kept, filtered
}

// rankedCandidates fetches the best-ranked chunks for matchQuery. The match is
// applied to the whole table rather than to the content column, so a token that
// appears only in a file's path or identifier tails is found at all, and bm25()
// weights the path above the body and exact body tokens above sub-token tails.
// chunk_search columns are (id, filepath, start_line, end_line, content, tails),
// which is why pathWeight sits second.
func rankedCandidates(db *sqlx.DB, matchQuery string, window int) ([]domain.SearchResultDB, error) {
	query := fmt.Sprintf(`
		WITH matched AS (
			SELECT filepath, start_line, end_line, content,
			       snippet(chunk_search, 4, '<b>', '</b>', '...', 10) AS snippet,
			       bm25(chunk_search, 0, %d, 0, 0, 1, 0.5) AS score
			FROM chunk_search
			WHERE chunk_search MATCH ?
		)
		SELECT filepath, start_line, end_line, content, snippet, score, count(*) OVER() AS total
		FROM matched
		ORDER BY score
		LIMIT ?
	`, pathWeight)

	var results []domain.SearchResultDB
	if err := db.Select(&results, query, matchQuery, window); err != nil {
		return nil, fmt.Errorf("SQLite query error: %w", err)
	}
	return results, nil
}

// dedupeByFile keeps up to maxPerFile non-overlapping best-ranked chunks per
// file. The query is already ordered by bm25(), so the earliest chunks seen
// for a path are the highest-ranked ones, and requiring non-overlapping line
// ranges avoids returning two adjacent slices of the same hit.
func dedupeByFile(candidates []domain.SearchResultDB, maxPerFile int) []domain.SearchResultDB {
	kept := make(map[string][]domain.SearchResultDB, len(candidates))
	unique := candidates[:0]
	for _, candidate := range candidates {
		if !canAcceptChunk(kept[candidate.Filepath], candidate, maxPerFile) {
			continue
		}
		kept[candidate.Filepath] = append(kept[candidate.Filepath], candidate)
		unique = append(unique, candidate)
	}
	return unique
}

func canAcceptChunk(existing []domain.SearchResultDB, candidate domain.SearchResultDB, maxPerFile int) bool {
	if len(existing) >= maxPerFile {
		return false
	}
	for _, prev := range existing {
		if candidate.StartLine <= prev.EndLine && candidate.EndLine >= prev.StartLine {
			return false
		}
	}
	return true
}

// matchPart reports where the terms landed: in the file's path, in its content,
// or in both. Checking both the path and the chunk content avoids mislabeling
// multi-term OR queries where an unmatched term is absent from the path.
func matchPart(terms []string, filePath, content string) string {
	lowerPath := strings.ToLower(filePath)
	lowerContent := strings.ToLower(content)
	pathHit, contentHit := false, false
	for _, term := range terms {
		lowerTerm := strings.ToLower(term)
		if strings.Contains(lowerPath, lowerTerm) {
			pathHit = true
		}
		if strings.Contains(lowerContent, lowerTerm) {
			contentHit = true
		}
		if pathHit && contentHit {
			return "both"
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
