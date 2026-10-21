package handlers

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// searchHit is one file matched by searchOwnFiles: which of its search fields
// matched (metaMatch, "" for none), and for a full-text search what a content
// excerpt needs -- the window around the first content match, that match's
// 1-based character offset (0 when the content did not match) and the
// document's length in characters.
type searchHit struct {
	file         sqlcgen.File
	metaMatch    string
	window       string
	matchPos     int64
	contentChars int64
}

// snippet is what a result shows under a name that did not itself match: the
// search field that did, else the excerpt around the content match.
func (h searchHit) snippet(q string) string {
	if h.metaMatch != "" {
		window, pos, chars := contentWindow(h.metaMatch, asciiLower(q), q)
		return snippetFromWindow(window, q, pos, chars)
	}
	return snippetFromWindow(h.window, q, h.matchPos, h.contentChars)
}

// searchOwnFiles is the search behind GET /api/search and MCP's search_files:
// the caller's live files whose name or search fields (a document's title,
// description and keywords -- see internal/searchmeta) contain q, newest
// first; with withContent, also those whose full text does.
//
// The default answers from the database alone (SearchUserFiles), which is
// what makes it cheap: the search fields were extracted when the content was
// stored. A full-text search has to read every document from its blob, as the
// SQL search over html_content once read it from the database -- the same
// bytes, bounded the same way by the account's quota. Its matching rules are
// that query's, kept so results did not change: case-insensitive for ASCII
// only (SQLite's lower()), offsets counted in characters (instr()), and a
// window of 100 characters either side of the match (substr()). Sites are not
// full-text searched: their source is a zip, not text.
func searchOwnFiles(ctx context.Context, queries *sqlcgen.Queries, store *blobstore.Store, userID int64, q string, withContent bool) ([]searchHit, error) {
	needle := asciiLower(q)
	hits := make([]searchHit, 0)

	if !withContent {
		rows, err := queries.SearchUserFiles(ctx, sqlcgen.SearchUserFilesParams{UserID: userID, Query: q})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			hits = append(hits, searchHit{file: row.File, metaMatch: metaLine(row.SearchMeta, needle)})
		}
		return hits, nil
	}

	rows, err := queries.ListUserFilesWithSearchMeta(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		// A search the client has given up on (the dashboard drops stale
		// responses) stops here rather than reading the rest of the account.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hit := searchHit{file: row.File, metaMatch: metaLine(row.SearchMeta, needle)}
		if row.File.Kind != KindSite {
			content, err := readContent(store, row.File)
			if err != nil {
				return nil, err
			}
			hit.window, hit.matchPos, hit.contentChars = contentWindow(string(content), needle, q)
		}
		if hit.matchPos > 0 || hit.metaMatch != "" || strings.Contains(asciiLower(row.File.Name), needle) {
			hits = append(hits, hit)
		}
	}
	return hits, nil
}

// metaLine returns the first search field (one per line of meta) containing
// needle, already ASCII-lowered, or "".
func metaLine(meta, needle string) string {
	for _, line := range strings.Split(meta, "\n") {
		if strings.Contains(asciiLower(line), needle) {
			return line
		}
	}
	return ""
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
