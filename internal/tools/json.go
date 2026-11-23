package tools

import json "encoding/json/v2"

// EncodeJSON serializes v as compact, single-line JSON without HTML escaping.
// Search snippets wrap matches in <b> tags, and encoding/json/v2 leaves angle
// brackets literal by default without adding a trailing newline.
func EncodeJSON(v any) (string, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
