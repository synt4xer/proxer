package tools

import (
	"fmt"
	"path/filepath"
	"strings"

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
	kindDocs
	kindAssets
)

// kindAll keeps every kind in the demotion order: code, then docs, then assets.
// It is a filter value rather than a fileKind because no file is "all kinds".
const kindAll = fileKind(-1)

var docExtensions = map[string]struct{}{
	".md":  {},
	".mdx": {},
	".txt": {},
	".rst": {},
}

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
// already cover the file the field report hit (static/htmx.min.js).
func classifyPath(filePath string) fileKind {
	base := strings.ToLower(filepath.Base(filePath))

	if strings.Contains(base, ".min.") || filepath.Ext(base) == ".map" {
		return kindAssets
	}
	if _, ok := assetLockfiles[base]; ok {
		return kindAssets
	}
	if _, ok := docExtensions[strings.ToLower(filepath.Ext(base))]; ok {
		return kindDocs
	}
	return kindCode
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

// orderByKind filters and orders candidates so the scarce slots describe code
// before documentation and documentation before generated assets. With kindAll
// every group survives, in that order; with a specific kind the others are
// dropped, which is the escape hatch for a question that really is about docs.
func orderByKind(candidates []domain.SearchResultDB, kind fileKind) []domain.SearchResultDB {
	if kind != kindAll {
		kept := candidates[:0]
		for _, candidate := range candidates {
			if classifyPath(candidate.Filepath) == kind {
				kept = append(kept, candidate)
			}
		}
		return kept
	}

	var groups [3][]domain.SearchResultDB
	for _, candidate := range candidates {
		k := classifyPath(candidate.Filepath)
		groups[k] = append(groups[k], candidate)
	}

	ordered := make([]domain.SearchResultDB, 0, len(candidates))
	for _, group := range groups {
		ordered = append(ordered, group...)
	}
	return ordered
}
