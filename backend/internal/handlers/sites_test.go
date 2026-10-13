package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

func TestPreviewToken(t *testing.T) {
	p := newPreviewSigner()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	file := sqlcgen.File{ID: 7, UserID: 3}
	owner := sqlcgen.User{ID: 3, PasswordHash: "hash-a"}

	tok := p.token(file, owner, now)
	if !strings.HasPrefix(tok, previewPrefix) {
		t.Fatalf("token %q lacks the %q prefix", tok, previewPrefix)
	}
	// Tokens sit in a URL path segment, so they must stay in the slug charset
	// plus the prefix.
	if !slugPattern.MatchString(strings.TrimPrefix(tok, previewPrefix)) {
		t.Errorf("token %q is not path-safe", tok)
	}

	cases := []struct {
		name  string
		file  sqlcgen.File
		owner sqlcgen.User
		at    time.Time
		tok   string
		ok    bool
	}{
		{"valid", file, owner, now.Add(time.Hour), tok, true},
		{"just before expiry", file, owner, now.Add(previewTTL - time.Second), tok, true},
		{"expired", file, owner, now.Add(previewTTL), tok, false},
		{"another file", sqlcgen.File{ID: 8, UserID: 3}, owner, now, tok, false},
		{"password changed", file, sqlcgen.User{ID: 3, PasswordHash: "hash-b"}, now, tok, false},
		{"another signer", file, owner, now, newPreviewSigner().token(file, owner, now), false},
		{"garbage", file, owner, now, "~not-base64!", false},
		{"truncated", file, owner, now, tok[:len(tok)-2], false},
	}
	for _, c := range cases {
		if got := p.verify(c.tok, c.file, c.owner, c.at); got != c.ok {
			t.Errorf("%s: verify = %v, want %v", c.name, got, c.ok)
		}
	}
}

func TestIsDocumentRequest(t *testing.T) {
	cases := []struct {
		entry, dest string
		want        bool
	}{
		{"index.html", "document", true},
		{"a/b.htm", "iframe", true},
		{"index.html", "", true}, // curl and friends
		{"index.html", "empty", false},
		{"style.css", "document", false},
		{"data.json", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if c.dest != "" {
			r.Header.Set("Sec-Fetch-Dest", c.dest)
		}
		if got := isDocumentRequest(r, c.entry); got != c.want {
			t.Errorf("isDocumentRequest(%q, dest=%q) = %v, want %v", c.entry, c.dest, got, c.want)
		}
	}
}
