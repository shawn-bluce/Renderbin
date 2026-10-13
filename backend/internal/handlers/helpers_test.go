package handlers

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

func TestNormalizeTags(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"a", "a"},
		{" a , b ,c ", "a,b,c"},
		{"a,a,a", "a"},
		{"a,,b", "a,b"},
		{"  ,  ", ""},
		{"b,a,b,c,a", "b,a,c"}, // dedupe preserves first-seen order
	}
	for _, c := range cases {
		if got := normalizeTags(c.in); got != c.want {
			t.Errorf("normalizeTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAccessCodeMatches(t *testing.T) {
	cases := []struct {
		want, got string
		match     bool
	}{
		{"abc", "abc", true},
		{"", "", true},
		{"abc", "abd", false},
		{"abc", "abcd", false},
		{"abcd", "abc", false},
		{"abc", "", false},
	}
	for _, c := range cases {
		if got := accessCodeMatches(c.want, c.got); got != c.match {
			t.Errorf("accessCodeMatches(%q, %q) = %v, want %v", c.want, c.got, got, c.match)
		}
	}
}

func TestSanitizeASCII(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain.html", "plain.html"},
		{"a b", "a b"},         // space kept
		{"a\"b", "a_b"},        // quote replaced
		{"a\\b", "a_b"},        // backslash replaced
		{"a\tb", "a_b"},        // control char replaced
		{"a\r\nb", "a__b"},     // CR/LF replaced (header-injection guard)
		{"héllo", "h_llo"},     // non-ASCII replaced
		{"测试.html", "__.html"}, // two runes + dot + html
	}
	for _, c := range cases {
		if got := sanitizeASCII(c.in); got != c.want {
			t.Errorf("sanitizeASCII(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDownloadDisposition(t *testing.T) {
	// ASCII name: both the ascii fallback and the RFC 5987 form carry it.
	got := downloadDisposition("report", "slug123", "html")
	if !strings.Contains(got, `filename="report.html"`) {
		t.Errorf("disposition %q missing ascii filename", got)
	}
	if !strings.HasPrefix(got, "attachment; ") {
		t.Errorf("disposition %q should start with attachment;", got)
	}

	// Unicode name: ascii fallback is sanitized, filename* is percent-encoded.
	got = downloadDisposition("测试", "slug123", "html")
	if !strings.Contains(got, `filename="__.html"`) {
		t.Errorf("disposition %q missing sanitized ascii fallback", got)
	}
	if !strings.Contains(got, "filename*=UTF-8''"+url.PathEscape("测试.html")) {
		t.Errorf("disposition %q missing RFC5987 filename*", got)
	}

	// Empty name falls back to slug for both forms.
	got = downloadDisposition("   ", "myslug", "html")
	if !strings.Contains(got, `filename="myslug.html"`) {
		t.Errorf("disposition %q should fall back to slug", got)
	}

	// The extension tracks the file's kind (e.g. markdown → .md).
	got = downloadDisposition("notes", "slug123", "md")
	if !strings.Contains(got, `filename="notes.md"`) {
		t.Errorf("disposition %q should use the .md extension", got)
	}
}

// A slug (or access code) is a URL path segment: the charset is what keeps it
// one, and "." / ".." pass the charset but a browser drops them from a URL
// before requesting it, so validPathToken refuses those two on top.
func TestSlugPatternAndPathToken(t *testing.T) {
	for _, c := range []struct {
		in             string
		pattern, token bool
	}{
		{"a", true, true},
		{"abc-123_.X", true, true},
		{strings.Repeat("a", 128), true, true},
		{"...", true, true},
		{".a", true, true},
		{"a.b", true, true},
		{".", true, false},
		{"..", true, false},
		{"", false, false},
		{"a b", false, false},
		{"a/b", false, false},
		{"a!b", false, false},
		{"日本", false, false},
		{strings.Repeat("a", 129), false, false},
	} {
		if got := slugPattern.MatchString(c.in); got != c.pattern {
			t.Errorf("slugPattern(%q) = %v, want %v", c.in, got, c.pattern)
		}
		if got := validPathToken(c.in); got != c.token {
			t.Errorf("validPathToken(%q) = %v, want %v", c.in, got, c.token)
		}
	}
}

// sqlWindow reproduces, in Go, exactly what SearchUserFilesWithContent's
// instr()/substr() pair extracts, so the cases below exercise
// snippetFromWindow against the shape it really receives instead of a
// convenient one. SQLite counts characters (not bytes) in both functions and
// instr() is 1-based, which is what makes the CJK case below meaningful.
// TestSearchReturnsContentSnippets in internal/server covers the SQL half for
// real, against a live database.
func sqlWindow(content, query string) (window string, matchPos, contentChars int64) {
	full := []rune(content)
	lowered := []rune(strings.ToLower(content))
	needle := []rune(strings.ToLower(query))

	pos := -1
	for i := 0; i+len(needle) <= len(lowered); i++ {
		if string(lowered[i:i+len(needle)]) == string(needle) {
			pos = i
			break
		}
	}
	if pos < 0 {
		return "", 0, int64(len(full))
	}

	matchPos = int64(pos + 1)
	start := max(int64(1), matchPos-100)
	end := min(int(start-1)+len(needle)+200, len(full))
	return string(full[start-1 : end]), matchPos, int64(len(full))
}

func TestSnippetFromWindow(t *testing.T) {
	cases := []struct {
		name    string
		content string
		query   string
		want    string
	}{
		{
			name:    "no match is empty",
			content: "hello world",
			query:   "zzz",
			want:    "",
		},
		{
			name:    "short content comes back whole, without ellipses",
			content: "hello world",
			query:   "WORLD",
			want:    "hello world",
		},
		{
			name:    "long content keeps 100 runes of context on each side",
			content: strings.Repeat("a", 500) + "NEEDLE" + strings.Repeat("b", 500),
			query:   "needle",
			want:    "…" + strings.Repeat("a", 100) + "NEEDLE" + strings.Repeat("b", 100) + "…",
		},
		{
			name:    "context is counted in runes, not bytes",
			content: strings.Repeat("汉", 300) + "目标" + strings.Repeat("字", 300),
			query:   "目标",
			want:    "…" + strings.Repeat("汉", 100) + "目标" + strings.Repeat("字", 100) + "…",
		},
		{
			// The window SQL hands over is longer than we want here, because
			// its fixed length was budgeted for 100 runes of lead-in that do
			// not exist. The trailing context still has to come out at 100.
			name:    "a match at the start has no leading ellipsis",
			content: "NEEDLE" + strings.Repeat("x", 500),
			query:   "needle",
			want:    "NEEDLE" + strings.Repeat("x", 100) + "…",
		},
		{
			name:    "a match at the end has no trailing ellipsis",
			content: strings.Repeat("x", 500) + "NEEDLE",
			query:   "needle",
			want:    "…" + strings.Repeat("x", 100) + "NEEDLE",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			window, matchPos, contentChars := sqlWindow(c.content, c.query)
			got := snippetFromWindow(window, c.query, matchPos, contentChars)
			if got != c.want {
				t.Errorf("snippetFromWindow() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestToFileResponse(t *testing.T) {
	created := time.Date(2026, 7, 16, 10, 30, 0, 0, time.UTC)

	// Unset limit columns → nil pointers in the response.
	unset := toFileResponse(sqlcgen.File{
		Slug:      "s",
		Name:      "n",
		CreatedAt: created,
		UpdatedAt: created,
	})
	if unset.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil", *unset.ExpiresAt)
	}
	if unset.MaxViews != nil {
		t.Errorf("MaxViews = %v, want nil", *unset.MaxViews)
	}
	if unset.CreatedAt != "2026-07-16T10:30:00Z" {
		t.Errorf("CreatedAt = %q, want RFC3339 UTC", unset.CreatedAt)
	}

	// Set limit columns → populated pointers.
	set := toFileResponse(sqlcgen.File{
		Slug:      "s",
		CreatedAt: created,
		UpdatedAt: created,
		ExpiresAt: sql.NullTime{Time: created, Valid: true},
		MaxViews:  sql.NullInt64{Int64: 7, Valid: true},
		ViewCount: 3,
	})
	if set.ExpiresAt == nil || *set.ExpiresAt != "2026-07-16T10:30:00Z" {
		t.Errorf("ExpiresAt = %v, want the formatted time", set.ExpiresAt)
	}
	if set.MaxViews == nil || *set.MaxViews != 7 {
		t.Errorf("MaxViews = %v, want 7", set.MaxViews)
	}
	if set.ViewCount != 3 {
		t.Errorf("ViewCount = %d, want 3", set.ViewCount)
	}
}
