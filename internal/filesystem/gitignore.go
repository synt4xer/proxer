package filesystem

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

type ignoreRule struct {
	pattern            string
	segments           []string
	negated            bool
	dirOnly            bool
	anchored           bool
	trailingDoubleStar bool
}

// IgnoreMatcher evaluates layered ignore rules for a project root:
//  1. .proxer.json ignored_dirs + DefaultIgnoredDirs (exact names and globs
//     like *.egg-info, pruning directories before descending).
//  2. .git/info/exclude and root/nested .gitignore rules in standard git order
//     (root to nested, last matching rule wins).
//
// Each .gitignore and .git/info/exclude file is read and compiled at most once
// per IgnoreMatcher instance.
type IgnoreMatcher struct {
	root             string
	exactIgnoredDirs map[string]struct{}
	globIgnoredDirs  []string
	excludeRules     []ignoreRule
	dirRules         map[string][]ignoreRule
	hasGitRules      bool
}

// NewIgnoreMatcher creates a matcher rooted at root with the effective
// ignoredDirs list (.proxer.json merged with DefaultIgnoredDirs).
func NewIgnoreMatcher(root string, ignoredDirs []string) *IgnoreMatcher {
	m := &IgnoreMatcher{
		root:             root,
		exactIgnoredDirs: make(map[string]struct{}, len(ignoredDirs)),
		dirRules:         make(map[string][]ignoreRule),
	}
	for _, dir := range ignoredDirs {
		if hasGlobMeta(dir) {
			m.globIgnoredDirs = append(m.globIgnoredDirs, dir)
		} else {
			m.exactIgnoredDirs[dir] = struct{}{}
		}
	}

	if root != "" {
		m.excludeRules = loadIgnoreFile(filepath.Join(root, ".git", "info", "exclude"))
		if len(m.excludeRules) > 0 {
			m.hasGitRules = true
		}
		m.LoadDir("")
	}
	return m
}

// MatchIgnoredDirName reports whether a single directory base name matches
// ignoredDirs (exact match or path.Match glob).
func (m *IgnoreMatcher) MatchIgnoredDirName(name string) bool {
	if _, ok := m.exactIgnoredDirs[name]; ok {
		return true
	}
	for _, pattern := range m.globIgnoredDirs {
		if matched, err := path.Match(pattern, name); err == nil && matched {
			return true
		}
	}
	return false
}

// LoadDir reads and caches <root>/<relDir>/.gitignore at most once.
// relDir must be slash-normalized relative to root ("" for root).
func (m *IgnoreMatcher) LoadDir(relDir string) {
	if m.root == "" {
		return
	}
	relDir = normalizeRel(relDir)
	if _, loaded := m.dirRules[relDir]; loaded {
		return
	}
	var ignorePath string
	if relDir == "" {
		ignorePath = filepath.Join(m.root, ".gitignore")
	} else {
		ignorePath = filepath.Join(m.root, filepath.FromSlash(relDir), ".gitignore")
	}
	rules := loadIgnoreFile(ignorePath)
	m.dirRules[relDir] = rules
	if len(rules) > 0 {
		m.hasGitRules = true
	}
}

// IsDirIgnored reports whether directory relPath (root-relative, slash-normalized)
// is ignored by ignoredDirs or gitignore rules.
func (m *IgnoreMatcher) IsDirIgnored(relPath string) bool {
	relPath = normalizeRel(relPath)
	if relPath == "" {
		return false
	}
	if m.MatchIgnoredDirName(path.Base(relPath)) {
		return true
	}
	return m.matchGitRules(relPath, true)
}

// IsFileIgnored reports whether file relPath (root-relative, slash-normalized)
// is ignored by .git/info/exclude or .gitignore rules.
func (m *IgnoreMatcher) IsFileIgnored(relPath string) bool {
	relPath = normalizeRel(relPath)
	if relPath == "" {
		return false
	}
	return m.matchGitRules(relPath, false)
}

// IsPathIgnored checks every ancestor directory of relPath (against both
// ignoredDirs and directory gitignore rules) and then checks relPath itself.
// Used by single-file operations like ReindexFile that do not run a directory walk.
func (m *IgnoreMatcher) IsPathIgnored(relPath string, isDir bool) bool {
	relPath = normalizeRel(relPath)
	if relPath == "" {
		return false
	}

	segments := strings.Split(relPath, "/")
	for i := 0; i < len(segments)-1; i++ {
		ancestor := strings.Join(segments[:i+1], "/")
		if m.IsDirIgnored(ancestor) {
			return true
		}
		m.LoadDir(ancestor)
	}

	if isDir {
		return m.IsDirIgnored(relPath)
	}
	return m.IsFileIgnored(relPath)
}

func (m *IgnoreMatcher) matchGitRules(relPath string, isDir bool) bool {
	if m.root == "" {
		return false
	}
	// Ensure all ancestor directories' .gitignore files are loaded when called
	// outside a top-down walk (no-op when already cached by WalkDir).
	m.ensureAncestorsLoaded(relPath)
	if !m.hasGitRules {
		return false
	}

	ignored := false
	ignored = applyRules(m.excludeRules, relPath, isDir, ignored)
	ignored = applyRules(m.dirRules[""], relPath, isDir, ignored)

	// Evaluate nested .gitignore files from shallowest to deepest ancestor.
	for i := 0; i < len(relPath); i++ {
		if relPath[i] != '/' {
			continue
		}
		dir := relPath[:i]
		rules := m.dirRules[dir]
		if len(rules) > 0 {
			ignored = applyRules(rules, relPath[i+1:], isDir, ignored)
		}
	}
	return ignored
}

func (m *IgnoreMatcher) ensureAncestorsLoaded(relPath string) {
	for i := 0; i < len(relPath); i++ {
		if relPath[i] == '/' {
			dir := relPath[:i]
			if _, loaded := m.dirRules[dir]; !loaded {
				m.LoadDir(dir)
			}
		}
	}
}

func applyRules(rules []ignoreRule, relToBase string, isDir bool, ignored bool) bool {
	for i := range rules {
		if rules[i].match(relToBase, isDir) {
			ignored = !rules[i].negated
		}
	}
	return ignored
}

func normalizeRel(relPath string) string {
	relPath = strings.Trim(filepath.ToSlash(relPath), "/")
	if relPath == "." {
		return ""
	}
	return relPath
}

func loadIgnoreFile(filePath string) []ignoreRule {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return parseGitignore(string(data))
}

func parseGitignore(content string) []ignoreRule {
	lines := strings.Split(content, "\n")
	rules := make([]ignoreRule, 0, len(lines))
	for _, line := range lines {
		if rule, ok := parseGitignoreLine(line); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

func parseGitignoreLine(line string) (ignoreRule, bool) {
	line = strings.TrimSuffix(line, "\r")
	line = trimUnescapedTrailingSpaces(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}

	var rule ignoreRule
	if strings.HasPrefix(line, "!") {
		rule.negated = true
		line = line[1:]
	} else if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
		line = line[1:]
	}

	if strings.HasSuffix(line, "/") && !strings.HasSuffix(line, `\/`) {
		rule.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}

	if line == "" {
		return ignoreRule{}, false
	}

	if strings.Contains(line, "/") {
		rule.anchored = true
		line = strings.TrimPrefix(line, "/")
	}
	if line == "" {
		return ignoreRule{}, false
	}

	if !rule.anchored {
		rule.pattern = collapseDoubleStar(line)
		return rule, true
	}

	rule.trailingDoubleStar = strings.HasSuffix(line, "/**")
	rawSegs := strings.Split(line, "/")
	rule.segments = make([]string, len(rawSegs))
	for i, seg := range rawSegs {
		if seg == "**" {
			rule.segments[i] = "**"
		} else {
			rule.segments[i] = collapseDoubleStar(seg)
		}
	}
	return rule, true
}

func trimUnescapedTrailingSpaces(s string) string {
	end := len(s)
	for end > 0 && s[end-1] == ' ' {
		backslashes := 0
		for i := end - 2; i >= 0 && s[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			// Escaped trailing space: keep the space and remove the escape backslash.
			return s[:end-2] + " "
		}
		end--
	}
	return s[:end]
}

func collapseDoubleStar(seg string) string {
	for strings.Contains(seg, "**") {
		seg = strings.ReplaceAll(seg, "**", "*")
	}
	return seg
}

func (r *ignoreRule) match(relToBase string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	if !r.anchored {
		ok, err := path.Match(r.pattern, path.Base(relToBase))
		return err == nil && ok
	}
	pathSegs := strings.Split(relToBase, "/")
	return matchSegments(r.segments, pathSegs, r.trailingDoubleStar)
}

func matchSegments(pat, segs []string, trailingDoubleStar bool) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		if len(pat) == 1 {
			if trailingDoubleStar {
				return len(segs) >= 1
			}
			return true
		}
		for i := 0; i <= len(segs); i++ {
			if matchSegments(pat[1:], segs[i:], trailingDoubleStar) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], segs[1:], trailingDoubleStar)
}
