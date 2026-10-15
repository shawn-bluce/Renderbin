package server_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// ---------------------------------------------------------------------------
// Directory sites: upload, navigation, access control, counting, quota.
// ---------------------------------------------------------------------------

// makeSiteZip zips files (path -> content) the way a user's tool would.
func makeSiteZip(t testing.TB, files map[string]string) []byte {
	t.Helper()
	b, err := siteZip(files)
	if err != nil {
		t.Fatalf("build site zip: %v", err)
	}
	return b
}

func siteZip(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		w, err := zw.Create(p)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(files[p])); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mustSiteZip is siteZip for package-level test tables, which have no t.
func mustSiteZip(files map[string]string) string {
	b, err := siteZip(files)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// demoSite is a small site that exercises relative references in both
// directions: down into subdirectories and back up with "../".
func demoSite() map[string]string {
	return map[string]string{
		"demo/index.html":      `<!doctype html><title>Demo  Site</title><link rel=stylesheet href="css/site.css"><a href="docs/">docs</a><script type=module src="js/app.mjs"></script>`,
		"demo/docs/index.html": `<link rel=stylesheet href="../css/site.css"><a href="../index.html">home</a><a href="page.html">page</a>`,
		"demo/docs/page.html":  `<p>page two</p>`,
		"demo/css/site.css":    `body{color:#333}`,
		"demo/js/app.mjs":      `fetch('../data.json')`,
		"demo/data.json":       `{"ok":true}`,
		"demo/中文/页面.html":      `<p>你好</p>`,
		"demo/legacy.html":     "<meta charset=gbk>\xc4\xe3\xba\xc3",
		"demo/LICENSE":         `MIT`,
		"__MACOSX/demo/._x":    `junk`,
	}
}

type siteResp struct {
	fileResp
	Size    int64 `json:"size"`
	Entries []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"entries"`
}

func decodeSite(t *testing.T, resp *http.Response) siteResp {
	t.Helper()
	defer resp.Body.Close()
	var s siteResp
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatalf("decode site response: %v", err)
	}
	return s
}

// raw sends a request with a binary body and optional extra headers, without
// following redirects, so redirect chains can be asserted hop by hop.
func (e *testEnv) raw(t *testing.T, method, path string, body []byte, cookie *http.Cookie, headers map[string]string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	return e.send(t, method, path, r, cookie, headers, false)
}

func (e *testEnv) uploadSite(t *testing.T, cookie *http.Cookie, name string, files map[string]string) siteResp {
	t.Helper()
	path := "/api/sites"
	if name != "" {
		path += "?name=" + url.QueryEscape(name)
	}
	resp := e.raw(t, http.MethodPost, path, makeSiteZip(t, files), cookie,
		map[string]string{"Content-Type": "application/zip"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload site: status %d, body %q", resp.StatusCode, bodyString(t, resp))
	}
	return decodeSite(t, resp)
}

// fileRow reads a live file's row straight from the database, counters and
// stored sizes included.
func (e *testEnv) fileRow(t *testing.T, slug string) sqlcgen.File {
	t.Helper()
	f, err := e.queries.GetFileBySlugAnyOwner(context.Background(), slug)
	if err != nil {
		t.Fatalf("read row %q: %v", slug, err)
	}
	return f
}

// archiveSize is the byte length of a site row's stored zip.
func archiveSize(f sqlcgen.File) int64 { return int64(len(f.Archive)) }

func wantRedirect(t *testing.T, resp *http.Response, location string) {
	t.Helper()
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 to %s", resp.StatusCode, location)
	}
	if got := resp.Header.Get("Location"); got != location {
		t.Fatalf("Location = %q, want %q", got, location)
	}
}

func TestSiteUploadAndNavigation(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	s := e.uploadSite(t, cookie, "", demoSite())
	if s.Kind != "site" {
		t.Errorf("kind = %q, want site", s.Kind)
	}
	if s.Name != "Demo Site" {
		t.Errorf("name = %q, want the <title> of index.html", s.Name)
	}
	if u := e.uploadSite(t, cookie, "", map[string]string{"index.html": "<p>hi</p>"}); u.Name != "Untitled" {
		t.Errorf("name without <title> = %q, want Untitled", u.Name)
	}
	// The wrapping "demo/" directory is stripped and __MACOSX dropped.
	if len(s.Entries) != 9 {
		t.Errorf("entries = %+v, want 9", s.Entries)
	}
	if s.IsPublic {
		t.Error("a new site should follow upload_default_public (off)")
	}
	e.publish(t, cookie, s.Slug)
	code := s.AccessCode
	base := "/res/" + s.Slug + "/" + code + "/"

	// The share link format is unchanged: ?code= hops into the path.
	wantRedirect(t, e.raw(t, http.MethodGet, "/res/"+s.Slug+"?code="+code, nil, nil, nil), base)
	// A missing trailing slash would break every relative reference.
	wantRedirect(t, e.raw(t, http.MethodGet, "/res/"+s.Slug+"/"+code, nil, nil, nil), base)
	// So would a directory without one.
	wantRedirect(t, e.raw(t, http.MethodGet, base+"docs", nil, nil, nil), base+"docs/")

	for _, c := range []struct {
		path, ct, body string
	}{
		{"", "text/html; charset=utf-8", "Demo  Site"},
		{"index.html", "text/html; charset=utf-8", "Demo  Site"},
		{"docs/", "text/html; charset=utf-8", "../css/site.css"},
		{"docs/page.html", "text/html; charset=utf-8", "page two"},
		{"css/site.css", "text/css; charset=utf-8", "color:#333"},
		{"js/app.mjs", "text/javascript; charset=utf-8", "fetch"},
		{"data.json", "application/json; charset=utf-8", `"ok"`},
		{url.PathEscape("中文") + "/" + url.PathEscape("页面.html"), "text/html; charset=utf-8", "你好"},
		// Not UTF-8: no charset in the header, so the page's own <meta> wins.
		{"legacy.html", "text/html", "charset=gbk"},
		{"LICENSE", "application/octet-stream", "MIT"},
	} {
		resp := e.raw(t, http.MethodGet, base+c.path, nil, nil, nil)
		body := bodyString(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", c.path, resp.StatusCode)
			continue
		}
		if got := resp.Header.Get("Content-Type"); got != c.ct {
			t.Errorf("%s: Content-Type = %q, want %q", c.path, got, c.ct)
		}
		if !strings.Contains(body, c.body) {
			t.Errorf("%s: body %q does not contain %q", c.path, body, c.body)
		}
		h := resp.Header
		if csp := h.Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox") || strings.Contains(csp, "allow-same-origin") {
			t.Errorf("%s: CSP = %q, want the sandbox without allow-same-origin", c.path, csp)
		}
		if got := h.Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want *", c.path, got)
		}
		if got := h.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", c.path, got)
		}
		if got := h.Get("X-Frame-Options"); got != "" {
			t.Errorf("%s: X-Frame-Options = %q, want unset like any shared document", c.path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s: Referrer-Policy = %q", c.path, got)
		}
	}

	// Paths are exact, case included; a missing entry is a 404 inside the
	// site (sandboxed, with CORS so a fetch() can see the status).
	for _, p := range []string{"nope.html", "INDEX.HTML", "docs/missing/"} {
		resp := e.raw(t, http.MethodGet, base+p, nil, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, resp.StatusCode)
		}
	}

	// Conditional requests revalidate instead of re-downloading.
	first := e.raw(t, http.MethodGet, base+"css/site.css", nil, nil, nil)
	first.Body.Close()
	lm := first.Header.Get("Last-Modified")
	if lm == "" {
		t.Fatal("no Last-Modified on a site entry")
	}
	resp := e.raw(t, http.MethodGet, base+"css/site.css", nil, nil, map[string]string{"If-Modified-Since": lm})
	resp.Body.Close()
	assertStatus(t, resp, http.StatusNotModified)

	// The CORS header is for site entries only, never the API.
	api := e.do(t, http.MethodGet, "/api/files", "", cookie)
	api.Body.Close()
	if got := api.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("/api/files carries Access-Control-Allow-Origin = %q", got)
	}

	// GET /api/files/{slug} lists entries instead of returning content.
	got := decodeSite(t, e.do(t, http.MethodGet, "/api/files/"+s.Slug, "", cookie))
	if got.HTMLContent != "" || len(got.Entries) != 9 {
		t.Errorf("get site: html_content %q, %d entries", got.HTMLContent, len(got.Entries))
	}
	if got.Size <= 0 {
		t.Errorf("get site: size = %d", got.Size)
	}

	// Download hands back the stored zip, which reads back as the same site.
	dl := e.do(t, http.MethodGet, "/api/files/"+s.Slug+"/download", "", cookie)
	if ct := dl.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("download Content-Type = %q", ct)
	}
	if cd := dl.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="Demo Site.zip"`) {
		t.Errorf("download Content-Disposition = %q", cd)
	}
	zb, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil || len(zr.File) != 9 {
		t.Fatalf("downloaded zip: %v, %d files", err, len(zr.File))
	}
}

func TestSiteAccessControl(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	_, otherCookie := e.newUser(t, "other")

	s := e.uploadSite(t, cookie, "private site", demoSite())
	code := s.AccessCode

	// Private: the right code is not enough, by either URL.
	for _, p := range []string{"/res/" + s.Slug + "?code=" + code, "/res/" + s.Slug + "/" + code + "/", "/res/" + s.Slug + "/" + code + "/css/site.css"} {
		resp := e.raw(t, http.MethodGet, p, nil, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("private %s: status %d, want 403", p, resp.StatusCode)
		}
	}

	// The owner is sent into the site with a preview token -- from the bare
	// URL and from a share link with the access code alike (the dashboard's
	// "open" link is the share link, and its hop must land on a URL ending in "/").
	prefix := "/res/" + s.Slug + "/~"
	var previewBase string
	for _, p := range []string{"/res/" + s.Slug, "/res/" + s.Slug + "?code=" + code} {
		resp := e.raw(t, http.MethodGet, p, nil, cookie, nil)
		resp.Body.Close()
		assertStatus(t, resp, http.StatusFound)
		loc := resp.Header.Get("Location")
		if !strings.HasPrefix(loc, prefix) || !strings.HasSuffix(loc, "/") {
			t.Fatalf("owner redirect for %s = %q, want %s.../", p, loc, prefix)
		}
		previewBase = loc
	}

	// The preview URL works with no cookie at all -- which is how the
	// browser will actually fetch the page's subresources.
	before := e.fileRow(t, s.Slug)
	for _, p := range []string{"", "css/site.css", "docs/page.html"} {
		resp := e.raw(t, http.MethodGet, previewBase+p, nil, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("preview %q: status %d", p, resp.StatusCode)
		}
	}
	after := e.fileRow(t, s.Slug)
	// Two pages viewed, one stylesheet: only the pages count.
	if after.SuccessCount-before.SuccessCount != 2 {
		t.Errorf("success_count moved by %d, want 2 (pages only)", after.SuccessCount-before.SuccessCount)
	}

	// The site route ignores cookies: the owner's session with a wrong
	// token is still refused.
	resp := e.raw(t, http.MethodGet, "/res/"+s.Slug+"/wrong/", nil, cookie, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusForbidden)

	// Another account's session gets nothing from the hop.
	resp = e.raw(t, http.MethodGet, "/res/"+s.Slug, nil, otherCookie, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusForbidden)

	// A tampered token fails; so does a valid token used on another site.
	tok := strings.TrimSuffix(strings.TrimPrefix(previewBase, prefix), "/")
	flipped := []byte(tok)
	if flipped[5] == 'A' {
		flipped[5] = 'B'
	} else {
		flipped[5] = 'A'
	}
	resp = e.raw(t, http.MethodGet, prefix+string(flipped)+"/", nil, nil, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusForbidden)
	s2 := e.uploadSite(t, cookie, "second", demoSite())
	resp = e.raw(t, http.MethodGet, "/res/"+s2.Slug+"/~"+tok+"/", nil, nil, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusForbidden)

	// A password reset revokes outstanding preview links.
	if _, err := e.queries.UpdateUserPassword(context.Background(), sqlcgen.UpdateUserPasswordParams{
		PasswordHash: "$2a$10$differentdifferentdifferentdifferentdifferentdiffere", ID: e.admin.ID,
	}); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}
	resp = e.raw(t, http.MethodGet, previewBase, nil, nil, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusForbidden)

	// Public now: the code works; the old owner path is unaffected.
	e.publish(t, cookie, s.Slug)
	resp = e.raw(t, http.MethodGet, "/res/"+s.Slug+"/"+code+"/", nil, nil, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusOK)

	// Trashed: 404 for every URL, owner preview included.
	resp = e.do(t, http.MethodDelete, "/api/files/"+s.Slug, "", cookie)
	resp.Body.Close()
	for _, p := range []string{"/res/" + s.Slug + "/" + code + "/", "/res/" + s.Slug + "?code=" + code} {
		resp := e.raw(t, http.MethodGet, p, nil, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("trashed %s: status %d, want 404", p, resp.StatusCode)
		}
	}

	// A suspended owner's site goes offline like any of their files.
	other, _ := e.newUser(t, "third")
	otherKey := e.apiKeyFor(t, other.ID)
	up := e.raw(t, http.MethodPost, "/api/sites", makeSiteZip(t, demoSite()), nil,
		map[string]string{"Authorization": "Bearer " + otherKey})
	if up.StatusCode != http.StatusCreated {
		t.Fatalf("upload with API key: %d %s", up.StatusCode, bodyString(t, up))
	}
	theirs := decodeSite(t, up)
	if _, err := e.conn.Exec(`UPDATE files SET is_public = 1 WHERE slug = ?`, theirs.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := e.queries.DisableUser(context.Background(), other.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	resp = e.raw(t, http.MethodGet, "/res/"+theirs.Slug+"/"+theirs.AccessCode+"/", nil, nil, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusNotFound)

	// A document is not a site: /res/{doc}/x is a plain 404 that keeps the
	// app's clickjacking header.
	e.createRaw(t, "plaindoc", "code", "<p>x</p>", true)
	resp = e.raw(t, http.MethodGet, "/res/plaindoc/code/", nil, nil, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusNotFound)
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
}

func TestSiteViewCounting(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	s := e.uploadSite(t, cookie, "counted", demoSite())
	e.expect(t, http.StatusOK, http.MethodPatch, "/api/files/"+s.Slug+"/expiry", `{"max_views":1}`, cookie)
	base := "/res/" + s.Slug + "/" + s.AccessCode + "/"

	get := func(path, dest string) int {
		t.Helper()
		h := map[string]string{}
		if dest != "" {
			h["Sec-Fetch-Dest"] = dest
		}
		resp := e.raw(t, http.MethodGet, base+path, nil, nil, h)
		resp.Body.Close()
		return resp.StatusCode
	}

	// The first visit: the page and everything it pulls in all load,
	// including an HTML fragment fetched by script, which is not a view.
	for _, c := range []struct{ path, dest string }{
		{"", "document"},
		{"css/site.css", "style"},
		{"js/app.mjs", "script"},
		{"data.json", "empty"},
		{"docs/page.html", "empty"},
	} {
		if got := get(c.path, c.dest); got != http.StatusOK {
			t.Errorf("first visit %q (%s): status %d, want 200", c.path, c.dest, got)
		}
	}
	row := e.fileRow(t, s.Slug)
	if row.ViewCount != 1 || row.CodeSuccessCount != 1 {
		t.Errorf("view_count=%d code_success_count=%d, want 1/1", row.ViewCount, row.CodeSuccessCount)
	}

	// The next page view is over the limit: the link expires...
	if got := get("docs/page.html", "document"); got != http.StatusForbidden {
		t.Errorf("second page view: status %d, want 403", got)
	}
	row = e.fileRow(t, s.Slug)
	if row.IsPublic || row.ExpiredReason != "views" {
		t.Errorf("after the limit: public=%v reason=%q", row.IsPublic, row.ExpiredReason)
	}
	// ...and with it every resource.
	if got := get("css/site.css", "style"); got != http.StatusForbidden {
		t.Errorf("resource after expiry: status %d, want 403", got)
	}

	// A wrong token counts as a failure only for page requests.
	before := e.fileRow(t, s.Slug).FailureCount
	resp := e.raw(t, http.MethodGet, "/res/"+s.Slug+"/bad/css/site.css", nil, nil, map[string]string{"Sec-Fetch-Dest": "style"})
	resp.Body.Close()
	resp = e.raw(t, http.MethodGet, "/res/"+s.Slug+"/bad/", nil, nil, nil)
	resp.Body.Close()
	if got := e.fileRow(t, s.Slug).FailureCount - before; got != 1 {
		t.Errorf("failure_count moved by %d, want 1", got)
	}

	// A deadline applies to every request, resources included.
	ttl := e.uploadSite(t, cookie, "ttl", demoSite())
	e.publish(t, cookie, ttl.Slug)
	if _, err := e.conn.Exec(`UPDATE files SET expires_at = ? WHERE slug = ?`,
		time.Now().Add(-time.Minute).UTC(), ttl.Slug); err != nil {
		t.Fatal(err)
	}
	resp = e.raw(t, http.MethodGet, "/res/"+ttl.Slug+"/"+ttl.AccessCode+"/css/site.css", nil, nil, map[string]string{"Sec-Fetch-Dest": "style"})
	resp.Body.Close()
	assertStatus(t, resp, http.StatusForbidden)
	if r := e.fileRow(t, ttl.Slug); r.IsPublic || r.ExpiredReason != "ttl" {
		t.Errorf("after the deadline: public=%v reason=%q", r.IsPublic, r.ExpiredReason)
	}
}

// TestSiteRevalidationSkipsArchive pins that a conditional request is answered
// from the row's metadata, without loading the zip. Every asset of a site
// revalidates on every visit (Cache-Control: no-cache), so loading a multi-MB
// archive to answer each 304 was the bulk of a repeat visit's cost. The
// stored archive is replaced with garbage behind the handler's back: a
// request that loads it fails, so a 304 proves it was never read.
func TestSiteRevalidationSkipsArchive(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	s := e.uploadSite(t, cookie, "cached", demoSite())
	e.publish(t, cookie, s.Slug)
	base := "/res/" + s.Slug + "/" + s.AccessCode + "/"

	first := e.raw(t, http.MethodGet, base+"css/site.css", nil, nil, nil)
	first.Body.Close()
	assertStatus(t, first, http.StatusOK)
	lm := first.Header.Get("Last-Modified")

	// Writing archive alone leaves updated_at as it was.
	if _, err := e.conn.Exec(`UPDATE files SET archive = x'00' WHERE slug = ?`, s.Slug); err != nil {
		t.Fatal(err)
	}

	ims := map[string]string{"If-Modified-Since": lm, "Sec-Fetch-Dest": "style"}
	resp := e.raw(t, http.MethodGet, base+"css/site.css", nil, nil, ims)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusNotModified)
	h := resp.Header
	if !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("304 without the sandbox CSP: %q", h.Get("Content-Security-Policy"))
	}
	if h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Cache-Control") != "no-cache" {
		t.Errorf("304 headers: ACAO %q, Cache-Control %q", h.Get("Access-Control-Allow-Origin"), h.Get("Cache-Control"))
	}
	if h.Get("Last-Modified") != lm {
		t.Errorf("304 Last-Modified = %q, want %q", h.Get("Last-Modified"), lm)
	}

	// A revalidated page view still counts, as it did before.
	before := e.fileRow(t, s.Slug).CodeSuccessCount
	ims["Sec-Fetch-Dest"] = "document"
	resp = e.raw(t, http.MethodGet, base, nil, nil, ims)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusNotModified)
	if got := e.fileRow(t, s.Slug).CodeSuccessCount - before; got != 1 {
		t.Errorf("code_success_count moved by %d on a revalidated page view, want 1", got)
	}

	// Without the condition the archive is read -- and is garbage.
	resp = e.raw(t, http.MethodGet, base+"css/site.css", nil, nil, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusInternalServerError)

	// A site replaced since the browser's copy is served in full, with the
	// new bytes and the new date.
	later := time.Now().Add(time.Hour).UTC()
	fresh := makeSiteZip(t, map[string]string{"index.html": "<p>new</p>", "css/site.css": "body{color:red}"})
	if _, err := e.conn.Exec(`UPDATE files SET archive = ?, updated_at = ? WHERE slug = ?`, fresh, later, s.Slug); err != nil {
		t.Fatal(err)
	}
	ims["Sec-Fetch-Dest"] = "style"
	resp = e.raw(t, http.MethodGet, base+"css/site.css", nil, nil, ims)
	assertStatus(t, resp, http.StatusOK)
	if body := bodyString(t, resp); body != "body{color:red}" {
		t.Errorf("replaced site served %q", body)
	}
	if got := resp.Header.Get("Last-Modified"); got != later.Format(http.TimeFormat) {
		t.Errorf("Last-Modified = %q, want %q", got, later.Format(http.TimeFormat))
	}
}

func TestSiteStorageAndEdits(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	s := e.uploadSite(t, cookie, "edits", demoSite())

	// content_size is the compressed archive, i.e. what the database holds.
	row := e.fileRow(t, s.Slug)
	if row.ContentSize != archiveSize(row) || row.ContentSize == 0 {
		t.Errorf("content_size = %d, archive = %d bytes", row.ContentSize, archiveSize(row))
	}
	if row.HtmlContent != "" {
		t.Errorf("html_content = %q, want empty for a site", row.HtmlContent)
	}
	size := row.ContentSize

	// PATCH cannot write a document into a site.
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+s.Slug,
		`{"name":"x","slug":"`+s.Slug+`","html_content":"<p>x</p>","access_code":"`+s.AccessCode+`"}`, cookie)
	// Without content it renames, re-slugs and re-codes, and the size holds.
	e.expect(t, http.StatusOK, http.MethodPatch, "/api/files/"+s.Slug,
		`{"name":"renamed","slug":"my-site","access_code":"newcode"}`, cookie)
	if r := e.fileRow(t, "my-site"); r.ContentSize != size || archiveSize(r) != size {
		t.Errorf("after PATCH: content_size=%d archive=%d, want %d", r.ContentSize, archiveSize(r), size)
	}

	// Dot segments are refused as slugs and codes: the browser would drop
	// them from the URL. This applies to documents too.
	doc := e.createViaAPI(t, cookie, "doc", "<p>d</p>")
	for _, body := range []string{
		`{"name":"x","slug":"..","access_code":"abc"}`,
		`{"name":"x","slug":"ok-slug","access_code":"."}`,
	} {
		e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/my-site", body, cookie)
	}
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+doc.Slug, `{"name":"x","slug":"..","html_content":"<p>x</p>","access_code":"abc"}`, cookie)

	// Replacing the whole site.
	put := e.raw(t, http.MethodPut, "/api/files/my-site/site",
		makeSiteZip(t, map[string]string{"index.html": "<p>v2</p>"}), cookie, nil)
	if put.StatusCode != http.StatusOK {
		t.Fatalf("replace: %d %s", put.StatusCode, bodyString(t, put))
	}
	replaced := decodeSite(t, put)
	if replaced.Name != "renamed" || len(replaced.Entries) != 1 {
		t.Errorf("replace: name %q, %d entries", replaced.Name, len(replaced.Entries))
	}
	resp := e.raw(t, http.MethodGet, "/res/my-site/newcode/", nil, nil, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusForbidden) // still private
	e.publish(t, cookie, "my-site")
	resp = e.raw(t, http.MethodGet, "/res/my-site/newcode/", nil, nil, nil)
	if body := bodyString(t, resp); !strings.Contains(body, "v2") {
		t.Errorf("after replace: %q", body)
	}
	if r := e.fileRow(t, "my-site"); r.ContentSize != archiveSize(r) {
		t.Errorf("after replace: content_size=%d archive=%d", r.ContentSize, archiveSize(r))
	}

	// A document cannot be replaced with a zip.
	resp = e.raw(t, http.MethodPut, "/api/files/"+doc.Slug+"/site", makeSiteZip(t, map[string]string{"index.html": "x"}), cookie, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusBadRequest)

	// Bad uploads say what is wrong.
	for _, c := range []struct {
		name   string
		body   []byte
		status int
		msg    string
	}{
		{"no index", makeSiteZip(t, map[string]string{"a.html": "x"}), http.StatusBadRequest, "index.html"},
		{"not a zip", []byte("hello"), http.StatusBadRequest, "zip"},
		{"traversal", makeSiteZip(t, map[string]string{"index.html": "x", "../evil": "x"}), http.StatusBadRequest, ".."},
		{"oversized body", make([]byte, 8<<20+1), http.StatusRequestEntityTooLarge, "8 MB"},
	} {
		resp := e.raw(t, http.MethodPost, "/api/sites", c.body, cookie, nil)
		body := bodyString(t, resp)
		if resp.StatusCode != c.status || !strings.Contains(body, c.msg) {
			t.Errorf("%s: %d %q, want %d mentioning %q", c.name, resp.StatusCode, body, c.status, c.msg)
		}
	}

	// The quota counts the archive, for creates and replacements alike.
	if _, err := e.queries.UpdateUserQuota(context.Background(), sqlcgen.UpdateUserQuotaParams{
		QuotaBytes: 1, ID: e.admin.ID,
	}); err != nil {
		t.Fatal(err)
	}
	resp = e.raw(t, http.MethodPost, "/api/sites", makeSiteZip(t, demoSite()), cookie, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusRequestEntityTooLarge)
	resp = e.raw(t, http.MethodPut, "/api/files/my-site/site", makeSiteZip(t, demoSite()), cookie, nil)
	resp.Body.Close()
	assertStatus(t, resp, http.StatusRequestEntityTooLarge)
}

func TestSiteSurvivesBackupRestore(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	s := e.uploadSite(t, cookie, "backed up", demoSite())
	before := e.fileRow(t, s.Slug)
	snapshot := e.downloadBackup(t, cookie)

	resp := e.do(t, http.MethodDelete, "/api/files/"+s.Slug, "", cookie)
	resp.Body.Close()
	resp = e.do(t, http.MethodDelete, "/api/files/"+s.Slug+"/permanent", "", cookie)
	resp.Body.Close()

	restored := e.restore(t, snapshot, cookie)
	assertStatus(t, restored, http.StatusOK)
	restored.Body.Close()

	after := e.fileRow(t, s.Slug)
	if archiveSize(after) != archiveSize(before) || after.ContentSize != before.ContentSize || after.ContentSize == 0 {
		t.Errorf("restored archive=%d size=%d, want archive=%d size=%d",
			archiveSize(after), after.ContentSize, archiveSize(before), before.ContentSize)
	}
	if _, err := e.conn.Exec(`UPDATE files SET is_public = 1 WHERE slug = ?`, s.Slug); err != nil {
		t.Fatal(err)
	}
	resp = e.raw(t, http.MethodGet, "/res/"+s.Slug+"/"+s.AccessCode+"/docs/page.html", nil, nil, nil)
	if body := bodyString(t, resp); !strings.Contains(body, "page two") {
		t.Errorf("restored site serves %q", body)
	}
}

func TestSiteTokensAreRedactedInLogs(t *testing.T) {
	e := newBareEnv(t)
	for _, p := range []string{"/res/abc/SECRETCODE/css/a.css", "/res/abc/~SECRETPREVIEW/", "/res/abc/SECRETBARE"} {
		e.expect(t, http.StatusNotFound, http.MethodGet, p, "", nil)
	}
	out := e.logs.String()
	if strings.Contains(out, "SECRET") {
		t.Errorf("a site token reached the log:\n%s", out)
	}
	if !strings.Contains(out, "/res/abc/***/css/a.css") {
		t.Errorf("redacted path missing from log:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// MCP: upload_site and update_site.
// ---------------------------------------------------------------------------

func mcpFiles(files map[string]string) []map[string]any {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]map[string]any, 0, len(files))
	for _, p := range paths {
		out = append(out, map[string]any{"path": p, "content": files[p]})
	}
	return out
}

func TestMCPSiteUploadAndUpdate(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	res := e.mcpOK(t, key, "upload_site", map[string]any{
		"files": mcpFiles(map[string]string{
			"index.html":  `<title>Agent Site</title><a href="b.html">b</a>`,
			"b.html":      `<link rel=stylesheet href="s.css"><p>bee</p>`,
			"s.css":       `p{}`,
			"old/x.html":  `<p>old</p>`,
			"docs/a.html": `<p>a</p>`,
		}),
	})
	out := res.StructuredContent
	slug, _ := out["slug"].(string)
	siteURL, _ := out["url"].(string)
	if out["kind"] != "site" || out["name"] != "Agent Site" || out["is_public"] != false {
		t.Errorf("upload_site output = %v", out)
	}
	if files, _ := out["files"].([]any); len(files) != 5 {
		t.Errorf("upload_site files = %v", out["files"])
	}

	// Published through the ordinary tool, the share URL hops into the site.
	res = e.mcpOK(t, key, "publish_file", map[string]any{"slug": slug})
	resp := e.do(t, http.MethodGet, e.pathOf(t, siteURL), "", nil) // follows redirects
	if body := bodyString(t, resp); !strings.Contains(body, "Agent Site") {
		t.Errorf("share URL serves %q", body)
	}

	// A patch touches only what it names.
	res = e.mcpOK(t, key, "update_site", map[string]any{
		"slug":   slug,
		"name":   "Renamed Site",
		"put":    mcpFiles(map[string]string{"b.html": `<p>bee v2</p>`, "c.html": `<p>sea</p>`}),
		"delete": []string{"old/x.html"},
	})
	if res.StructuredContent["name"] != "Renamed Site" {
		t.Errorf("update_site name = %v", res.StructuredContent["name"])
	}
	base := "/res/" + slug + "/" + strings.SplitN(siteURL, "code=", 2)[1] + "/"
	for p, want := range map[string]int{"b.html": 200, "c.html": 200, "s.css": 200, "docs/a.html": 200, "old/x.html": 404} {
		resp := e.raw(t, http.MethodGet, base+p, nil, nil, nil)
		body := bodyString(t, resp)
		if resp.StatusCode != want {
			t.Errorf("%s after update: %d, want %d", p, resp.StatusCode, want)
		}
		if p == "b.html" && !strings.Contains(body, "bee v2") {
			t.Errorf("b.html after update = %q", body)
		}
	}
	if r := e.fileRow(t, slug); r.ContentSize != archiveSize(r) {
		t.Errorf("after update_site: content_size=%d archive=%d", r.ContentSize, archiveSize(r))
	}

	// Refusals arrive as tool errors that say what to do.
	sizeBefore := e.fileRow(t, slug).ContentSize
	doc := e.mcpUpload(t, key, "a doc", "markdown", "# doc")
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"update_site", map[string]any{"slug": slug, "delete": []string{"index.html"}}, "index.html"},
		{"update_site", map[string]any{"slug": slug, "delete": []string{"ghost.html"}}, "does not exist"},
		{"update_site", map[string]any{"slug": slug}, "provide"},
		{"update_site", map[string]any{"slug": doc["slug"], "put": mcpFiles(map[string]string{"x.html": "x"})}, "update_file"},
		{"update_file", map[string]any{"slug": slug, "name": "sneaky"}, "update_site"},
		{"update_file", map[string]any{"slug": slug, "content": "<p>x</p>"}, "update_site"},
		{"upload_site", map[string]any{"files": mcpFiles(map[string]string{"a.html": "x"})}, "index.html"},
		{"upload_site", map[string]any{"files": mcpFiles(map[string]string{"index.html": "x", "/abs.html": "x"})}, "absolute"},
	} {
		res := e.mcp(t, key, c.tool, c.args)
		if !res.IsError || !strings.Contains(res.text(), c.want) {
			t.Errorf("%s %v: isError=%v %q, want an error mentioning %q", c.tool, c.args, res.IsError, res.text(), c.want)
		}
	}
	if r := e.fileRow(t, slug); r.ContentSize != sizeBefore || r.HtmlContent != "" {
		t.Errorf("refused calls changed the site: size %d -> %d, html_content %q", sizeBefore, r.ContentSize, r.HtmlContent)
	}

	// list_files reports the kind.
	res = e.mcp(t, key, "list_files", map[string]any{})
	if !strings.Contains(res.text(), "[site,") {
		t.Errorf("list_files does not show the site's kind: %s", res.text())
	}

	// Another account's site is "not found", exactly like a missing slug.
	other, _ := e.newUser(t, "other")
	otherKey := e.apiKeyFor(t, other.ID)
	res = e.mcp(t, otherKey, "update_site", map[string]any{
		"slug": slug, "put": mcpFiles(map[string]string{"index.html": "pwned"}),
	})
	if !res.IsError || !strings.Contains(res.text(), "not found") {
		t.Errorf("foreign update_site: %q", res.text())
	}

	// The quota applies to both tools.
	if _, err := e.queries.UpdateUserQuota(context.Background(), sqlcgen.UpdateUserQuotaParams{QuotaBytes: 1, ID: e.admin.ID}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"upload_site", map[string]any{"files": mcpFiles(map[string]string{"index.html": "x"})}},
		{"update_site", map[string]any{"slug": slug, "put": mcpFiles(map[string]string{"big.txt": strings.Repeat("z", 4096)})}},
	} {
		res := e.mcp(t, key, c.tool, c.args)
		if !res.IsError || !strings.Contains(res.text(), "quota") {
			t.Errorf("%s over quota: %q", c.tool, res.text())
		}
	}
}
