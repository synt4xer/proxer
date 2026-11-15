package domain

type SearchResultDB struct {
	Filepath  string `db:"filepath"`
	StartLine int    `db:"start_line"`
	EndLine   int    `db:"end_line"`
	Snippet   string `db:"snippet"`
}

type SearchResultJSON struct {
	Filepath string `json:"filepath"`
	Line     string `json:"line"`
	Snippet  string `json:"snippet"`
}

type ReadResultJSON struct {
	Filepath string `json:"filepath"`
	Content  string `json:"content"`
}
