package server_test

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
	"github.com/shawn-bluce/renderbin/backend/internal/server"
)

const (
	testUser = "admin"
	testPass = "s3cret"
)

// bcrypt is deliberately slow (~50ms) and nearly every test seeds an account,
// but the tests only ever verify a hash, so one per password per test binary is
// enough.
var (
	adminHash = sync.OnceValue(func() string { return mustHash(testPass) })
	userHash  = sync.OnceValue(func() string { return mustHash("secret2") })
)

func mustHash(pw string) string {
	h, err := auth.HashPassword(pw)
	if err != nil {
		panic(err)
	}
	return h
}

// syncBuffer is a log sink the server goroutines and the test can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type testEnv struct {
	srv     *httptest.Server
	queries *sqlcgen.Queries
	conn    *sql.DB
	blobs   *blobstore.Store
	admin   sqlcgen.User
	logs    *syncBuffer // everything the server logged
}

// newBareEnv starts a server against an empty database — no users yet, the
// state the first-run /api/setup flow expects.
func newBareEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	blobs, err := blobstore.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("blobstore.Open: %v", err)
	}
	conn, err := db.Open(filepath.Join(dir, "app.db"), blobs)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	logs := &syncBuffer{}
	srv := httptest.NewServer(server.New(conn, blobs, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)

	return &testEnv{srv: srv, queries: sqlcgen.New(conn), conn: conn, blobs: blobs, logs: logs}
}

// newEnv additionally seeds the super admin (id=1, testUser/testPass), the
// state every post-setup test wants.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newBareEnv(t)
	admin, err := e.queries.CreateUser(context.Background(), sqlcgen.CreateUserParams{
		Username:     testUser,
		Nickname:     "Admin",
		PasswordHash: adminHash(),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	e.admin = admin
	return e
}

// cookieFor inserts a valid session for userID and returns the cookie to send
// with it. The real cookie is Secure, so a plain-HTTP test client's jar won't
// resend it — we attach it explicitly instead.
func (e *testEnv) cookieFor(t *testing.T, userID int64) *http.Cookie {
	t.Helper()
	tok, err := auth.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if err := e.queries.CreateSession(context.Background(), sqlcgen.CreateSessionParams{
		Token:     tok,
		UserID:    userID,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: tok}
}

// authCookie is a session for the seeded super admin.
func (e *testEnv) authCookie(t *testing.T) *http.Cookie {
	t.Helper()
	return e.cookieFor(t, e.admin.ID)
}

// newUser creates an ordinary (non-super-admin) account and a session for it.
// Owner-isolation tests need a second identity; keeping this in one place
// stops a copy from quietly reusing the admin's cookie and passing vacuously.
func (e *testEnv) newUser(t *testing.T, username string) (sqlcgen.User, *http.Cookie) {
	t.Helper()
	user, err := e.queries.CreateUser(context.Background(), sqlcgen.CreateUserParams{
		Username:     username,
		Nickname:     username,
		PasswordHash: userHash(),
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	return user, e.cookieFor(t, user.ID)
}

// send is the one place a request is built. follow=false stops at the first
// redirect so a chain can be asserted hop by hop.
func (e *testEnv) send(t *testing.T, method, path string, body io.Reader, cookie *http.Cookie,
	header map[string]string, follow bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	client := *e.srv.Client()
	if !follow {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// do sends a JSON request (body "" means none) with an optional session cookie.
func (e *testEnv) do(t *testing.T, method, path, body string, cookie *http.Cookie) *http.Response {
	t.Helper()
	if body == "" {
		return e.send(t, method, path, nil, cookie, nil, true)
	}
	return e.send(t, method, path, strings.NewReader(body), cookie,
		map[string]string{"Content-Type": "application/json"}, true)
}

// expect is do for the many calls whose only interest is the status.
func (e *testEnv) expect(t *testing.T, want int, method, path, body string, cookie *http.Cookie) {
	t.Helper()
	resp := e.do(t, method, path, body, cookie)
	defer resp.Body.Close()
	assertStatus(t, resp, want)
}

// expectKeyGET is expect for a GET authenticated by a Bearer API key instead
// of a session cookie.
func (e *testEnv) expectKeyGET(t *testing.T, want int, path, key string) {
	t.Helper()
	resp := e.doKey(t, http.MethodGet, path, "", key)
	defer resp.Body.Close()
	assertStatus(t, resp, want)
}

type fileResp struct {
	Slug             string  `json:"slug"`
	Name             string  `json:"name"`
	Kind             string  `json:"kind"`
	HTMLContent      string  `json:"html_content"`
	IsPublic         bool    `json:"is_public"`
	AccessCode       string  `json:"access_code"`
	Tags             string  `json:"tags"`
	SuccessCount     int64   `json:"success_count"`
	CodeSuccessCount int64   `json:"code_success_count"`
	FailureCount     int64   `json:"failure_count"`
	ExpiresAt        *string `json:"expires_at"`
	MaxViews         *int64  `json:"max_views"`
	ViewCount        int64   `json:"view_count"`
	ExpiredAt        *string `json:"expired_at"`
	ExpiredReason    string  `json:"expired_reason"`
}

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Errorf("status = %d, want %d", resp.StatusCode, want)
	}
}

// decodeJSON checks the status, decodes the body as T and closes it.
func decodeJSON[T any](t *testing.T, resp *http.Response, want int) T {
	t.Helper()
	defer resp.Body.Close()
	assertStatus(t, resp, want)
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode %T: %v", v, err)
	}
	return v
}

func decodeFile(t *testing.T, resp *http.Response, want int) fileResp {
	t.Helper()
	return decodeJSON[fileResp](t, resp, want)
}

// bodyOf checks the status and returns the body as text.
func bodyOf(t *testing.T, resp *http.Response, want int) string {
	t.Helper()
	assertStatus(t, resp, want)
	return bodyString(t, resp)
}

func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// publish makes a file public through the visibility endpoint.
func (e *testEnv) publish(t *testing.T, cookie *http.Cookie, slug string) {
	t.Helper()
	e.expect(t, http.StatusOK, http.MethodPatch, "/api/files/"+slug+"/visibility", `{"is_public":true}`, cookie)
}

// trash soft-deletes a file.
func (e *testEnv) trash(t *testing.T, cookie *http.Cookie, slug string) {
	t.Helper()
	e.expect(t, http.StatusNoContent, http.MethodDelete, "/api/files/"+slug, "", cookie)
}

// setConfig writes a global config value the way the settings endpoint would.
func (e *testEnv) setConfig(t *testing.T, key, value string) {
	t.Helper()
	if err := e.queries.SetConfig(context.Background(), sqlcgen.SetConfigParams{Key: key, Value: value}); err != nil {
		t.Fatalf("SetConfig(%s): %v", key, err)
	}
}

// createViaAPI creates a private file through the authenticated endpoint and
// returns the decoded response.
func (e *testEnv) createViaAPI(t *testing.T, cookie *http.Cookie, name, html string) fileResp {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "html_content": html})
	return decodeFile(t, e.do(t, http.MethodPost, "/api/files", string(body), cookie), http.StatusCreated)
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	resp := e.do(t, http.MethodGet, "/api/health", "", nil)
	if b := bodyOf(t, resp, http.StatusOK); !strings.Contains(b, `"status":"ok"`) {
		t.Errorf("health body = %q", b)
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t)

	// Bad JSON.
	e.expect(t, http.StatusBadRequest, http.MethodPost, "/api/auth/login", "{not json", nil)

	// Wrong credentials.
	e.expect(t, http.StatusUnauthorized, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"nope"}`, nil)

	// Correct credentials -> 204 + Set-Cookie.
	resp := e.do(t, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"s3cret"}`, nil)
	assertStatus(t, resp, http.StatusNoContent)
	var found bool
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			found = true
		}
	}
	resp.Body.Close()
	if !found {
		t.Error("successful login did not set a session cookie")
	}
}

func TestMeAndLogout(t *testing.T) {
	e := newEnv(t)

	// Unauthenticated.
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/auth/me", "", nil)

	// Authenticated.
	cookie := e.authCookie(t)
	me := decodeJSON[struct {
		Username string `json:"username"`
	}](t, e.do(t, http.MethodGet, "/api/auth/me", "", cookie), http.StatusOK)
	if me.Username != testUser {
		t.Errorf("me username = %q, want %q", me.Username, testUser)
	}

	// Logout always 204.
	e.expect(t, http.StatusNoContent, http.MethodPost, "/api/auth/logout", "", cookie)

	// The session should no longer be valid.
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/auth/me", "", cookie)
}

func TestFilesCRUD(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	created := e.createViaAPI(t, cookie, "doc", "<h1>v1</h1>")
	if created.IsPublic {
		t.Error("new file should be private")
	}
	if created.HTMLContent != "" {
		t.Error("create response should omit html_content")
	}
	// Short generation scheme: base64(first 6 chars) -> exactly 8 chars.
	if len(created.Slug) != 8 {
		t.Errorf("slug length = %d, want 8", len(created.Slug))
	}
	if len(created.AccessCode) != 22 {
		t.Errorf("access code length = %d, want 22 (128 bits)", len(created.AccessCode))
	}

	// Get returns full content.
	resp := e.do(t, http.MethodGet, "/api/files/"+created.Slug, "", cookie)
	got := decodeFile(t, resp, http.StatusOK)
	if got.HTMLContent != "<h1>v1</h1>" {
		t.Errorf("Get html_content = %q", got.HTMLContent)
	}

	// List includes it.
	list := decodeJSON[[]fileResp](t, e.do(t, http.MethodGet, "/api/files", "", cookie), http.StatusOK)
	if len(list) != 1 || list[0].Slug != created.Slug {
		t.Errorf("list = %d items, want the created file", len(list))
	}

	// Update name, slug, content, access code.
	upd := `{"name":"renamed","slug":"custom-slug","html_content":"<h1>v2</h1>","access_code":"mycode12"}`
	resp = e.do(t, http.MethodPatch, "/api/files/"+created.Slug, upd, cookie)
	updated := decodeFile(t, resp, http.StatusOK)
	if updated.Slug != "custom-slug" || updated.Name != "renamed" {
		t.Errorf("update result = %+v", updated)
	}
	if updated.AccessCode != "mycode12" {
		t.Errorf("access code = %q, want custom code", updated.AccessCode)
	}

	// Old slug is gone.
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/files/"+created.Slug, "", cookie)
}

func TestUpdateValidation(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	a := e.createViaAPI(t, cookie, "a", "<p>a</p>")
	b := e.createViaAPI(t, cookie, "b", "<p>b</p>")

	// Invalid slug.
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"bad slug!","html_content":"<p>x</p>","access_code":"ok123456"}`, cookie)

	// Empty content.
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"ok","html_content":"","access_code":"ok123456"}`, cookie)

	// Empty access code (would open the file to code-less requests).
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"ok","html_content":"<p>x</p>","access_code":""}`, cookie)

	// Access code with invalid characters.
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"ok","html_content":"<p>x</p>","access_code":"has space"}`, cookie)

	// Slug collision -> 409.
	e.expect(t, http.StatusConflict, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"`+b.Slug+`","html_content":"<p>x</p>","access_code":"ok123456"}`, cookie)
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// Missing html_content.
	e.expect(t, http.StatusBadRequest, http.MethodPost, "/api/files", `{"name":"x","html_content":""}`, cookie)

	// The 5MB ceiling is pinned in TestOversizedDocumentReportsItsLimit.

	// Unknown kind -> 400.
	e.expect(t, http.StatusBadRequest, http.MethodPost, "/api/files", `{"name":"x","kind":"pdf","html_content":"x"}`, cookie)
}

func TestCreateKind(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// An explicit kind round-trips in the response.
	resp := e.do(t, http.MethodPost, "/api/files", `{"name":"notes","kind":"markdown","html_content":"# hi"}`, cookie)
	if got := decodeFile(t, resp, http.StatusCreated); got.Kind != "markdown" {
		t.Errorf("kind = %q, want markdown", got.Kind)
	}

	// Omitting kind defaults to html (back-compat).
	resp = e.do(t, http.MethodPost, "/api/files", `{"name":"page","html_content":"<p>x</p>"}`, cookie)
	if got := decodeFile(t, resp, http.StatusCreated); got.Kind != "html" {
		t.Errorf("default kind = %q, want html", got.Kind)
	}
}

func TestTrashAndRestore(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>x</p>")

	// Delete -> 204.
	e.trash(t, cookie, f.Slug)

	// Not in active list.
	resp := e.do(t, http.MethodGet, "/api/files", "", cookie)
	if b := bodyString(t, resp); strings.Contains(b, f.Slug) {
		t.Error("deleted file still in active list")
	}

	// In trashed list.
	resp = e.do(t, http.MethodGet, "/api/files?deleted=true", "", cookie)
	if b := bodyString(t, resp); !strings.Contains(b, f.Slug) {
		t.Error("deleted file not in trashed list")
	}

	// Restore -> 200.
	e.expect(t, http.StatusOK, http.MethodPost, "/api/files/"+f.Slug+"/restore", "", cookie)

	// Restoring again -> 404 (no longer deleted).
	e.expect(t, http.StatusNotFound, http.MethodPost, "/api/files/"+f.Slug+"/restore", "", cookie)
}

func TestHardDelete(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>x</p>")

	// Active file -> 404 (only trashed files can be purged).
	e.expect(t, http.StatusNotFound, http.MethodDelete, "/api/files/"+f.Slug+"/permanent", "", cookie)

	// Soft delete, then purge -> 204.
	e.trash(t, cookie, f.Slug)
	e.expect(t, http.StatusNoContent, http.MethodDelete, "/api/files/"+f.Slug+"/permanent", "", cookie)

	// Gone from the trashed list.
	resp := e.do(t, http.MethodGet, "/api/files?deleted=true", "", cookie)
	if b := bodyString(t, resp); strings.Contains(b, f.Slug) {
		t.Error("purged file still in trashed list")
	}

	// Purging again -> 404; restore is impossible too.
	e.expect(t, http.StatusNotFound, http.MethodDelete, "/api/files/"+f.Slug+"/permanent", "", cookie)
	e.expect(t, http.StatusNotFound, http.MethodPost, "/api/files/"+f.Slug+"/restore", "", cookie)

	// The freed slug can be taken by another file.
	other := e.createViaAPI(t, cookie, "other", "<p>y</p>")
	upd := `{"name":"other","slug":"` + f.Slug + `","html_content":"<p>y</p>","access_code":"ok123456"}`
	e.expect(t, http.StatusOK, http.MethodPatch, "/api/files/"+other.Slug, upd, cookie)
}

func TestSetExpiry(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>x</p>")

	// max_views forces public.
	resp := e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"max_views":3}`, cookie)
	got := decodeFile(t, resp, http.StatusOK)
	if !got.IsPublic {
		t.Error("setting a view limit must force public")
	}
	if got.MaxViews == nil || *got.MaxViews != 3 {
		t.Errorf("max_views = %v, want 3", got.MaxViews)
	}

	// ttl sets an expiry timestamp.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"ttl":"24h"}`, cookie)
	got = decodeFile(t, resp, http.StatusOK)
	if got.ExpiresAt == nil {
		t.Error("ttl should set expires_at")
	}
	if got.MaxViews != nil {
		t.Error("ttl should clear max_views")
	}

	// Mutually exclusive -> 400.
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"ttl":"24h","max_views":5}`, cookie)

	// An arbitrary amount plus a unit is accepted, not just the old presets.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"ttl":"3mo"}`, cookie)
	got = decodeFile(t, resp, http.StatusOK)
	if got.ExpiresAt == nil {
		t.Error("a calendar-unit ttl should set expires_at")
	}

	// Invalid ttl -> 400.
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"ttl":"99x"}`, cookie)

	// Non-positive max_views -> 400.
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"max_views":0}`, cookie)

	// Clearing keeps visibility (currently public) and nulls the limits.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{}`, cookie)
	got = decodeFile(t, resp, http.StatusOK)
	if got.ExpiresAt != nil || got.MaxViews != nil {
		t.Error("clearing should null both limits")
	}
	if !got.IsPublic {
		t.Error("clearing must not change visibility")
	}
}

func TestRename(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "old name", "<p>x</p>")

	// Success.
	resp := e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/name", `{"name":"new name"}`, cookie)
	if got := decodeFile(t, resp, http.StatusOK); got.Name != "new name" {
		t.Errorf("name = %q, want %q", got.Name, "new name")
	}

	// Empty/whitespace name -> 400.
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+f.Slug+"/name", `{"name":"  "}`, cookie)

	// Unknown slug -> 404.
	e.expect(t, http.StatusNotFound, http.MethodPatch, "/api/files/nope/name", `{"name":"x"}`, cookie)
}

func TestVisibilityTagsRefresh(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>x</p>")

	resp := e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/visibility", `{"is_public":true}`, cookie)
	if got := decodeFile(t, resp, http.StatusOK); !got.IsPublic {
		t.Error("visibility not set to public")
	}

	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/tags", `{"tags":" a , b ,a "}`, cookie)
	if got := decodeFile(t, resp, http.StatusOK); got.Tags != "a,b" {
		t.Errorf("tags = %q, want normalized %q", got.Tags, "a,b")
	}

	resp = e.do(t, http.MethodPost, "/api/files/"+f.Slug+"/refresh-code", "", cookie)
	if got := decodeFile(t, resp, http.StatusOK); got.AccessCode == f.AccessCode || len(got.AccessCode) != 22 {
		t.Errorf("refresh-code did not produce a new 128-bit code: %q", got.AccessCode)
	}
}

// storeBlob writes content to the blob store and records it, as the handlers
// do before they write a files row.
func (e *testEnv) storeBlob(t *testing.T, content []byte) string {
	t.Helper()
	b, err := e.blobs.Put(content)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := e.queries.InsertBlob(context.Background(), sqlcgen.InsertBlobParams{
		ID: b.ID, Size: b.Size, Sha256: b.SHA256,
	}); err != nil {
		t.Fatalf("InsertBlob: %v", err)
	}
	return b.ID
}

// content is what a file row's blob holds: a document's source, a site's zip.
func (e *testEnv) content(t *testing.T, f sqlcgen.File) []byte {
	t.Helper()
	data, err := e.blobs.Read(f.BlobID)
	if err != nil {
		t.Fatalf("read content of %s: %v", f.Slug, err)
	}
	return data
}

// insertFile writes a file row directly, with the slug, access code and
// visibility the API's Create (random slug, private) can't express.
func (e *testEnv) insertFile(t *testing.T, slug, kind, source, code string, public bool) {
	t.Helper()
	if _, err := e.queries.CreateFile(context.Background(), sqlcgen.CreateFileParams{
		Slug:       slug,
		Name:       slug,
		BlobID:     e.storeBlob(t, []byte(source)),
		Kind:       kind,
		IsPublic:   public,
		AccessCode: code,
		UserID:     e.admin.ID,
	}); err != nil {
		t.Fatalf("CreateFile(%s %s): %v", slug, kind, err)
	}
}

// createRaw inserts an html file with a controlled slug/access_code/visibility.
func (e *testEnv) createRaw(t *testing.T, slug, code, html string, public bool) {
	t.Helper()
	e.insertFile(t, slug, "html", html, code, public)
}

// createRawKind inserts a public file of an explicit kind, for exercising the
// non-html render paths.
func (e *testEnv) createRawKind(t *testing.T, slug, kind, source string) {
	t.Helper()
	e.insertFile(t, slug, kind, source, "c", true)
}

func TestRenderByKind(t *testing.T) {
	e := newEnv(t)

	// Markdown is rendered to HTML.
	e.createRawKind(t, "md", "markdown", "# Title\n\nsome **bold** text\n")
	resp := e.do(t, http.MethodGet, "/res/md?code=c", "", nil)
	if b := bodyOf(t, resp, http.StatusOK); !strings.Contains(b, "Title</h1>") {
		t.Errorf("markdown not rendered to HTML: %q", b)
	}

	// Text is HTML-escaped and its newlines preserved (never interpreted). The
	// renderers themselves are covered in handlers/content_test.go; this pins
	// that /res dispatches on kind at all.
	e.createRawKind(t, "note", "txt", "a <b> tag\nsecond line")
	resp = e.do(t, http.MethodGet, "/res/note?code=c", "", nil)
	if b := bodyOf(t, resp, http.StatusOK); !strings.Contains(b, "a &lt;b&gt; tag\nsecond line") {
		t.Errorf("txt content not preserved/escaped: %q", b)
	}
}

func TestRenderAccessControl(t *testing.T) {
	e := newEnv(t)

	// Missing slug -> 404.
	e.expect(t, http.StatusNotFound, http.MethodGet, "/res/nope", "", nil)

	// Private file, no session -> 403.
	e.createRaw(t, "priv", "code1", "<h1>secret</h1>", false)
	e.expect(t, http.StatusForbidden, http.MethodGet, "/res/priv", "", nil)

	// Public file, wrong code -> 403.
	e.createRaw(t, "pub", "rightcode", "<h1>hello</h1>", true)
	e.expect(t, http.StatusForbidden, http.MethodGet, "/res/pub?code=wrong", "", nil)

	// Public file, correct code -> 200 + content, and code_success_count
	// bumps (anonymous access), not success_count (session access).
	// (Followed by the QR widget, see TestRenderQRWidget.)
	resp := e.do(t, http.MethodGet, "/res/pub?code=rightcode", "", nil)
	if b := bodyOf(t, resp, http.StatusOK); !strings.HasPrefix(b, "<h1>hello</h1>") {
		t.Errorf("render body = %q", b)
	}
	f, err := e.queries.GetFileBySlugAnyOwner(context.Background(), "pub")
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if f.CodeSuccessCount != 1 {
		t.Errorf("code_success_count = %d, want 1", f.CodeSuccessCount)
	}
	if f.SuccessCount != 0 {
		t.Errorf("success_count = %d, want 0 for code-based access", f.SuccessCount)
	}
	if f.FailureCount != 1 { // from the wrong-code attempt above
		t.Errorf("failure_count = %d, want 1", f.FailureCount)
	}

	// Admin session bypasses the code on a private file and bumps
	// success_count, not code_success_count.
	cookie := e.authCookie(t)
	resp = e.do(t, http.MethodGet, "/res/priv", "", cookie)
	if b := bodyOf(t, resp, http.StatusOK); !strings.HasPrefix(b, "<h1>secret</h1>") {
		t.Errorf("admin render body = %q", b)
	}
	f, err = e.queries.GetFileBySlugAnyOwner(context.Background(), "priv")
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if f.SuccessCount != 1 {
		t.Errorf("success_count = %d, want 1 for session access", f.SuccessCount)
	}
	if f.CodeSuccessCount != 0 {
		t.Errorf("code_success_count = %d, want 0 for session access", f.CodeSuccessCount)
	}
}

func TestRenderDeletedIsNotFound(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	e.createRaw(t, "gone", "code", "<p>x</p>", true)
	if _, err := e.queries.SoftDeleteFile(context.Background(), sqlcgen.SoftDeleteFileParams{Slug: "gone", UserID: e.admin.ID}); err != nil {
		t.Fatalf("SoftDeleteFile: %v", err)
	}
	// Even an admin can't resurrect a deleted file via /res.
	e.expect(t, http.StatusNotFound, http.MethodGet, "/res/gone", "", cookie)
}

func TestRenderViewLimitExpiry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.createRaw(t, "limited", "c", "<h1>x</h1>", false)
	if _, err := e.queries.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{
		MaxViews: sqlNullInt64(1),
		Slug:     "limited",
		UserID:   e.admin.ID,
	}); err != nil {
		t.Fatalf("SetFileExpiry: %v", err)
	}

	// First anonymous access consumes the single allowed view.
	e.expect(t, http.StatusOK, http.MethodGet, "/res/limited?code=c", "", nil)

	// Second anonymous access is over quota -> 403, and the file flips private.
	e.expect(t, http.StatusForbidden, http.MethodGet, "/res/limited?code=c", "", nil)

	f, err := e.queries.GetFileBySlugAnyOwner(ctx, "limited")
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if f.IsPublic {
		t.Error("file should be private after exhausting its view quota")
	}
	if f.MaxViews.Valid {
		t.Error("limits should be cleared after expiry")
	}
	// Clearing the limits erases the evidence, so the reason is recorded before
	// they go: without it the dashboard can only say "private" and the owner has
	// no way to tell a link that ran out from one they made private themselves.
	if f.ExpiredReason != "views" || !f.ExpiredAt.Valid {
		t.Errorf("expiry marker = %q at %v, want views with a timestamp", f.ExpiredReason, f.ExpiredAt)
	}
	// ...and the owner sees it on the file.
	resp := e.do(t, http.MethodGet, "/api/files/limited", "", e.authCookie(t))
	if got := decodeFile(t, resp, http.StatusOK); got.ExpiredReason != "views" || got.ExpiredAt == nil {
		t.Errorf("api reports marker %q at %v, want views with a timestamp", got.ExpiredReason, got.ExpiredAt)
	}
}

// TestRenderTTLExpiry is the time-based half of lazy expiry: the deadline is
// checked on access, with no cron to do it.
func TestRenderTTLExpiry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.createRaw(t, "stale", "c", "<h1>x</h1>", true)
	if _, err := e.queries.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{
		ExpiresAt: sql.NullTime{Time: time.Now().Add(-time.Minute), Valid: true},
		Slug:      "stale",
		UserID:    e.admin.ID,
	}); err != nil {
		t.Fatalf("SetFileExpiry: %v", err)
	}

	e.expect(t, http.StatusForbidden, http.MethodGet, "/res/stale?code=c", "", nil)

	f, err := e.queries.GetFileBySlugAnyOwner(ctx, "stale")
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if f.IsPublic || f.ExpiresAt.Valid {
		t.Errorf("expired file = public %v, expires_at %v; want private with no limit", f.IsPublic, f.ExpiresAt)
	}
	if f.ExpiredReason != "ttl" {
		t.Errorf("expiry marker = %q, want ttl", f.ExpiredReason)
	}

	// Re-publishing with a fresh limit clears the marker, so the badge can
	// never sit next to a live expiry it contradicts.
	resp := e.do(t, http.MethodPatch, "/api/files/stale/expiry", `{"ttl":"24h"}`, e.authCookie(t))
	if got := decodeFile(t, resp, http.StatusOK); got.ExpiredReason != "" || got.ExpiredAt != nil {
		t.Errorf("marker = %q at %v, want cleared by a new limit", got.ExpiredReason, got.ExpiredAt)
	}
}

func TestRenderAdminDoesNotConsumeQuota(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cookie := e.authCookie(t)
	e.createRaw(t, "quota", "c", "<h1>x</h1>", false)
	if _, err := e.queries.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{
		MaxViews: sqlNullInt64(2),
		Slug:     "quota",
		UserID:   e.admin.ID,
	}); err != nil {
		t.Fatalf("SetFileExpiry: %v", err)
	}

	for i := 0; i < 3; i++ {
		e.expect(t, http.StatusOK, http.MethodGet, "/res/quota", "", cookie)
	}
	f, err := e.queries.GetFileBySlugAnyOwner(ctx, "quota")
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if f.ViewCount != 0 {
		t.Errorf("admin views consumed quota: view_count = %d, want 0", f.ViewCount)
	}
	if !f.IsPublic {
		t.Error("file should remain public — admin views don't expire it")
	}
}

func TestDownload(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "report", "<h1>dl</h1>")

	// Unauthenticated -> 401 (requireAuth middleware).
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/files/"+f.Slug+"/download", "", nil)

	// Authenticated -> 200 with attachment disposition + raw content.
	resp := e.do(t, http.MethodGet, "/api/files/"+f.Slug+"/download", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if b := bodyString(t, resp); b != "<h1>dl</h1>" {
		t.Errorf("download body = %q", b)
	}

	// Missing slug -> 404.
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/files/nope/download", "", cookie)
}

func TestBackupDownload(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	// Put some data in the DB so the snapshot isn't trivially empty.
	e.createViaAPI(t, cookie, "doc", "<h1>backed up</h1>")

	// Unauthenticated -> 401 (requireAuth middleware).
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/backup", "", nil)

	// Authenticated -> 200, a .tar attachment: the database, then the content.
	resp := e.do(t, http.MethodGet, "/api/backup", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, ".tar") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-tar" {
		t.Errorf("Content-Type = %q", ct)
	}
	// (A signed-in non-admin gets 403: see TestSuperAdminEndpointsRejectOrdinaryUsers.)
	tr := tar.NewReader(strings.NewReader(bodyString(t, resp)))
	var names []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("backup is not a tar: %v", err)
		}
		names = append(names, hdr.Name)
		if hdr.Name == "renderbin.db" {
			head := make([]byte, 16)
			if _, err := io.ReadFull(tr, head); err != nil || string(head) != "SQLite format 3\x00" {
				t.Errorf("renderbin.db does not look like a SQLite database (first bytes: %q)", head)
			}
		}
	}
	if len(names) != 2 || names[0] != "renderbin.db" || !strings.HasPrefix(names[1], "blobs/") {
		t.Errorf("backup entries = %v, want renderbin.db and the document's blob", names)
	}
}

// TestBackupRestoreRoundTrip is the whole point of the pair: download a
// snapshot, change everything, upload the snapshot back, and find the original
// state — through the same live server, with no restart in between.
func TestBackupRestoreRoundTrip(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	original := e.createViaAPI(t, cookie, "before backup", "<h1>original</h1>")
	snapshot := e.downloadBackup(t, cookie)

	// Diverge: add a file, delete the original one.
	added := e.createViaAPI(t, cookie, "after backup", "<p>new</p>")
	e.trash(t, cookie, original.Slug)

	restored := e.restore(t, snapshot, cookie)
	out := decodeJSON[struct {
		Users int64 `json:"users"`
		Files int64 `json:"files"`
	}](t, restored, http.StatusOK)
	if out.Users != 1 || out.Files != 1 {
		t.Errorf("restore reported %+v, want 1 user / 1 file", out)
	}

	// The same running server now serves the snapshot's data: the original file
	// is back and the one added afterwards is gone. A fresh session is needed
	// because the snapshot's sessions table replaced the live one.
	after := e.cookieFor(t, e.admin.ID)
	resp := e.do(t, http.MethodGet, "/api/files/"+original.Slug, "", after)
	if got := decodeFile(t, resp, http.StatusOK); got.HTMLContent != "<h1>original</h1>" {
		t.Errorf("restored content = %q", got.HTMLContent)
	}
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/files/"+added.Slug, "", after)

	// And the restored database keeps working for writes — the id sequence came
	// across with the rows, so a new file doesn't collide with a restored id.
	e.createViaAPI(t, after, "after restore", "<p>fresh</p>")
}

// downloadBackup returns the database snapshot as bytes.
func (e *testEnv) downloadBackup(t *testing.T, cookie *http.Cookie) []byte {
	t.Helper()
	return []byte(bodyOf(t, e.do(t, http.MethodGet, "/api/backup", "", cookie), http.StatusOK))
}

// restore POSTs a raw database file to the restore endpoint. The body is the
// file itself, with no multipart wrapper.
func (e *testEnv) restore(t *testing.T, snapshot []byte, cookie *http.Cookie) *http.Response {
	t.Helper()
	return e.send(t, http.MethodPost, "/api/backup/restore", bytes.NewReader(snapshot), cookie,
		map[string]string{"Content-Type": "application/octet-stream"}, true)
}

func TestRestoreRejections(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	kept := e.createViaAPI(t, cookie, "keep me", "<p>safe</p>")
	snapshot := e.downloadBackup(t, cookie)

	resp := e.restore(t, snapshot, nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()

	// Not a database at all, and an empty body.
	for _, c := range []struct {
		name string
		body []byte
		want int
	}{
		{"not a database", []byte("just some notes, not a database at all"), http.StatusBadRequest},
		{"empty upload", nil, http.StatusBadRequest},
		// A download cut short: the database arrived, the content did not.
		{"truncated backup", snapshot[:len(snapshot)/2], http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := e.restore(t, c.body, cookie)
			assertStatus(t, resp, c.want)
			resp.Body.Close()
		})
	}

	// Every rejection above must have left the live data untouched — that is
	// what makes it safe to report the problem instead of a lost database.
	e.expect(t, http.StatusOK, http.MethodGet, "/api/files/"+kept.Slug, "", cookie)
}

func sqlNullInt64(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: true}
}

// sessionCookie returns the session Set-Cookie of a login/register/setup
// response exactly as sent, attributes included (req.AddCookie only serialises
// name and value, so it can be attached to a request as is).
func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatal("response did not set a session cookie")
	return nil
}

func TestSetupFlow(t *testing.T) {
	e := newBareEnv(t)

	// Empty database → needs setup.
	resp := e.do(t, http.MethodGet, "/api/setup/status", "", nil)
	if b := bodyOf(t, resp, http.StatusOK); !strings.Contains(b, `"needs_setup":true`) {
		t.Errorf("status body = %q, want needs_setup true", b)
	}

	// Registration is blocked before setup.
	e.expect(t, http.StatusConflict, http.MethodPost, "/api/auth/register",
		`{"username":"u","nickname":"n","password":"secret1"}`, nil)

	// Setup creates the super admin, writes configs, and logs in.
	resp = e.do(t, http.MethodPost, "/api/setup",
		`{"username":"boss","nickname":"Boss","password":"secret1","allow_registration":true,"mcp_enabled":false}`, nil)
	assertStatus(t, resp, http.StatusCreated)
	cookie := sessionCookie(t, resp)
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/api/auth/me", "", cookie)
	if b := bodyOf(t, resp, http.StatusOK); !strings.Contains(b, `"is_admin":true`) {
		t.Errorf("me body = %q, want is_admin true", b)
	}

	// Second setup attempt is rejected.
	e.expect(t, http.StatusConflict, http.MethodPost, "/api/setup",
		`{"username":"evil","nickname":"E","password":"secret1"}`, nil)

	resp = e.do(t, http.MethodGet, "/api/setup/status", "", nil)
	if b := bodyString(t, resp); !strings.Contains(b, `"needs_setup":false`) || !strings.Contains(b, `"allow_registration":true`) {
		t.Errorf("status body after setup = %q", b)
	}
}

func TestRegisterRespectsConfig(t *testing.T) {
	e := newEnv(t) // super admin exists, no configs → registration disabled
	e.expect(t, http.StatusForbidden, http.MethodPost, "/api/auth/register",
		`{"username":"u2","nickname":"N","password":"secret1"}`, nil)

	// Admin enables registration.
	cookie := e.authCookie(t)
	e.expect(t, http.StatusOK, http.MethodPut, "/api/settings", `{"allow_registration":true}`, cookie)

	// Now registration works and logs the new user in as a non-admin.
	resp := e.do(t, http.MethodPost, "/api/auth/register",
		`{"username":"u2","nickname":"N","password":"secret1"}`, nil)
	assertStatus(t, resp, http.StatusCreated)
	userCookie := sessionCookie(t, resp)
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/api/auth/me", "", userCookie)
	if b := bodyOf(t, resp, http.StatusOK); !strings.Contains(b, `"is_admin":false`) {
		t.Errorf("me body = %q, want is_admin false", b)
	}

	// Duplicate username → 409; weak password → 400.
	e.expect(t, http.StatusConflict, http.MethodPost, "/api/auth/register",
		`{"username":"u2","nickname":"N","password":"secret1"}`, nil)
	e.expect(t, http.StatusBadRequest, http.MethodPost, "/api/auth/register",
		`{"username":"u3","nickname":"N","password":"short"}`, nil)
}

func TestUpdateProfile(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// Nickname change.
	e.expect(t, http.StatusNoContent, http.MethodPatch, "/api/user", `{"nickname":"New Name"}`, cookie)
	resp := e.do(t, http.MethodGet, "/api/auth/me", "", cookie)
	if b := bodyString(t, resp); !strings.Contains(b, `"nickname":"New Name"`) {
		t.Errorf("me body = %q, want updated nickname", b)
	}

	// Password change requires the current password.
	e.expect(t, http.StatusForbidden, http.MethodPatch, "/api/user",
		`{"current_password":"wrong","new_password":"secret2"}`, cookie)
	e.expect(t, http.StatusNoContent, http.MethodPatch, "/api/user",
		`{"current_password":"`+testPass+`","new_password":"secret2"}`, cookie)

	// Old password no longer logs in; the new one does.
	e.expect(t, http.StatusUnauthorized, http.MethodPost, "/api/auth/login",
		`{"username":"`+testUser+`","password":"`+testPass+`"}`, nil)
	e.expect(t, http.StatusNoContent, http.MethodPost, "/api/auth/login",
		`{"username":"`+testUser+`","password":"secret2"}`, nil)
}

func TestAPIKeyLifecycle(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// MCP disabled → no key issued.
	e.expect(t, http.StatusConflict, http.MethodPost, "/api/user/api-key", "", cookie)

	e.expect(t, http.StatusOK, http.MethodPut, "/api/settings", `{"mcp_enabled":true}`, cookie)

	keyOf := func(resp *http.Response) string {
		t.Helper()
		return decodeJSON[struct {
			APIKey string `json:"api_key"`
		}](t, resp, http.StatusOK).APIKey
	}

	// First ensure creates a key; a second ensure returns the same key.
	resp := e.do(t, http.MethodPost, "/api/user/api-key", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	first := keyOf(resp)
	if !strings.HasPrefix(first, "rb_") {
		t.Errorf("api key = %q, want rb_ prefix", first)
	}
	resp = e.do(t, http.MethodPost, "/api/user/api-key", "", cookie)
	if again := keyOf(resp); again != first {
		t.Errorf("ensure returned a different key: %q vs %q", again, first)
	}

	// Reset issues a fresh key.
	resp = e.do(t, http.MethodPost, "/api/user/api-key/reset", "", cookie)
	if fresh := keyOf(resp); fresh == first {
		t.Error("reset should replace the key")
	}
}

type searchResp struct {
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	MatchedName    bool   `json:"matched_name"`
	MatchedMeta    bool   `json:"matched_meta"`
	MatchedContent bool   `json:"matched_content"`
	Snippet        string `json:"snippet"`
}

func (e *testEnv) search(t *testing.T, cookie *http.Cookie, query string) []searchResp {
	t.Helper()
	return decodeJSON[[]searchResp](t, e.do(t, http.MethodGet, "/api/search?"+query, "", cookie), http.StatusOK)
}

func TestSearchFiles(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	e.createViaAPI(t, cookie, "hello world", "<p>nothing interesting</p>")
	e.createViaAPI(t, cookie, "report", "<p>padding "+strings.Repeat("x", 300)+" hello inside the content "+strings.Repeat("y", 300)+"</p>")
	e.createViaAPI(t, cookie, "unrelated", "<p>zzz</p>")

	// Name-only search matches just the title hit, case-insensitively.
	results := e.search(t, cookie, "q=HELLO")
	if len(results) != 1 || results[0].Name != "hello world" {
		t.Fatalf("name search = %+v, want only 'hello world'", results)
	}
	if !results[0].MatchedName || results[0].Snippet != "" {
		t.Errorf("name hit = %+v, want matched_name and no snippet", results[0])
	}

	// Content search adds the content-only hit with a windowed snippet.
	results = e.search(t, cookie, "q=hello&content=true")
	if len(results) != 2 {
		t.Fatalf("content search returned %d results, want 2", len(results))
	}
	var contentHit *searchResp
	for i := range results {
		if results[i].Name == "report" {
			contentHit = &results[i]
		}
	}
	if contentHit == nil {
		t.Fatal("content search missing the content-only hit")
	}
	if contentHit.MatchedName || !contentHit.MatchedContent {
		t.Errorf("content hit flags = %+v", contentHit)
	}
	if !strings.Contains(contentHit.Snippet, "hello inside the content") {
		t.Errorf("snippet %q missing the match", contentHit.Snippet)
	}
	if !strings.HasPrefix(contentHit.Snippet, "…") || !strings.HasSuffix(contentHit.Snippet, "…") {
		t.Errorf("snippet %q should be truncated on both sides", contentHit.Snippet)
	}
	if n := len([]rune(contentHit.Snippet)); n > 2*100+len("hello inside the content")+2 {
		t.Errorf("snippet is %d runes, want ≤ match+200+ellipses", n)
	}

	// Empty query → empty result set, not an error.
	if results := e.search(t, cookie, "q="); len(results) != 0 {
		t.Errorf("empty query returned %d results, want 0", len(results))
	}

	// Search requires auth.
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/search?q=hello", "", nil)
}

// The default search covers names and the head fields extracted when the
// content was stored -- a page's title, description, keywords -- but not the
// body, which takes content=true. A field hit shows the field as its snippet.
func TestSearchMatchesHeadFields(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	e.createViaAPI(t, cookie, "q3.html", `<html><head><title>Quarterly Revenue</title>
<meta name="description" content="Numbers for the board, by region"></head>
<body><p>The body mentions zebras.</p></body></html>`)
	create := func(name, kind, content string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": name, "kind": kind, "html_content": content})
		resp := e.do(t, http.MethodPost, "/api/files", string(body), cookie)
		assertStatus(t, resp, http.StatusCreated)
		resp.Body.Close()
	}
	create("notes.md", "markdown", "---\ntitle: 周报\n---\n正文里有 zebras")
	s := e.uploadSite(t, cookie, "docs", map[string]string{
		"index.html": `<head><title>Site Handbook</title></head><body>zebras again</body>`,
	})

	for _, c := range []struct{ q, want, snippet string }{
		{"quarterly", "q3.html", "Quarterly Revenue"},
		{"BY REGION", "q3.html", "Numbers for the board, by region"},
		{"周报", "notes.md", "周报"},
		{"handbook", s.Name, "Site Handbook"},
	} {
		results := e.search(t, cookie, "q="+url.QueryEscape(c.q))
		if len(results) != 1 || results[0].Name != c.want {
			t.Errorf("search %q = %+v, want only %q", c.q, results, c.want)
			continue
		}
		r := results[0]
		if r.MatchedName || !r.MatchedMeta || r.MatchedContent || r.Snippet != c.snippet {
			t.Errorf("search %q: %+v, want a head-field hit with snippet %q", c.q, r, c.snippet)
		}
	}

	// Body text is not in the default search...
	if results := e.search(t, cookie, "q=zebras"); len(results) != 0 {
		t.Errorf("default search found body text: %+v", results)
	}
	// ...but is in the full-text one (sites excepted: their source is a zip).
	if results := e.search(t, cookie, "q=zebras&content=true"); len(results) != 2 {
		t.Errorf("full-text search for body text = %+v, want the two documents", results)
	}
	// A full-text search still reports a head-field hit as one.
	if results := e.search(t, cookie, "q=handbook&content=true"); len(results) != 1 || !results[0].MatchedMeta {
		t.Errorf("full-text search for a site's title = %+v", results)
	}
}

func TestSearchScopedToOwnFiles(t *testing.T) {
	e := newEnv(t)
	adminCookie := e.authCookie(t)
	e.createViaAPI(t, adminCookie, "hello admin file", "<p>x</p>")

	// A second user with their own session sees only their own files.
	_, otherCookie := e.newUser(t, "other")

	e.createViaAPI(t, otherCookie, "hello other file", "<p>y</p>")

	adminResults := e.search(t, adminCookie, "q=hello&content=true")
	if len(adminResults) != 1 || adminResults[0].Name != "hello admin file" {
		t.Errorf("admin search = %+v, want only their own file", adminResults)
	}
	otherResults := e.search(t, otherCookie, "q=hello&content=true")
	if len(otherResults) != 1 || otherResults[0].Name != "hello other file" {
		t.Errorf("other search = %+v, want only their own file", otherResults)
	}
}

func TestEmptyTrash(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	_, otherCookie := e.newUser(t, "other")

	live := e.createViaAPI(t, cookie, "live", "<p>keep</p>")
	trashed := e.createViaAPI(t, cookie, "trashed", "<p>gone</p>")
	theirs := e.createViaAPI(t, otherCookie, "theirs", "<p>mine</p>")
	for _, c := range []struct {
		slug   string
		cookie *http.Cookie
	}{{trashed.Slug, cookie}, {theirs.Slug, otherCookie}} {
		e.expect(t, http.StatusNoContent, http.MethodDelete, "/api/files/"+c.slug, "", c.cookie)
	}

	out := decodeJSON[struct {
		Deleted int64 `json:"deleted"`
	}](t, e.do(t, http.MethodDelete, "/api/trash", "", cookie), http.StatusOK)
	if out.Deleted != 1 {
		t.Errorf("deleted = %d, want 1", out.Deleted)
	}

	// The live file is untouched -- this endpoint is the only bulk delete in
	// the API, so "it took only the trash" is the property that matters.
	e.expect(t, http.StatusOK, http.MethodGet, "/api/files/"+live.Slug, "", cookie)
	// And it is owner-scoped: the other user still has their own trash.
	e.expect(t, http.StatusOK, http.MethodPost, "/api/files/"+theirs.Slug+"/restore", "", otherCookie)

	// Emptying an already-empty trash succeeds with 0 rather than 404.
	e.expect(t, http.StatusOK, http.MethodDelete, "/api/trash", "", cookie)

	e.expect(t, http.StatusUnauthorized, http.MethodDelete, "/api/trash", "", nil)
}

// TestSessionCookieSecureFollowsScheme pins the fix for a login that silently
// never took: the cookie was unconditionally Secure, and a browser discards a
// Secure cookie arriving over plain HTTP from anything but localhost — so
// self-hosting on a LAN address looked like a rejected password with no error.
func TestSessionCookieSecureFollowsScheme(t *testing.T) {
	e := newEnv(t)
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, testUser, testPass)

	// The test server speaks plain HTTP, which is the LAN case.
	resp := e.do(t, http.MethodPost, "/api/auth/login", body, nil)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
	if c := sessionCookie(t, resp); c.Secure {
		t.Error("cookie must not be Secure over plain HTTP, or the browser drops it and login appears to fail")
	}

	// A terminating proxy is the third deployment: the backend sees HTTP, the
	// browser saw HTTPS, and only the forwarded header says so.
	resp = e.send(t, http.MethodPost, "/api/auth/login", strings.NewReader(body), nil,
		map[string]string{"Content-Type": "application/json", "X-Forwarded-Proto": "https"}, true)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
	if c := sessionCookie(t, resp); !c.Secure {
		t.Error("cookie must be Secure when the browser reached us over HTTPS")
	}

	// Logout mirrors it, so the deletion targets the cookie that was set.
	resp = e.do(t, http.MethodPost, "/api/auth/logout", "", nil)
	assertStatus(t, resp, http.StatusNoContent)
	if c := sessionCookie(t, resp); c.Secure {
		t.Error("logout over plain HTTP should clear a non-Secure cookie")
	}
	resp.Body.Close()
}

// --- account management ---

type adminUserResp struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	IsSuperAdmin bool   `json:"is_super_admin"`
	Disabled     bool   `json:"disabled"`
	FileCount    int64  `json:"file_count"`
	TrashedCount int64  `json:"trashed_count"`
}

func (e *testEnv) adminUsers(t *testing.T, cookie *http.Cookie) []adminUserResp {
	t.Helper()
	return decodeJSON[[]adminUserResp](t, e.do(t, http.MethodGet, "/api/admin/users", "", cookie), http.StatusOK)
}

func TestAdminListsUsersWithFileCounts(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	_, userCookie := e.newUser(t, "bob")

	e.createViaAPI(t, cookie, "admin file", "<p>1</p>")
	trashed := e.createViaAPI(t, userCookie, "bob file", "<p>2</p>")
	e.createViaAPI(t, userCookie, "bob live", "<p>3</p>")
	e.trash(t, userCookie, trashed.Slug)

	users := e.adminUsers(t, cookie)
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}
	if !users[0].IsSuperAdmin || users[1].IsSuperAdmin {
		t.Errorf("super admin flag = %v/%v, want only the first", users[0].IsSuperAdmin, users[1].IsSuperAdmin)
	}
	if users[0].FileCount != 1 || users[1].FileCount != 1 || users[1].TrashedCount != 1 {
		t.Errorf("counts = %+v", users)
	}
}

// currentHash reads a user's stored password hash, for asserting that a
// rejected request did not change it.
func (e *testEnv) currentHash(t *testing.T, id int64) string {
	t.Helper()
	u, err := e.queries.GetUserByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	return u.PasswordHash
}

// TestAdminSuspendAccount covers every door a suspension has to close: the
// login form, sessions already issued, the MCP key, and the public links to
// files the account owns.
func TestAdminSuspendAccount(t *testing.T) {
	e := newEnv(t)
	adminCookie := e.authCookie(t)
	user, userCookie := e.newUser(t, "bob")

	// Give bob a published file and an MCP key.
	file := e.createViaAPI(t, userCookie, "bob public", "<p>bob</p>")
	e.publish(t, userCookie, file.Slug)
	bobKey := e.apiKeyFor(t, user.ID)

	publicURL := "/res/" + file.Slug + "?code=" + file.AccessCode
	e.expect(t, http.StatusOK, http.MethodGet, publicURL, "", nil)

	// Suspend.
	e.expect(t, http.StatusNoContent, http.MethodPatch, fmt.Sprintf("/api/admin/users/%d/status", user.ID), `{"disabled":true}`, adminCookie)

	if users := e.adminUsers(t, adminCookie); !users[1].Disabled {
		t.Error("list should report the account as disabled")
	}
	// The password still verifies, so this 403 is about the account's state.
	e.expect(t, http.StatusForbidden, http.MethodPost, "/api/auth/login", `{"username":"bob","password":"secret2"}`, nil)
	// An existing session stops working on its very next request.
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/files", "", userCookie)
	// The MCP key dies with it, or suspension would leave the agent door open.
	resp := e.mcpCall(t, bobKey, "list_files", map[string]any{})
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
	// And the account's public links read as nonexistent, not merely forbidden.
	e.expect(t, http.StatusNotFound, http.MethodGet, publicURL, "", nil)
	// The super admin's own files are unaffected by someone else's suspension.
	adminFile := e.createViaAPI(t, adminCookie, "admin file", "<p>admin</p>")
	e.expect(t, http.StatusOK, http.MethodGet, "/res/"+adminFile.Slug, "", adminCookie)

	// Restore: everything comes back, including the link.
	e.expect(t, http.StatusNoContent, http.MethodPatch, fmt.Sprintf("/api/admin/users/%d/status", user.ID), `{"disabled":false}`, adminCookie)
	resp = e.do(t, http.MethodPost, "/api/auth/login", `{"username":"bob","password":"secret2"}`, nil)
	assertStatus(t, resp, http.StatusNoContent)
	restored := sessionCookie(t, resp)
	resp.Body.Close()
	e.expect(t, http.StatusOK, http.MethodGet, "/api/files", "", restored)
	e.expect(t, http.StatusOK, http.MethodGet, publicURL, "", nil)
}

// The super admin is the only account that can lift a suspension, so letting it
// suspend itself would be a one-way door out of the app.
func TestAdminCannotDisableSuperAdmin(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	e.expect(t, http.StatusForbidden, http.MethodPatch, "/api/admin/users/1/status", `{"disabled":true}`, cookie)

	e.expect(t, http.StatusOK, http.MethodGet, "/api/auth/me", "", cookie)

	// An unknown id is a missing object (404); a non-numeric one could never
	// name a row and is a malformed request (400).
	e.expect(t, http.StatusNotFound, http.MethodPatch, "/api/admin/users/9999/status", `{"disabled":true}`, cookie)
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/admin/users/abc/status", `{"disabled":true}`, cookie)
}

func TestAdminResetPassword(t *testing.T) {
	e := newEnv(t)
	adminCookie := e.authCookie(t)
	user, userCookie := e.newUser(t, "bob")
	path := fmt.Sprintf("/api/admin/users/%d/password", user.ID)

	// Too short is rejected before anything is written.
	e.expect(t, http.StatusBadRequest, http.MethodPost, path, `{"new_password":"abc"}`, adminCookie)

	e.expect(t, http.StatusNoContent, http.MethodPost, path, `{"new_password":"brand-new-pass"}`, adminCookie)

	// The new password works and the old one doesn't -- no current password
	// was needed, which is the point: there is no self-service recovery.
	e.expect(t, http.StatusNoContent, http.MethodPost, "/api/auth/login", `{"username":"bob","password":"brand-new-pass"}`, nil)
	e.expect(t, http.StatusUnauthorized, http.MethodPost, "/api/auth/login", `{"username":"bob","password":"secret2"}`, nil)

	// Unlike a self-service change, an admin reset ends the target's existing
	// sessions: it is what you reach for when an account is compromised.
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/files", "", userCookie)

	e.expect(t, http.StatusNotFound, http.MethodPost, "/api/admin/users/9999/password", `{"new_password":"whatever1"}`, adminCookie)
}

// --- MCP endpoint ---

// enableMCP turns the mcp_enabled config on and issues the admin an API key.
func (e *testEnv) enableMCP(t *testing.T) string {
	t.Helper()
	return e.apiKeyFor(t, e.admin.ID)
}

// mcpCall POSTs a raw JSON-RPC tools/call to /mcp. Stateless mode means each
// call is self-contained — no initialize handshake needed.
func (e *testEnv) mcpCall(t *testing.T, apiKey, tool string, args map[string]any) *http.Response {
	t.Helper()
	argJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, argJSON)
	header := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
	if apiKey != "" {
		header["Authorization"] = "Bearer " + apiKey
	}
	return e.send(t, http.MethodPost, "/mcp", strings.NewReader(body), nil, header, true)
}

type mcpToolResult struct {
	IsError bool `json:"isError"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
}

func (r mcpToolResult) text() string {
	var parts []string
	for _, c := range r.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n")
}

func decodeMCPResult(t *testing.T, resp *http.Response) mcpToolResult {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("mcp status = %d, body %q", resp.StatusCode, b)
	}
	var envelope struct {
		Result mcpToolResult `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode mcp response: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("mcp protocol error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result
}

// mcp runs a tool and returns its result, tool errors included.
func (e *testEnv) mcp(t *testing.T, key, tool string, args map[string]any) mcpToolResult {
	t.Helper()
	return decodeMCPResult(t, e.mcpCall(t, key, tool, args))
}

// mcpOK runs a tool that is expected to succeed.
func (e *testEnv) mcpOK(t *testing.T, key, tool string, args map[string]any) mcpToolResult {
	t.Helper()
	res := e.mcp(t, key, tool, args)
	if res.IsError {
		t.Fatalf("%s: %s", tool, res.text())
	}
	return res
}

// mcpUpload uploads one file over MCP and returns its structured output.
func (e *testEnv) mcpUpload(t *testing.T, key, name, kind, content string) map[string]any {
	t.Helper()
	res := e.mcpOK(t, key, "upload_file", map[string]any{
		"name": name, "kind": kind, "content": content,
	})
	return res.StructuredContent
}

// pathOf strips the test server origin so the URL can be fetched via e.do.
func (e *testEnv) pathOf(t *testing.T, url string) string {
	t.Helper()
	if !strings.HasPrefix(url, e.srv.URL) {
		t.Fatalf("url %q does not start with server origin %q", url, e.srv.URL)
	}
	return strings.TrimPrefix(url, e.srv.URL)
}

func TestMCPGateAndAuth(t *testing.T) {
	e := newEnv(t)

	// mcp_enabled off (default) → 403 for everyone.
	resp := e.mcpCall(t, "some-key", "search_files", map[string]any{"query": "x"})
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

	key := e.enableMCP(t)

	// Missing and invalid bearer tokens → 401.
	resp = e.mcpCall(t, "", "search_files", map[string]any{"query": "x"})
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
	resp = e.mcpCall(t, "rb_wrong", "search_files", map[string]any{"query": "x"})
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()

	// Valid key works.
	e.mcpOK(t, key, "search_files", map[string]any{"query": "x"})
}

func TestMCPUploadPublishFlow(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	out := e.mcpUpload(t, key, "发布测试", "markdown", "# Hello MCP")
	url, _ := out["url"].(string)
	slug, _ := out["slug"].(string)
	if url == "" || slug == "" {
		t.Fatalf("upload output missing url/slug: %v", out)
	}
	if isPublic, _ := out["is_public"].(bool); isPublic {
		t.Error("MCP uploads must start private")
	}

	// Private → the returned URL is not anonymously viewable yet.
	e.expect(t, http.StatusForbidden, http.MethodGet, e.pathOf(t, url), "", nil)

	// publish_file → same URL becomes anonymously viewable, rendered as HTML.
	e.mcpOK(t, key, "publish_file", map[string]any{"slug": slug})
	resp := e.do(t, http.MethodGet, e.pathOf(t, url), "", nil)
	if b := bodyOf(t, resp, http.StatusOK); !strings.Contains(b, "<h1") || !strings.Contains(b, "Hello MCP") {
		t.Errorf("published markdown did not render: %q", b)
	}
}

// TestMCPPublishWithExpiry covers the self-expiring publish: one call has to
// both make the file public and attach the limit, since a two-step version
// could leave it public with no limit if the second step failed.
func TestMCPPublishWithExpiry(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)
	ctx := context.Background()

	byViews := e.mcpUpload(t, key, "views", "markdown", "# v")
	viewsSlug, _ := byViews["slug"].(string)
	res := e.mcpOK(t, key, "publish_file", map[string]any{
		"slug": viewsSlug, "max_views": 2,
	})
	if !strings.Contains(res.text(), "2 more view") {
		t.Errorf("result %q should tell the agent how long the link lasts", res.text())
	}
	f, err := e.queries.GetFileBySlugAnyOwner(ctx, viewsSlug)
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if !f.IsPublic || !f.MaxViews.Valid || f.MaxViews.Int64 != 2 {
		t.Errorf("file = public %v, max_views %v; want public with a 2-view limit", f.IsPublic, f.MaxViews)
	}

	byTTL := e.mcpUpload(t, key, "ttl", "markdown", "# t")
	ttlSlug, _ := byTTL["slug"].(string)
	res = e.mcpOK(t, key, "publish_file", map[string]any{
		"slug": ttlSlug, "ttl": "7d",
	})
	if f, err = e.queries.GetFileBySlugAnyOwner(ctx, ttlSlug); err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if !f.IsPublic || !f.ExpiresAt.Valid {
		t.Errorf("file = public %v, expires_at %v; want public with a deadline", f.IsPublic, f.ExpiresAt)
	}

	// The same vocabulary and the same mutual-exclusion rule as the HTTP
	// endpoint, because both go through parseExpiryLimit.
	for _, args := range []map[string]any{
		{"slug": ttlSlug, "ttl": "99x"},
		{"slug": ttlSlug, "ttl": "24h", "max_views": 5},
		{"slug": ttlSlug, "max_views": 0},
	} {
		if res = e.mcp(t, key, "publish_file", args); !res.IsError {
			t.Errorf("publish_file%v should be a tool error", args)
		}
	}
}

func TestMCPUnpublish(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	out := e.mcpUpload(t, key, "doc", "markdown", "# hi")
	slug, _ := out["slug"].(string)
	url, _ := out["url"].(string)
	e.mcpOK(t, key, "publish_file", map[string]any{"slug": slug})
	e.expect(t, http.StatusOK, http.MethodGet, e.pathOf(t, url), "", nil)

	res := e.mcpOK(t, key, "unpublish_file", map[string]any{"slug": slug})
	if isPublic, _ := res.StructuredContent["is_public"].(bool); isPublic {
		t.Error("unpublish_file should report the file as private")
	}
	e.expect(t, http.StatusForbidden, http.MethodGet, e.pathOf(t, url), "", nil)

	// The document itself survives -- unpublishing is not deleting.
	resp := e.do(t, http.MethodGet, "/api/files/"+slug, "", e.authCookie(t))
	if got := decodeFile(t, resp, http.StatusOK); got.HTMLContent != "# hi" {
		t.Errorf("content = %q, want it untouched", got.HTMLContent)
	}

	if res = e.mcp(t, key, "unpublish_file", map[string]any{"slug": "nope"}); !res.IsError {
		t.Error("unpublish_file on an unknown slug should be a tool error")
	}
}

func TestMCPListFiles(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	// Empty project: a plain answer, not an error.
	res := e.mcp(t, key, "list_files", map[string]any{})
	if res.IsError || !strings.Contains(res.text(), "no documents") {
		t.Errorf("list_files on an empty project = %v %q", res.IsError, res.text())
	}

	e.mcpUpload(t, key, "first", "markdown", "# one")
	second := e.mcpUpload(t, key, "second", "html", "<p>two</p>")
	secondSlug, _ := second["slug"].(string)
	e.mcpOK(t, key, "publish_file", map[string]any{"slug": secondSlug, "max_views": 3})

	// Another user's files must not appear: list_files has no query to scope it,
	// so the owner filter in ListUserFiles is the only thing standing there.
	_, otherCookie := e.newUser(t, "other")
	e.createViaAPI(t, otherCookie, "not mine", "<p>hidden</p>")

	res = e.mcpOK(t, key, "list_files", map[string]any{})
	if total, _ := res.StructuredContent["total"].(float64); total != 2 {
		t.Errorf("total = %v, want 2 (only the key owner's files)", res.StructuredContent["total"])
	}
	text := res.text() // the foreign file's name is pinned absent by TestMCPOwnershipIsolation
	for _, want := range []string{"first", "second", "private", "3 more view"} {
		if !strings.Contains(text, want) {
			t.Errorf("list_files text %q missing %q", text, want)
		}
	}
}

func TestMCPUploadValidation(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	// txt is a web-UI kind but not an MCP upload kind.
	res := e.mcp(t, key, "upload_file", map[string]any{
		"name": "x", "kind": "txt", "content": "plain",
	})
	if !res.IsError || !strings.Contains(res.text(), "markdown or html") {
		t.Errorf("txt upload = %v %q, want kind error", res.IsError, res.text())
	}

	res = e.mcp(t, key, "upload_file", map[string]any{
		"name": "x", "kind": "html", "content": "",
	})
	if !res.IsError || !strings.Contains(res.text(), "content is required") {
		t.Errorf("empty content = %v %q, want content error", res.IsError, res.text())
	}
}

func TestMCPBatchUpload(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	res := e.mcpOK(t, key, "upload_files", map[string]any{
		"files": []map[string]any{
			{"name": "a", "kind": "markdown", "content": "# a"},
			{"name": "b", "kind": "html", "content": "<p>b</p>"},
			{"name": "bad", "kind": "pdf", "content": "x"},
		},
	})
	if got := res.StructuredContent["uploaded"].(float64); got != 2 {
		t.Errorf("uploaded = %v, want 2", got)
	}
	if got := res.StructuredContent["failed"].(float64); got != 1 {
		t.Errorf("failed = %v, want 1", got)
	}

	// Both successful files landed in the DB, attributed to the admin.
	files, err := e.queries.ListUserFiles(context.Background(), e.admin.ID)
	if err != nil {
		t.Fatalf("ListUserFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("db has %d files, want 2", len(files))
	}
	for _, f := range files {
		if f.UserID != e.admin.ID {
			t.Errorf("file %q user_id = %d, want %d", f.Slug, f.UserID, e.admin.ID)
		}
	}
}

func TestMCPSearch(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	e.mcpUpload(t, key, "部署文档", "markdown", "# 部署\n\n如何部署本项目。")
	e.mcpUpload(t, key, "另一篇", "html", "<p>这里提到了部署流程的细节。</p>")
	e.mcpUpload(t, key, "无关", "html", "<p>nothing</p>")

	res := e.mcpOK(t, key, "search_files", map[string]any{"query": "部署"})
	results := res.StructuredContent["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("search returned %d results, want 2", len(results))
	}
	// The content-only hit carries a snippet; the URL is present on both.
	var contentHit map[string]any
	for _, r := range results {
		m := r.(map[string]any)
		if m["url"] == nil || m["url"] == "" {
			t.Errorf("result missing url: %v", m)
		}
		if m["name"] == "另一篇" {
			contentHit = m
		}
	}
	if contentHit == nil || contentHit["snippet"] == nil || !strings.Contains(contentHit["snippet"].(string), "部署") {
		t.Errorf("content-only hit missing snippet: %v", contentHit)
	}

	// Miss → found=false with a human-readable message.
	res = e.mcpOK(t, key, "search_files", map[string]any{"query": "不存在的词"})
	if found := res.StructuredContent["found"].(bool); found {
		t.Error("miss should report found=false")
	}
	if !strings.Contains(res.text(), "No documents matching") {
		t.Errorf("miss text = %q", res.text())
	}
}

func TestMCPUpdate(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	out := e.mcpUpload(t, key, "旧标题", "markdown", "# v1")
	slug := out["slug"].(string)
	before, err := e.queries.GetFileBySlugAnyOwner(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}

	// Neither field → tool error.
	res := e.mcp(t, key, "update_file", map[string]any{"slug": slug})
	if !res.IsError {
		t.Error("update with nothing to change should error")
	}

	res = e.mcpOK(t, key, "update_file", map[string]any{
		"slug": slug, "name": "新标题", "content": "# v2",
	})

	after, err := e.queries.GetFileBySlugAnyOwner(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner after update: %v", err)
	}
	if content := string(e.content(t, after)); after.Name != "新标题" || content != "# v2" {
		t.Errorf("update result = %q %q", after.Name, content)
	}
	if after.Slug != before.Slug || after.AccessCode != before.AccessCode || after.Kind != before.Kind {
		t.Error("update must not change slug, access code, or kind")
	}
}

func TestMCPDeleteTwoPhase(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	out := e.mcpUpload(t, key, "待删除", "html", "<p>bye</p>")
	slug := out["slug"].(string)
	url := out["url"].(string)

	// Phase 1: no confirm → nothing deleted, name and URL surfaced for the user.
	res := e.mcpOK(t, key, "delete_file", map[string]any{"slug": slug})
	if deleted := res.StructuredContent["deleted"].(bool); deleted {
		t.Error("phase 1 must not delete")
	}
	if !strings.Contains(res.text(), "待删除") || !strings.Contains(res.text(), url) {
		t.Errorf("phase 1 text %q must carry name and URL", res.text())
	}
	if _, err := e.queries.GetFileBySlugAnyOwner(context.Background(), slug); err != nil {
		t.Fatalf("file should still exist after phase 1: %v", err)
	}

	// Phase 2: confirm=true → soft-deleted (in trash, not gone permanently).
	res = e.mcpOK(t, key, "delete_file", map[string]any{"slug": slug, "confirm": true})
	if deleted := res.StructuredContent["deleted"].(bool); !deleted {
		t.Error("phase 2 should report deleted=true")
	}
	if _, err := e.queries.GetFileBySlugAnyOwner(context.Background(), slug); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("file should be soft-deleted, got err=%v", err)
	}
	trashed, err := e.queries.ListUserDeletedFiles(context.Background(), e.admin.ID)
	if err != nil {
		t.Fatalf("ListUserDeletedFiles: %v", err)
	}
	if len(trashed) != 1 || trashed[0].Slug != slug {
		t.Errorf("trash = %v, want the deleted file", trashed)
	}
}

func TestMCPOwnershipIsolation(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	// A second user owns a file; the admin's key must not see or touch it.
	other, _ := e.newUser(t, "other")
	theirs, err := e.queries.CreateFile(context.Background(), sqlcgen.CreateFileParams{
		Slug: "their-doc", Name: "机密文档", BlobID: e.storeBlob(t, []byte("<p>secret</p>")), Kind: "html",
		AccessCode: "code1234", UserID: other.ID,
	})
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	// search does not surface it…
	res := e.mcp(t, key, "search_files", map[string]any{"query": "机密"})
	if found := res.StructuredContent["found"].(bool); found {
		t.Error("search must not return another user's files")
	}
	// …list does not either…
	res = e.mcp(t, key, "list_files", map[string]any{})
	if strings.Contains(res.text(), "机密") {
		t.Error("list must not return another user's files")
	}
	// …and every slug-taking tool answers "not found". Each one must appear
	// here: a new tool that forgets ownedFile is exactly the bug this catches.
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"update_file", map[string]any{"slug": theirs.Slug, "name": "hacked"}},
		{"publish_file", map[string]any{"slug": theirs.Slug}},
		{"unpublish_file", map[string]any{"slug": theirs.Slug}},
		{"delete_file", map[string]any{"slug": theirs.Slug, "confirm": true}},
	} {
		res := e.mcp(t, key, call.tool, call.args)
		if !res.IsError || !strings.Contains(res.text(), "not found") {
			t.Errorf("%s on foreign file = %v %q, want not-found error", call.tool, res.IsError, res.text())
		}
	}
	after, err := e.queries.GetFileBySlugAnyOwner(context.Background(), theirs.Slug)
	if err != nil {
		t.Errorf("foreign file should be untouched: %v", err)
	} else if after.Name != theirs.Name || after.IsPublic {
		t.Errorf("foreign file was mutated: %+v", after)
	}
}

// TestMCPToolsAreAllTested walks the advertised tool list so a tool added
// without a test fails here rather than shipping unexercised.
func TestMCPToolsAreAllTested(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	resp := e.send(t, http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), nil,
		map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "application/json, text/event-stream",
			"Authorization": "Bearer " + key,
		}, true)
	envelope := decodeJSON[struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}](t, resp, http.StatusOK)

	// Each entry names the test that exercises the tool.
	tested := map[string]string{
		"upload_file":    "TestMCPUploadPublishFlow",
		"upload_files":   "TestMCPBatchUpload",
		"list_files":     "TestMCPListFiles",
		"search_files":   "TestMCPSearch",
		"update_file":    "TestMCPUpdate",
		"publish_file":   "TestMCPPublishWithExpiry",
		"unpublish_file": "TestMCPUnpublish",
		"delete_file":    "TestMCPDeleteTwoPhase",
		"upload_site":    "TestMCPSiteUploadAndUpdate",
		"update_site":    "TestMCPSiteUploadAndUpdate",
	}
	if len(envelope.Result.Tools) != len(tested) {
		t.Errorf("server advertises %d tools, the test map lists %d",
			len(envelope.Result.Tools), len(tested))
	}
	for _, tool := range envelope.Result.Tools {
		if tested[tool.Name] == "" {
			t.Errorf("MCP tool %q has no test — add one and list it here", tool.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Owner isolation.
//
// Every /api/files endpoint is owner-scoped, so another user's slug must be
// indistinguishable from one that never existed: 404, identical body, and no
// mutation. These tests are the executable form of that rule.
// ---------------------------------------------------------------------------

// foreignCase is one endpoint exercised as a non-owner. body must be valid
// enough to pass the handler's own validation, or the request would 400
// before it ever reaches the ownership check and the case would prove nothing.
type foreignCase struct {
	method string
	path   string // %s is replaced by the victim's slug
	body   string
	// trashed marks cases that only make sense against a soft-deleted file.
	trashed bool
}

// foreignCases must cover every slug-keyed route under /api/files.
// TestFileEndpointsCoverAllRoutes fails if a new route appears without one.
var foreignCases = []foreignCase{
	{method: http.MethodGet, path: "/api/files/%s"},
	{method: http.MethodGet, path: "/api/files/%s/download"},
	{method: http.MethodPatch, path: "/api/files/%s",
		body: `{"name":"hacked","slug":"hacked-slug","html_content":"<p>hacked</p>","access_code":"hackedcode"}`},
	{method: http.MethodPatch, path: "/api/files/%s/name", body: `{"name":"hacked"}`},
	{method: http.MethodPatch, path: "/api/files/%s/visibility", body: `{"is_public":true}`},
	{method: http.MethodPatch, path: "/api/files/%s/tags", body: `{"tags":"hacked"}`},
	{method: http.MethodPatch, path: "/api/files/%s/expiry", body: `{"ttl":"24h"}`},
	{method: http.MethodPost, path: "/api/files/%s/refresh-code"},
	{method: http.MethodDelete, path: "/api/files/%s"},
	{method: http.MethodPost, path: "/api/files/%s/restore", trashed: true},
	{method: http.MethodDelete, path: "/api/files/%s/permanent", trashed: true},
	// The body is a valid site zip, so the request would succeed if the
	// ownership check did not stop it first.
	{method: http.MethodPut, path: "/api/files/%s/site",
		body: mustSiteZip(map[string]string{"index.html": "<p>hacked</p>"})},
}

// fingerprint is every column a foreignCase could plausibly mutate, so the
// "nothing changed" assertion catches an endpoint that 404s and writes anyway.
func (e *testEnv) fingerprint(t *testing.T, slug string) string {
	t.Helper()
	f, err := e.queries.GetFileBySlugAnyOwner(context.Background(), slug)
	if err != nil {
		t.Fatalf("fingerprint %q: %v", slug, err)
	}
	return fmt.Sprintf("name=%q blob=%s content=%q public=%v code=%q tags=%q expires=%v maxviews=%v",
		f.Name, f.BlobID, e.content(t, f), f.IsPublic, f.AccessCode, f.Tags, f.ExpiresAt.Valid, f.MaxViews.Valid)
}

// trashFingerprint is fingerprint for a soft-deleted file, which
// GetFileBySlugAnyOwner filters out by design.
func (e *testEnv) trashFingerprint(t *testing.T, ownerID int64, slug string) string {
	t.Helper()
	rows, err := e.queries.ListUserDeletedFiles(context.Background(), ownerID)
	if err != nil {
		t.Fatalf("ListUserDeletedFiles: %v", err)
	}
	for _, f := range rows {
		if f.Slug != slug {
			continue
		}
		// The point of this fingerprint is that the content is untouched: the
		// same blob, holding the same bytes.
		return fmt.Sprintf("name=%q blob=%s content=%q public=%v code=%q tags=%q",
			f.Name, f.BlobID, e.content(t, f), f.IsPublic, f.AccessCode, f.Tags)
	}
	t.Fatalf("file %q is no longer in user %d's trash", slug, ownerID)
	return ""
}

func TestFileEndpointsRejectForeignSlug(t *testing.T) {
	// Run both directions: the super admin gets no exception on file
	// endpoints, which is otherwise an invisible policy someone could
	// "fix" as a bug.
	for _, dir := range []struct {
		name              string
		ownerIsSuperAdmin bool
	}{
		{"owner=admin attacker=user", true},
		{"owner=user attacker=admin", false},
	} {
		t.Run(dir.name, func(t *testing.T) {
			e := newEnv(t)
			adminCookie := e.authCookie(t)
			user, userCookie := e.newUser(t, "other")

			ownerCookie, attackerCookie := adminCookie, userCookie
			ownerID := e.admin.ID
			if !dir.ownerIsSuperAdmin {
				ownerCookie, attackerCookie = userCookie, adminCookie
				ownerID = user.ID
			}

			active := e.createViaAPI(t, ownerCookie, "victim active", "<p>secret</p>")
			trashed := e.createViaAPI(t, ownerCookie, "victim trashed", "<p>secret2</p>")
			e.trash(t, ownerCookie, trashed.Slug)

			activeBefore := e.fingerprint(t, active.Slug)
			trashedBefore := e.trashFingerprint(t, ownerID, trashed.Slug)

			for _, c := range foreignCases {
				victim := active.Slug
				if c.trashed {
					victim = trashed.Slug
				}
				path := fmt.Sprintf(c.path, victim)
				resp := e.do(t, c.method, path, c.body, attackerCookie)
				if resp.StatusCode != http.StatusNotFound {
					t.Errorf("%s %s as non-owner = %d, want 404", c.method, path, resp.StatusCode)
				}
				resp.Body.Close()
			}

			if got := e.fingerprint(t, active.Slug); got != activeBefore {
				t.Errorf("active file was mutated by a rejected request:\n got %s\nwant %s", got, activeBefore)
			}
			// Still in the owner's trash: neither restored nor purged.
			if got := e.trashFingerprint(t, ownerID, trashed.Slug); got != trashedBefore {
				t.Errorf("trashed file was mutated by a rejected request:\n got %s\nwant %s", got, trashedBefore)
			}
		})
	}
}

// isFileRoute reports whether a route reads or writes files, and therefore owes
// this file an ownership test. /api/search and /api/trash are file endpoints
// that deliberately don't live under /api/files -- see the static-segment check
// in TestFileEndpointsCoverAllRoutes for why.
func isFileRoute(route string) bool {
	return strings.HasPrefix(route, "/api/files") ||
		route == "/api/search" || route == "/api/trash" || route == "/api/sites"
}

// TestFileEndpointsCoverAllRoutes walks the real router so that adding a file
// endpoint without an ownership case fails a test that already exists, rather
// than silently going untested. It also enforces the shape of the /api/files
// subtree, which is a correctness property in its own right.
func TestFileEndpointsCoverAllRoutes(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range foreignCases {
		// Normalise "/api/files/%s/name" to chi's "/api/files/{slug}/name".
		covered[c.method+" "+strings.ReplaceAll(c.path, "%s", "{slug}")] = true
	}
	// Routes that are not slug-keyed and so cannot leak another user's file.
	notSlugKeyed := map[string]bool{
		http.MethodGet + " /api/files":    true, // covered by TestListScopedToOwner
		http.MethodPost + " /api/files":   true, // creates, attributed to caller
		http.MethodGet + " /api/search":   true, // covered by TestSearchScopedToOwnFiles
		http.MethodDelete + " /api/trash": true, // covered by TestEmptyTrash
		http.MethodPost + " /api/sites":   true, // creates, attributed to caller
	}

	seen := 0
	err := chi.Walk(server.New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).(*chi.Mux),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			route = strings.TrimSuffix(route, "/")
			if !isFileRoute(route) {
				return nil
			}
			seen++
			key := method + " " + route
			if !covered[key] && !notSlugKeyed[key] {
				t.Errorf("route %q has no ownership test. Every file endpoint must be "+
					"owner-scoped: add a foreignCase (or list it in notSlugKeyed with a reason).", key)
			}
			// chi matches a static segment before a parameter, so a static
			// route at the {slug} level shadows any file whose custom slug
			// happens to equal it: that file's own endpoint would answer with
			// the other handler's response instead. /api/files/search used to
			// do exactly this, which is why search moved to /api/search and
			// emptying the trash to /api/trash. Nothing may go back.
			if rest, ok := strings.CutPrefix(route, "/api/files/"); ok {
				if seg, _, _ := strings.Cut(rest, "/"); seg != "{slug}" {
					t.Errorf("route %q puts the static segment %q where a slug goes; "+
						"a file with that slug becomes unreachable through its own endpoint. "+
						"Mount it outside /api/files instead.", key, seg)
				}
			}
			return nil
		})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	// A prefix filter that matches nothing would make this test vacuous.
	if want := len(foreignCases) + len(notSlugKeyed); seen != want {
		t.Errorf("walked %d file routes, expected %d — the route table and the "+
			"ownership table have drifted apart", seen, want)
	}
}

func TestListScopedToOwner(t *testing.T) {
	e := newEnv(t)
	adminCookie := e.authCookie(t)
	_, otherCookie := e.newUser(t, "other")

	e.createViaAPI(t, adminCookie, "admin one", "<p>1</p>")
	adminTrash := e.createViaAPI(t, adminCookie, "admin two", "<p>2</p>")
	otherFile := e.createViaAPI(t, otherCookie, "other one", "<p>3</p>")

	list := func(cookie *http.Cookie, path string) []fileResp {
		t.Helper()
		return decodeJSON[[]fileResp](t, e.do(t, http.MethodGet, path, "", cookie), http.StatusOK)
	}

	if got := list(adminCookie, "/api/files"); len(got) != 2 {
		t.Errorf("admin sees %d files, want only their own 2", len(got))
	}
	other := list(otherCookie, "/api/files")
	if len(other) != 1 || other[0].Slug != otherFile.Slug {
		t.Errorf("other user sees %+v, want only their own file", other)
	}

	// Trash is scoped too.
	e.trash(t, adminCookie, adminTrash.Slug)

	if got := list(adminCookie, "/api/files?deleted=true"); len(got) != 1 {
		t.Errorf("admin trash has %d files, want 1", len(got))
	}
	if got := list(otherCookie, "/api/files?deleted=true"); len(got) != 0 {
		t.Errorf("other user's trash = %+v, want empty", got)
	}
}

// TestRenderIgnoresForeignSession pins the /res/{slug} half of the same bug:
// a session used to bypass the access code for *any* file, not just the
// viewer's own.
func TestRenderIgnoresForeignSession(t *testing.T) {
	e := newEnv(t)
	adminCookie := e.authCookie(t)
	_, otherCookie := e.newUser(t, "other")

	file := e.createViaAPI(t, adminCookie, "private", "<h1>secret</h1>")

	// A signed-in stranger gets no more than an anonymous visitor: the file
	// is private, so no code can help and the attempt is counted as failed.
	resp := e.do(t, http.MethodGet, "/res/"+file.Slug, "", otherCookie)
	if b := bodyOf(t, resp, http.StatusForbidden); strings.Contains(b, "secret") {
		t.Error("a foreign session must not receive the file's content")
	}
	after, err := e.queries.GetFileBySlugAnyOwner(context.Background(), file.Slug)
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if after.FailureCount != 1 || after.SuccessCount != 0 {
		t.Errorf("counters = success %d / failure %d, want 0 / 1",
			after.SuccessCount, after.FailureCount)
	}

	// The owner still gets in without a code.
	e.expect(t, http.StatusOK, http.MethodGet, "/res/"+file.Slug, "", adminCookie)

	// Once public, the stranger's correct code works and counts as a
	// code-based view rather than an owner view.
	e.publish(t, adminCookie, file.Slug)

	e.expect(t, http.StatusOK, http.MethodGet, "/res/"+file.Slug+"?code="+file.AccessCode, "", otherCookie)

	after, err = e.queries.GetFileBySlugAnyOwner(context.Background(), file.Slug)
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if after.CodeSuccessCount != 1 || after.SuccessCount != 1 {
		t.Errorf("counters = success %d / code %d, want 1 / 1",
			after.SuccessCount, after.CodeSuccessCount)
	}
}

// --- API-key REST access ---

// apiKeyFor turns on mcp_enabled and issues an API key for userID — the only
// state the real settings flow can produce a key in.
func (e *testEnv) apiKeyFor(t *testing.T, userID int64) string {
	t.Helper()
	e.setConfig(t, "mcp_enabled", "true")
	key, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := e.queries.SetUserAPIKey(context.Background(), sqlcgen.SetUserAPIKeyParams{
		ApiKey: sql.NullString{String: key, Valid: true},
		ID:     userID,
	}); err != nil {
		t.Fatalf("SetUserAPIKey: %v", err)
	}
	return key
}

// doKey is do with a Bearer API key instead of a session cookie.
func (e *testEnv) doKey(t *testing.T, method, path, body, key string) *http.Response {
	t.Helper()
	header := map[string]string{"Authorization": "Bearer " + key}
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
		header["Content-Type"] = "application/json"
	}
	return e.send(t, method, path, r, nil, header, true)
}

func TestAPIKeyRESTAccess(t *testing.T) {
	e := newEnv(t)
	user, _ := e.newUser(t, "bob")
	key := e.apiKeyFor(t, user.ID)

	// Create, then read back, through the key alone.
	resp := e.doKey(t, http.MethodPost, "/api/files",
		`{"name":"via key","html_content":"<p>hi</p>"}`, key)
	created := decodeFile(t, resp, http.StatusCreated)

	resp = e.doKey(t, http.MethodGet, "/api/files/"+created.Slug, "", key)
	if got := decodeFile(t, resp, http.StatusOK); got.HTMLContent != "<p>hi</p>" {
		t.Errorf("content = %q, want the uploaded source", got.HTMLContent)
	}

	// The key resolves to its owner, not to whoever happens to be user 1.
	resp = e.doKey(t, http.MethodGet, "/api/auth/me", "", key)
	if body := bodyOf(t, resp, http.StatusOK); !strings.Contains(body, `"bob"`) {
		t.Errorf("me = %s, want username bob", body)
	}

	// Owner scoping is unchanged: bob's key can't see the admin's file.
	adminFile := e.createViaAPI(t, e.authCookie(t), "admins", "<p>secret</p>")
	e.expectKeyGET(t, http.StatusNotFound, "/api/files/"+adminFile.Slug, key)

	// A key that matches no row is a plain 401.
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/files", "", nil) // no credential at all
	e.expectKeyGET(t, http.StatusUnauthorized, "/api/files", "rb_nope")
}

// TestAPIKeyRESTGates pins the two kill switches: the mcp_enabled config and
// account suspension both make every key dead on its next request.
func TestAPIKeyRESTGates(t *testing.T) {
	e := newEnv(t)
	user, _ := e.newUser(t, "carol")
	key := e.apiKeyFor(t, user.ID)

	e.expectKeyGET(t, http.StatusOK, "/api/files", key)

	e.setConfig(t, "mcp_enabled", "false")
	e.expectKeyGET(t, http.StatusUnauthorized, "/api/files", key)

	e.setConfig(t, "mcp_enabled", "true")
	if _, err := e.queries.DisableUser(context.Background(), user.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	e.expectKeyGET(t, http.StatusUnauthorized, "/api/files", key)
}

// TestAPIKeyCannotUseSuperAdminEndpoints pins the escalation boundary: even
// the super admin's own key must not unlock the surfaces that expose other
// accounts' data. api_key is stored in plaintext on the accepted premise that
// a key can never reach /api/backup; a key that could would carry every hash
// and every other key out in one request.
func TestAPIKeyCannotUseSuperAdminEndpoints(t *testing.T) {
	e := newEnv(t)
	adminKey := e.apiKeyFor(t, e.admin.ID)

	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/backup", ""},
		{http.MethodPost, "/api/backup/restore", "not-a-db"},
		{http.MethodPut, "/api/settings", `{"allow_registration":true}`},
		{http.MethodGet, "/api/admin/users", ""},
		{http.MethodPost, "/api/admin/users", `{"username":"eve"}`},
	} {
		resp := e.doKey(t, tc.method, tc.path, tc.body, adminKey)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with admin key: status = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
		bodyString(t, resp)
	}

	// The same requests keep working over the admin's session.
	resp := e.do(t, http.MethodGet, "/api/admin/users", "", e.authCookie(t))
	bodyOf(t, resp, http.StatusOK)

	// A non-admin key fails the role check before the auth-method check.
	user, _ := e.newUser(t, "dave")
	userKey := e.apiKeyFor(t, user.ID)
	e.expectKeyGET(t, http.StatusForbidden, "/api/backup", userKey)
}

// TestUploadDefaultVisibilityConfig pins the upload_default_public config: off
// (or absent) means files are created private; on means the HTTP create path
// starts them public — while MCP uploads stay private either way, because the
// upload tools promise that and publish_file is the explicit consent step.
func TestUploadDefaultVisibilityConfig(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	if f := e.createViaAPI(t, cookie, "before", "<p>a</p>"); f.IsPublic {
		t.Error("with the config absent, a new file is public, want private")
	}

	resp := e.do(t, http.MethodPut, "/api/settings", `{"upload_default_public":true}`, cookie)
	if body := bodyOf(t, resp, http.StatusOK); !strings.Contains(body, `"upload_default_public":true`) {
		t.Errorf("settings after update = %s, want upload_default_public true", body)
	}

	f := e.createViaAPI(t, cookie, "after", "<p>b</p>")
	if !f.IsPublic {
		t.Error("with the config on, a new file is private, want public")
	}
	// And the link works anonymously right away.
	resp = e.do(t, http.MethodGet, "/res/"+f.Slug+"?code="+f.AccessCode, "", nil)
	bodyOf(t, resp, http.StatusOK)

	key := e.apiKeyFor(t, e.admin.ID)
	if out := e.mcpUpload(t, key, "agent-doc", "markdown", "# hi"); out["is_public"] != false {
		t.Errorf("MCP upload with the config on: is_public = %v, want false", out["is_public"])
	}
}
