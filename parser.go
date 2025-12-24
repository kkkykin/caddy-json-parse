package jsonparse

import (
	"strconv"
	"strings"

	"github.com/caddyserver/caddy/v2"
)

func fetchValue(value any, path string) any {
	for _, key := range strings.Split(path, ".") {
		switch node := value.(type) {
		case map[string]any:
			value = node[key]
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(node) {
				return nil
			}
			value = node[i]
		default:
			return nil
		}
	}
	return value
}

func newReplacerFunc(doc *document) caddy.ReplacerFunc {
	return func(key string) (any, bool) {
		path, ok := strings.CutPrefix(key, "json.")
		if !ok {
			return nil, false
		}
		// Do not cache values: an intervening json_transform may replace any
		// part of the document, including its root.
		return fetchValue(doc.value, path), true
	}
}
