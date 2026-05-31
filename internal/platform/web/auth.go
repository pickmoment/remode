package web

import (
	"net/http"
	"strings"
)

// extractToken reads the bearer token from the Authorization header or
// the "token" query parameter.
func extractToken(r *http.Request) string {
	// Prefer Authorization header
	if auth := r.Header.Get("Authorization"); auth != "" {
		if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
	}
	// Fallback for EventSource (can't set headers)
	return r.URL.Query().Get("token")
}
