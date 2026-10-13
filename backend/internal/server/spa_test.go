package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// The embedded dist/ is empty on a fresh clone (only `make build-web` fills
// it), so the handler is tested over an in-memory tree instead of the real
// embed: against an empty directory any "serves index.html" assertion would
// pass on a directory listing and prove nothing.
func TestSPAHandler(t *testing.T) {
	h := spaHandler(fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>shell</title>")},
		"_app/immutable/a.js": {Data: []byte("console.log('asset')")},
	})

	get := func(path string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		b, _ := io.ReadAll(rec.Result().Body)
		return rec.Code, string(b)
	}

	for _, tc := range []struct {
		name, path, want string
	}{
		{"root", "/", "<title>shell</title>"},
		{"a real asset is served as itself", "/_app/immutable/a.js", "console.log('asset')"},
		// Client-side routes have no file behind them; the SPA router resolves them.
		{"a client route falls back to the shell", "/settings", "<title>shell</title>"},
		{"a deep client route falls back to the shell", "/some/deep/route", "<title>shell</title>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := get(tc.path)
			if code != http.StatusOK || !strings.Contains(body, tc.want) {
				t.Errorf("GET %s = %d %q, want 200 containing %q", tc.path, code, body, tc.want)
			}
		})
	}
}
