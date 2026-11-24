package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
)

// fileKind is the ranking class of a path. A query for a common token matches
// documentation and minified assets as readily as code, and the agent almost
// always wants the code first, so the kind decides the order rather than the
// SQL: demoting in SQL measured 20 ms to 78 ms more than the whole unfiltered
// query.
type fileKind int

const (
	kindCode fileKind = iota
	kindTest
	kindDocs
	kindAssets
)

// kindAll keeps every kind in the demotion order: production code, then tests,
// then docs, then assets. It is a filter value rather than a fileKind because
// no file is "all kinds".
const kindAll = fileKind(-1)

// assetLockfiles are dependency locks: generated, large, and never what a code
// question is asking for.
var assetLockfiles = map[string]struct{}{
	"package-lock.json": {},
	"pnpm-lock.yaml":    {},
	"yarn.lock":         {},
	"bun.lockb":         {},
	"go.sum":            {},
	"cargo.lock":        {},
	"composer.lock":     {},
	"gemfile.lock":      {},
	"poetry.lock":       {},
	"uv.lock":           {},
	"podfile.lock":      {},
	"flake.lock":        {},
}

// classifyPath sorts a result path into the demotion order. It reads the path
// only, because the body is not available at query time and the path rules
// already cover the file the field report hit (static/htmx.min.js) as well as
// test files that would otherwise outrank the symbol they test.
func classifyPath(filePath string) fileKind {
	rawBase := filepath.Base(filePath)
	base := strings.ToLower(rawBase)

	if strings.Contains(base, ".min.") || filepath.Ext(base) == ".map" {
		return kindAssets
	}
	if _, ok := assetLockfiles[base]; ok {
		return kindAssets
	}
	if kind, ok := config.AllowedExactNames[rawBase]; ok && kind == config.KindDocs {
		return kindDocs
	}
	if kind, ok := config.AllowedExtensions[strings.ToLower(filepath.Ext(base))]; ok && kind == config.KindDocs {
		return kindDocs
	}
	if isTestPath(filePath, base) {
		return kindTest
	}
	return kindCode
}

// isTestPath reports whether a non-asset, non-doc file is a test file so that
// production definitions lead before test call sites.
func isTestPath(filePath, lowerBase string) bool {
	if strings.HasSuffix(lowerBase, "_test.go") ||
		strings.HasSuffix(lowerBase, "_test.py") ||
		strings.HasSuffix(lowerBase, "_test.rs") ||
		strings.HasSuffix(lowerBase, "_spec.rb") ||
		(strings.HasPrefix(lowerBase, "test_") && strings.HasSuffix(lowerBase, ".py")) {
		return true
	}
	if strings.Contains(lowerBase, ".test.") || strings.Contains(lowerBase, ".spec.") {
		return true
	}
	slashPath := "/" + filepath.ToSlash(strings.ToLower(filePath))
	return strings.Contains(slashPath, "/__tests__/")
}

// parseKind turns a caller's --kind value into a filter. An empty value is the
// default, not an error, so both interfaces can leave the flag out.
func parseKind(value string) (fileKind, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return kindAll, nil
	case "code":
		return kindCode, nil
	case "docs":
		return kindDocs, nil
	default:
		return kindAll, fmt.Errorf("invalid kind %q: expected code, docs, or all", value)
	}
}

// docsPathMatch constrains an FTS5 expression to the paths of documentation.
// Derived from config.AllowedExtensions so the pre-filter and registry cannot
// drift. The filter is a coarse pre-filter for the query; exact classification
// still happens in Go, where a path with a doc token in a directory name is
// caught.
var docsPathMatch = buildDocsPathMatch()

func buildDocsPathMatch() string {
	exts := make([]string, 0, 4)
	for ext, kind := range config.AllowedExtensions {
		if kind == config.KindDocs {
			exts = append(exts, strings.TrimPrefix(ext, "."))
		}
	}
	sort.Strings(exts)
	clauses := make([]string, len(exts))
	for i, ext := range exts {
		clauses[i] = "filepath:" + ext
	}
	return "(" + strings.Join(clauses, " OR ") + ")"
}

func restrictToDocs(matchQuery string) string {
	return "(" + matchQuery + ") AND " + docsPathMatch
}

// excludeDocs narrows a match to everything that is not documentation. It is
// what makes an explicit --kind code honest when documentation outranks code,
// and what recovers code from a window that documentation filled.
func excludeDocs(matchQuery string) string {
	return "(" + matchQuery + ") NOT " + docsPathMatch
}

func partitionByKind(candidates []domain.SearchResultDB) [4][]domain.SearchResultDB {
	var groups [4][]domain.SearchResultDB
	for _, candidate := range candidates {
		k := classifyPath(candidate.Filepath)
		groups[k] = append(groups[k], candidate)
	}
	return groups
}

func kindMultiplier(kind fileKind) float64 {
	switch kind {
	case kindCode:
		return 1.0
	case kindTest:
		return 0.6
	case kindDocs:
		return 0.35
	case kindAssets:
		return 0.1
	default:
		return 1.0
	}
}

func sortBySoftKindScore(candidates []domain.SearchResultDB) []domain.SearchResultDB {
	ordered := make([]domain.SearchResultDB, len(candidates))
	copy(ordered, candidates)
	sort.SliceStable(ordered, func(i, j int) bool {
		ki := classifyPath(ordered[i].Filepath)
		kj := classifyPath(ordered[j].Filepath)
		si := ordered[i].Score * kindMultiplier(ki)
		sj := ordered[j].Score * kindMultiplier(kj)
		if si != sj {
			return si < sj
		}
		return ki < kj
	})
	return ordered
}

// filterKind drops every candidate that is not of kind. When kind is kindCode,
// both production code and test files are kept and ranked with soft kind
// multipliers so an explicit --kind code still finds tests while preferring
// production definitions on equal matches.
func filterKind(candidates []domain.SearchResultDB, kind fileKind) []domain.SearchResultDB {
	if kind == kindCode {
		kept := make([]domain.SearchResultDB, 0, len(candidates))
		for _, candidate := range candidates {
			k := classifyPath(candidate.Filepath)
			if k == kindCode || k == kindTest {
				kept = append(kept, candidate)
			}
		}
		return sortBySoftKindScore(kept)
	}
	kept := make([]domain.SearchResultDB, 0, len(candidates))
	for _, candidate := range candidates {
		if classifyPath(candidate.Filepath) == kind {
			kept = append(kept, candidate)
		}
	}
	return kept
}

var (
	importLineRe = regexp.MustCompile(`^\s*(?:import\b|from\s+\S+\s+import\b|require\(|use\s+|#include\b)`)
	defLineRe    = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:func|def|class|type|interface|struct|fn|impl|fun|object|protocol|enum|const|let|var)\b`)
)

const (
	importDemoteFactor = 0.65
	defBoostFactor     = 1.35
)

// applySymbolScoring adjusts negative BM25 scores in-place for code and test
// chunks so declarations outrank import blocks before per-file deduplication.
func applySymbolScoring(candidates []domain.SearchResultDB, terms []string) {
	if len(candidates) == 0 || len(terms) == 0 {
		return
	}
	lowerTerms := make([][]rune, 0, len(terms))
	for _, t := range terms {
		if t != "" {
			lowerTerms = append(lowerTerms, []rune(strings.ToLower(t)))
		}
	}
	if len(lowerTerms) == 0 {
		return
	}

	for i := range candidates {
		k := classifyPath(candidates[i].Filepath)
		if k != kindCode && k != kindTest {
			continue
		}
		matchedLines := 0
		importLines := 0
		hasDef := false
		for line := range strings.SplitSeq(candidates[i].Content, "\n") {
			if firstTermRuneIndex(line, lowerTerms) < 0 {
				continue
			}
			matchedLines++
			if importLineRe.MatchString(line) {
				importLines++
			} else if defLineRe.MatchString(line) {
				hasDef = true
			}
		}
		if matchedLines == 0 {
			continue
		}
		if importLines*2 >= matchedLines {
			candidates[i].Score *= importDemoteFactor
		} else if hasDef {
			candidates[i].Score *= defBoostFactor
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score < candidates[j].Score
	})
}

// orderByKind filters and orders candidates using soft BM25 multipliers
// (Code 1.0x, Test 0.6x, Docs 0.35x, Assets 0.1x) when kindAll is used, so
// strong exact matches in tests or docs can outrank weak 1-token code noise
// while preserving code-first preference on equal matches.
func orderByKind(candidates []domain.SearchResultDB, kind fileKind) []domain.SearchResultDB {
	if kind != kindAll {
		return filterKind(candidates, kind)
	}
	return sortBySoftKindScore(candidates)
}
