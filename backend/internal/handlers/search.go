package handlers

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// contentHit is one file matched by searchOwnFiles, with what the snippet
// needs: the window around the first content match, that match's 1-based
// character offset (0 when only the name matched) and the document's length in
// characters.
type contentHit struct {
	file         sqlcgen.File
	window       string
	matchPos     int64
	contentChars int64
}

// searchOwnFiles is the name-and-content search behind GET /api/search?content=true
// and MCP's search_files: the caller's live files whose name or content
// contains q, newest first.
//
// It used to be one SQL statement over html_content. The content is on disk
// now, so the statement lists the candidates and this reads each document from
// its blob -- the same bytes the query used to scan, read from the same volume,
// bounded the same way by the account's quota. The matching rules are the
// query's, kept on purpose so results did not change: case-insensitive for
// ASCII only (SQLite's lower()), offsets counted in characters (instr()), and
// a window of 100 characters either side of the match (substr()).
//
// Sites are matched by name only, as before: their source is a zip, and the
// query only ever looked at html_content, which a site leaves empty.
func searchOwnFiles(ctx context.Context, queries *sqlcgen.Queries, store *blobstore.Store, userID int64, q string) ([]contentHit, error) {
	files, err := queries.ListUserFiles(ctx, userID)
	if err != nil {
		return nil, err
	}
	needle := asciiLower(q)

	hits := make([]contentHit, 0)
	for _, f := range files {
		// A search the client has given up on (the dashboard drops stale
		// responses) stops here rather than reading the rest of the account.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hit := contentHit{file: f}
		if f.Kind != KindSite {
			content, err := readContent(store, f)
			if err != nil {
				return nil, err
			}
			hit.window, hit.matchPos, hit.contentChars = contentWindow(string(content), needle, q)
		}
		if hit.matchPos > 0 || strings.Contains(asciiLower(f.Name), needle) {
			hits = append(hits, hit)
		}
	}
	return hits, nil
}

// contentWindow finds needle (already ASCII-lowered; q is the query as typed,
// whose length in characters sizes the window) in content and returns what
// instr()/substr() returned for it: up to 100 characters before the match
// through 100 after it, the match's 1-based character offset (0 for no match),
// and the content's length in characters.
//
// ASCII lowering never changes a string's byte length, which is what lets a
// byte offset found in the lowered copy index the original.
func contentWindow(content, needle, q string) (window string, matchPos, contentChars int64) {
	contentChars = int64(utf8.RuneCountInString(content))
	at := strings.Index(asciiLower(content), needle)
	if at < 0 {
		return "", 0, contentChars
	}
	matchPos = int64(utf8.RuneCountInString(content[:at])) + 1

	start := at
	for i := 0; i < snippetRadius && start > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(content[:start])
		start -= size
	}
	// substr() asked for len(q) + 2*snippetRadius characters from the window's
	// start; snippetFromWindow trims whatever runs past snippetRadius after the
	// match.
	end := start
	for i := 0; i < utf8.RuneCountInString(q)+2*snippetRadius && end < len(content); i++ {
		_, size := utf8.DecodeRuneInString(content[end:])
		end += size
	}
	return content[start:end], matchPos, contentChars
}

// asciiLower lowercases ASCII letters only, as SQLite's built-in lower() does,
// and returns s itself when there is nothing to change.
func asciiLower(s string) string {
	i := 0
	for i < len(s) && (s[i] < 'A' || s[i] > 'Z') {
		i++
	}
	if i == len(s) {
		return s
	}
	b := []byte(s)
	for ; i < len(b); i++ {
		if c := b[i]; c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
