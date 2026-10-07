package api

import (
	"fmt"
	"net/http"
	"strconv"
)

// Cursor pagination for history lists (§14): ?limit=N&before=<cursor>, where
// the cursor is the ID of the last item already seen. Responses are
// {items, next}; next is the cursor of the following page ("" at the end).

// pageParams reads limit (default def, at most max) and before.
func pageParams(w http.ResponseWriter, r *http.Request, def, max int) (limit int, before string, ok bool) {
	q := r.URL.Query()
	limit = def
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > max {
			writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("limit must be 1–%d", max))
			return 0, "", false
		}
		limit = n
	}
	return limit, q.Get("before"), true
}

// nextCursor is the cursor after a raw page (before filtering or mapping).
func nextCursor[T any](raw []T, limit int, cursor func(T) string) string {
	if len(raw) == limit && limit > 0 {
		return cursor(raw[len(raw)-1])
	}
	return ""
}

// writePage writes a page; a full page has a next cursor (its last item's).
func writePage[T any](w http.ResponseWriter, items []T, limit int, cursor func(T) string) {
	if items == nil {
		items = []T{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next": nextCursor(items, limit, cursor)})
}
