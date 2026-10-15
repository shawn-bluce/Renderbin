package server_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const qrWidgetTag = "renderbin-qr"

// qrNote returns the note the page's widget was given, and whether it has one.
func qrNote(body string) (string, bool) {
	i := strings.LastIndex(body, `})("`)
	if !strings.Contains(body, qrWidgetTag) || i < 0 {
		return "", false
	}
	note, _, _ := strings.Cut(body[i+4:], `"`)
	return note, true
}

func TestRenderQRWidget(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// On out of the box: migration 0007 seeds the row, since a missing
	// boolean config reads as false.
	if s := decodeJSON[map[string]bool](t, e.do(t, http.MethodGet, "/api/settings", "", cookie), http.StatusOK); !s["qr_widget"] {
		t.Fatalf("qr_widget = false on a fresh install: %v", s)
	}

	e.createRaw(t, "pub", "c", "<html><body><h1>hi</h1></body></html>", true)
	e.createRaw(t, "priv", "c", "<h1>secret</h1>", false)
	e.createRawKind(t, "md", "markdown", "# Title")
	e.createRawKind(t, "note", "txt", "plain")

	// A visitor's page carries it, inside the body; the source is otherwise
	// untouched.
	b := bodyOf(t, e.do(t, http.MethodGet, "/res/pub?code=c", "", nil), http.StatusOK)
	if note, ok := qrNote(b); !ok || note != "" {
		t.Errorf("visitor page: widget %v, note %q", ok, note)
	}
	if !strings.HasPrefix(b, "<html><body><h1>hi</h1><script>") || !strings.HasSuffix(b, "</script></body></html>") {
		t.Errorf("widget not placed before </body>: %q...%q", b[:40], b[len(b)-40:])
	}

	// The owner's view of a private file says the phone will need to sign in.
	b = bodyOf(t, e.do(t, http.MethodGet, "/res/priv", "", cookie), http.StatusOK)
	if note, ok := qrNote(b); !ok || note != "private" {
		t.Errorf("owner's private page: widget %v, note %q", ok, note)
	}

	// Rendered kinds too.
	for _, slug := range []string{"md", "note"} {
		b = bodyOf(t, e.do(t, http.MethodGet, "/res/"+slug+"?code=c", "", nil), http.StatusOK)
		if _, ok := qrNote(b); !ok {
			t.Errorf("%s page has no widget", slug)
		}
	}

	// Not on the denial page, and never in a download: that is the source.
	if b = bodyOf(t, e.do(t, http.MethodGet, "/res/pub?code=wrong", "", nil), http.StatusForbidden); strings.Contains(b, qrWidgetTag) {
		t.Errorf("403 page carries the widget")
	}
	if b = bodyOf(t, e.do(t, http.MethodGet, "/api/files/pub/download", "", cookie), http.StatusOK); b != "<html><body><h1>hi</h1></body></html>" {
		t.Errorf("download = %q, want the stored source", b)
	}

	// The super admin can turn it off, and pages are then served verbatim.
	if s := decodeJSON[map[string]bool](t, e.do(t, http.MethodPut, "/api/settings", `{"qr_widget":false}`, cookie), http.StatusOK); s["qr_widget"] {
		t.Fatalf("qr_widget still on after turning it off")
	}
	if b = bodyOf(t, e.do(t, http.MethodGet, "/res/pub?code=c", "", nil), http.StatusOK); b != "<html><body><h1>hi</h1></body></html>" {
		t.Errorf("widget off: body = %q", b)
	}

	// Nobody else can.
	_, otherCookie := e.newUser(t, "other")
	e.expect(t, http.StatusForbidden, http.MethodPut, "/api/settings", `{"qr_widget":true}`, otherCookie)
}

func TestSiteQRWidget(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	s := e.uploadSite(t, cookie, "", demoSite())
	e.publish(t, cookie, s.Slug)
	base := "/res/" + s.Slug + "/" + s.AccessCode + "/"

	get := func(path, dest string, c *http.Cookie) (*http.Response, string) {
		t.Helper()
		h := map[string]string{}
		if dest != "" {
			h["Sec-Fetch-Dest"] = dest
		}
		resp := e.raw(t, http.MethodGet, path, nil, c, h)
		return resp, bodyOf(t, resp, http.StatusOK)
	}

	// Every page of the site gets it, and says its bytes vary by how it was asked for.
	for _, p := range []string{"", "docs/", "docs/page.html"} {
		resp, b := get(base+p, "document", nil)
		if note, ok := qrNote(b); !ok || note != "" {
			t.Errorf("page %q: widget %v, note %q", p, ok, note)
		}
		if v := resp.Header.Values("Vary"); !strings.Contains(strings.Join(v, ","), "Sec-Fetch-Dest") {
			t.Errorf("page %q: Vary = %v", p, v)
		}
	}

	// HTML fetched by script is a fragment; stylesheets and the rest are not pages.
	if _, b := get(base+"docs/page.html", "empty", nil); b != "<p>page two</p>" {
		t.Errorf("fetched fragment = %q, want it verbatim", b)
	}
	resp, b := get(base+"css/site.css", "style", nil)
	if b != "body{color:#333}" || resp.Header.Get("Vary") != "" {
		t.Errorf("stylesheet = %q, Vary %q", b, resp.Header.Get("Vary"))
	}

	// The owner's preview link works on a phone too, but only for a while.
	redirect := e.raw(t, http.MethodGet, "/res/"+s.Slug, nil, cookie, nil)
	redirect.Body.Close()
	if _, b = get(redirect.Header.Get("Location"), "document", nil); !strings.Contains(b, `})("preview")`) {
		t.Errorf("preview page lacks the preview note")
	}
}

func TestQRCodeEndpoint(t *testing.T) {
	e := newEnv(t)

	// Public, as the sandboxed page asking for it sends no cookie.
	resp := e.do(t, http.MethodGet, "/api/qr?text="+url.QueryEscape("http://example.com/res/a?code=b"), "", nil)
	b := bodyOf(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.HasPrefix(b, "<svg ") || !strings.Contains(b, `<path fill="#000" d="M`) {
		t.Errorf("not an SVG QR code: %.80q", b)
	}

	e.expect(t, http.StatusBadRequest, http.MethodGet, "/api/qr", "", nil)
	e.expect(t, http.StatusBadRequest, http.MethodGet, "/api/qr?text="+strings.Repeat("a", 2049), "", nil)
}
