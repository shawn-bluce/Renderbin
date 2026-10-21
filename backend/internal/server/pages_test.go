package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

type pageResp struct {
	Files      []fileResp `json:"files"`
	NextCursor string     `json:"next_cursor"`
	Counts     struct {
		All     int64 `json:"all"`
		Public  int64 `json:"public"`
		Private int64 `json:"private"`
		Trash   int64 `json:"trash"`
	} `json:"counts"`
}

func (e *testEnv) page(t *testing.T, cookie *http.Cookie, query url.Values) pageResp {
	t.Helper()
	return decodeJSON[pageResp](t, e.do(t, http.MethodGet, "/api/files?"+query.Encode(), "", cookie), http.StatusOK)
}

// walk follows next_cursor to the end and returns every slug in order.
func (e *testEnv) walk(t *testing.T, cookie *http.Cookie, query url.Values) []string {
	t.Helper()
	var slugs []string
	for range 100 {
		p := e.page(t, cookie, query)
		for _, f := range p.Files {
			slugs = append(slugs, f.Slug)
		}
		if p.NextCursor == "" {
			return slugs
		}
		query.Set("cursor", p.NextCursor)
	}
	t.Fatal("paging did not end")
	return nil
}

// asciiFold is SQLite's NOCASE: ASCII letters folded, nothing else.
func asciiFold(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

func TestPagedListingVisitsEveryFileOnceInOrder(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// Names with case-only differences and exact repeats, so ties are real;
	// counters with repeats for the same reason; a mix of visibilities.
	names := []string{"beta", "Alpha", "alpha", "Gamma", "beta", "delta", "汉字", "Zeta", "alpha", "épée", "b", "B", "c"}
	var all []sqlcgen.File
	for i, name := range names {
		slug := fmt.Sprintf("f%02d", i)
		e.insertFile(t, slug, "html", "<p>x</p>", "code-"+slug, i%3 == 0)
		if _, err := e.conn.Exec(`UPDATE files SET name = ?, success_count = ?, failure_count = ? WHERE slug = ?`,
			name, i%4, (i*7)%5, slug); err != nil {
			t.Fatal(err)
		}
		all = append(all, e.fileRow(t, slug))
	}

	for _, c := range []struct {
		sort string
		less func(a, b sqlcgen.File) bool // ascending order, id breaking ties
	}{
		{"recent", func(a, b sqlcgen.File) bool { return a.ID < b.ID }},
		{"name", func(a, b sqlcgen.File) bool {
			if x, y := asciiFold(a.Name), asciiFold(b.Name); x != y {
				return x < y
			}
			return a.ID < b.ID
		}},
		{"success", func(a, b sqlcgen.File) bool {
			if a.SuccessCount != b.SuccessCount {
				return a.SuccessCount < b.SuccessCount
			}
			return a.ID < b.ID
		}},
		{"failure", func(a, b sqlcgen.File) bool {
			if a.FailureCount != b.FailureCount {
				return a.FailureCount < b.FailureCount
			}
			return a.ID < b.ID
		}},
	} {
		dirs := []string{"asc", "desc"}
		if c.sort == "recent" {
			dirs = []string{"desc"} // newest first only
		}
		for _, dir := range dirs {
			for _, vis := range []string{"all", "public", "private"} {
				var want []sqlcgen.File
				for _, f := range all {
					if vis == "all" || f.IsPublic == (vis == "public") {
						want = append(want, f)
					}
				}
				sort.Slice(want, func(i, j int) bool {
					if dir == "desc" {
						return c.less(want[j], want[i])
					}
					return c.less(want[i], want[j])
				})
				wantSlugs := make([]string, len(want))
				for i, f := range want {
					wantSlugs[i] = f.Slug
				}

				got := e.walk(t, cookie, url.Values{"limit": {"4"}, "sort": {c.sort}, "dir": {dir}, "visibility": {vis}})
				if strings.Join(got, ",") != strings.Join(wantSlugs, ",") {
					t.Errorf("sort=%s dir=%s visibility=%s:\n got  %v\n want %v", c.sort, dir, vis, got, wantSlugs)
				}
			}
		}
	}
}

func TestPagedListingCountsAndTrash(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	for i := range 7 {
		slug := fmt.Sprintf("f%d", i)
		e.insertFile(t, slug, "html", "<p>x</p>", "code", i < 2)
	}
	for _, slug := range []string{"f4", "f2", "f6"} { // deleted in this order
		resp := e.do(t, http.MethodDelete, "/api/files/"+slug, "", cookie)
		assertStatus(t, resp, http.StatusNoContent)
		resp.Body.Close()
		// Distinct deleted_at seconds, so the order is not left to the id
		// tie-break.
		if _, err := e.conn.Exec(`UPDATE files SET deleted_at = datetime(deleted_at, ?) WHERE slug = ?`,
			fmt.Sprintf("+%d seconds", map[string]int{"f4": 1, "f2": 2, "f6": 3}[slug]), slug); err != nil {
			t.Fatal(err)
		}
	}

	p := e.page(t, cookie, url.Values{"limit": {"2"}})
	if p.Counts.All != 4 || p.Counts.Public != 2 || p.Counts.Private != 2 || p.Counts.Trash != 3 {
		t.Errorf("counts = %+v, want all 4, public 2, private 2, trash 3", p.Counts)
	}
	if len(p.Files) != 2 || p.NextCursor == "" {
		t.Errorf("first page = %d files, cursor %q; want 2 and a cursor", len(p.Files), p.NextCursor)
	}

	if got := e.walk(t, cookie, url.Values{"limit": {"2"}, "deleted": {"true"}}); strings.Join(got, ",") != "f6,f2,f4" {
		t.Errorf("trash pages = %v, want most recently deleted first", got)
	}
}

func TestPagedListingRejectsBadRequests(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	for i := range 3 {
		e.insertFile(t, fmt.Sprintf("f%d", i), "html", "<p>x</p>", "code", false)
	}
	byName := e.page(t, cookie, url.Values{"limit": {"1"}, "sort": {"name"}})

	for _, q := range []url.Values{
		{"limit": {"0"}},
		{"limit": {"x"}},
		{"limit": {"5"}, "sort": {"size"}},
		{"limit": {"5"}, "visibility": {"secret"}},
		{"limit": {"5"}, "sort": {"name"}, "dir": {"sideways"}},
		{"limit": {"5"}, "cursor": {"not-a-cursor"}},
		// A name-sorted cursor replayed against another listing would skip
		// rows silently; it is refused instead.
		{"limit": {"5"}, "sort": {"success"}, "cursor": {byName.NextCursor}},
		{"limit": {"5"}, "sort": {"name"}, "dir": {"desc"}, "cursor": {byName.NextCursor}},
		{"limit": {"5"}, "deleted": {"true"}, "cursor": {byName.NextCursor}},
	} {
		resp := e.do(t, http.MethodGet, "/api/files?"+q.Encode(), "", cookie)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /api/files?%s = %d, want 400", q.Encode(), resp.StatusCode)
		}
		resp.Body.Close()
	}

	// A huge limit is capped, not refused.
	if p := e.page(t, cookie, url.Values{"limit": {"100000"}}); len(p.Files) != 3 {
		t.Errorf("capped page = %d files", len(p.Files))
	}
}

// API clients that never asked for pages still get one array of everything.
func TestUnpagedListingIsUnchanged(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	for i := range 3 {
		e.insertFile(t, fmt.Sprintf("f%d", i), "html", "<p>x</p>", "code", false)
	}
	body := bodyOf(t, e.do(t, http.MethodGet, "/api/files", "", cookie), http.StatusOK)
	var files []fileResp
	if err := json.Unmarshal([]byte(body), &files); err != nil || len(files) != 3 {
		t.Errorf("unpaged listing = %q (%v), want an array of 3", body, err)
	}
}

// A page is the caller's files only, like every other listing.
func TestPagedListingIsOwnerScoped(t *testing.T) {
	e := newEnv(t)
	e.insertFile(t, "admins", "html", "<p>x</p>", "code", true)
	_, cookie := e.newUser(t, "other")
	for _, q := range []url.Values{{"limit": {"10"}}, {"limit": {"10"}, "deleted": {"true"}}} {
		if p := e.page(t, cookie, q); len(p.Files) != 0 || p.Counts.All != 0 {
			t.Errorf("another user's page %v = %+v", q, p)
		}
	}
}
