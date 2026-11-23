package domain

type SearchResultDB struct {
	Filepath  string  `db:"filepath"`
	StartLine int     `db:"start_line"`
	EndLine   int     `db:"end_line"`
	Content   string  `db:"content"`
	Snippet   string  `db:"snippet"`
	Score     float64 `db:"score"`
	Total     int     `db:"total"`
}

// RefreshSummary reports what the pre-search refresh changed. It is omitted when
// the caller asked for no refresh, because "zero changes" and "not checked" are
// different answers and only one of them is a claim about the index.
type RefreshSummary struct {
	Added   int `json:"added"`
	Changed int `json:"changed"`
	Removed int `json:"removed"`
	Skipped int `json:"skipped"`
}

// IndexResponse is the envelope returned by the index command and MCP tool.
type IndexResponse struct {
	Status    string         `json:"status" jsonschema:"human-readable result of the index run"`
	Refreshed RefreshSummary `json:"refreshed" jsonschema:"files added, changed, removed, and skipped by this run; all zero when the index was already current"`
}

// SearchResponse is the envelope around a result set. The index metadata leads
// so a caller sees whether the answer came from the tree it is looking at before
// it reads the results. Total counts every matching chunk, so Truncated is a
// statement about the whole match rather than about the returned slice.
type SearchResponse struct {
	Root      string             `json:"root"`
	IndexedAt string             `json:"indexed_at,omitempty"`
	Refreshed *RefreshSummary    `json:"refreshed,omitempty"`
	Total     int                `json:"total"`
	Truncated bool               `json:"truncated"`
	Results   []SearchResultJSON `json:"results"`
}

// SearchResultJSON is one match handed to the caller. Match says whether the
// terms were found in the file's path, its content, or both: a path-only hit
// means the chunk boundary is not necessarily where the interesting code is.
type SearchResultJSON struct {
	Root     string `json:"root"`
	Filepath string `json:"filepath"`
	Line     string `json:"line"`
	Snippet  string `json:"snippet"`
	Match    string `json:"match"`
}

type ReadResultJSON struct {
	Root     string `json:"root"`
	Filepath string `json:"filepath"`
	Content  string `json:"content"`
	Hash     string `json:"hash"`
}

type EditResultJSON struct {
	Status  string `json:"status" jsonschema:"human-readable result status"`
	Message string `json:"message" jsonschema:"details of the edit"`
}
