package tools

import (
	"bytes"
	"encoding/json"
	"strings"
)

// EncodeJSON serializes v like json.Marshal but without HTML escaping.
// Search snippets wrap matches in <b> tags, and encoding those as \u003c makes
// stdout needlessly hard to read. Angle brackets are valid inside a JSON string;
// escaping them only matters when the JSON is embedded in HTML, which is not
// what this CLI emits. The encoder's trailing newline is dropped so callers keep
// printing exactly one line.
func EncodeJSON(v any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
