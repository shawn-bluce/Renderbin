package server_test

import (
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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/config"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
	"github.com/shawn-bluce/renderbin/backend/internal/server"
)

const (
	testUser = "admin"
	testPass = "s3cret"
)

type testEnv struct {
	srv     *httptest.Server
	queries *sqlcgen.Queries
	conn    *sql.DB
	admin   sqlcgen.User
}

// newBareEnv starts a server against an empty database — no users yet, the
// state the first-run /api/setup flow expects.
func newBareEnv(t *testing.T) *testEnv {
	return newBareEnvWithConfig(t, config.Default())
}

func newBareEnvWithConfig(t *testing.T, cfg config.Runtime) *testEnv {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	queries := sqlcgen.New(conn)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(server.NewWithConfig(queries, conn, logger, cfg))
	t.Cleanup(srv.Close)

	return &testEnv{srv: srv, queries: queries, conn: conn}
}

// newEnv additionally seeds the super admin (id=1, testUser/testPass), the
// state every post-setup test wants.
func newEnv(t *testing.T) *testEnv {
	return newEnvWithConfig(t, config.Default())
}

func newEnvWithConfig(t *testing.T, cfg config.Runtime) *testEnv {
	t.Helper()
	e := newBareEnvWithConfig(t, cfg)
	hash, err := auth.HashPassword(testPass)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	admin, err := e.queries.CreateUser(context.Background(), sqlcgen.CreateUserParams{
		Username:     testUser,
		Nickname:     "Admin",
		PasswordHash: hash,
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
	hash, err := auth.HashPassword("secret2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	user, err := e.queries.CreateUser(context.Background(), sqlcgen.CreateUserParams{
		Username:     username,
		Nickname:     username,
		PasswordHash: hash,
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	return user, e.cookieFor(t, user.ID)
}

func (e *testEnv) do(t *testing.T, method, path, body string, cookie *http.Cookie) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, r)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
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

func decodeFile(t *testing.T, resp *http.Response) fileResp {
	t.Helper()
	defer resp.Body.Close()
	var f fileResp
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		t.Fatalf("decode file response: %v", err)
	}
	return f
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

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Errorf("status = %d, want %d", resp.StatusCode, want)
	}
}

// createViaAPI creates a private file through the authenticated endpoint and
// returns the decoded response.
func (e *testEnv) createViaAPI(t *testing.T, cookie *http.Cookie, name, html string) fileResp {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "html_content": html})
	resp := e.do(t, http.MethodPost, "/api/files", string(body), cookie)
	assertStatus(t, resp, http.StatusCreated)
	return decodeFile(t, resp)
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	resp := e.do(t, http.MethodGet, "/api/health", "", nil)
	assertStatus(t, resp, http.StatusOK)
	if b := bodyString(t, resp); !strings.Contains(b, `"status":"ok"`) {
		t.Errorf("health body = %q", b)
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t)

	// Bad JSON.
	resp := e.do(t, http.MethodPost, "/api/auth/login", "{not json", nil)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Wrong credentials.
	resp = e.do(t, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"nope"}`, nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()

	// Correct credentials -> 204 + Set-Cookie.
	resp = e.do(t, http.MethodPost, "/api/auth/login", `{"username":"admin","password":"s3cret"}`, nil)
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
	resp := e.do(t, http.MethodGet, "/api/auth/me", "", nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()

	// Authenticated.
	cookie := e.authCookie(t)
	resp = e.do(t, http.MethodGet, "/api/auth/me", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	var me struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	resp.Body.Close()
	if me.Username != testUser {
		t.Errorf("me username = %q, want %q", me.Username, testUser)
	}

	// Logout always 204.
	resp = e.do(t, http.MethodPost, "/api/auth/logout", "", cookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	// The session should no longer be valid.
	resp = e.do(t, http.MethodGet, "/api/auth/me", "", cookie)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
}

func TestRequireAuthGuardsFiles(t *testing.T) {
	e := newEnv(t)
	resp := e.do(t, http.MethodGet, "/api/files", "", nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
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
	if len(created.AccessCode) != 8 {
		t.Errorf("access code length = %d, want 8", len(created.AccessCode))
	}

	// Get returns full content.
	resp := e.do(t, http.MethodGet, "/api/files/"+created.Slug, "", cookie)
	assertStatus(t, resp, http.StatusOK)
	got := decodeFile(t, resp)
	if got.HTMLContent != "<h1>v1</h1>" {
		t.Errorf("Get html_content = %q", got.HTMLContent)
	}

	// List includes it.
	resp = e.do(t, http.MethodGet, "/api/files", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	var list []fileResp
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	resp.Body.Close()
	if len(list) != 1 || list[0].Slug != created.Slug {
		t.Errorf("list = %d items, want the created file", len(list))
	}

	// Update name, slug, content, access code.
	upd := `{"name":"renamed","slug":"custom-slug","html_content":"<h1>v2</h1>","access_code":"mycode12"}`
	resp = e.do(t, http.MethodPatch, "/api/files/"+created.Slug, upd, cookie)
	assertStatus(t, resp, http.StatusOK)
	updated := decodeFile(t, resp)
	if updated.Slug != "custom-slug" || updated.Name != "renamed" {
		t.Errorf("update result = %+v", updated)
	}
	if updated.AccessCode != "mycode12" {
		t.Errorf("access code = %q, want custom code", updated.AccessCode)
	}

	// Old slug is gone.
	resp = e.do(t, http.MethodGet, "/api/files/"+created.Slug, "", cookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

func TestUpdateValidation(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	a := e.createViaAPI(t, cookie, "a", "<p>a</p>")
	b := e.createViaAPI(t, cookie, "b", "<p>b</p>")

	// Invalid slug.
	resp := e.do(t, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"bad slug!","html_content":"<p>x</p>","access_code":"ok123456"}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Empty content.
	resp = e.do(t, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"ok","html_content":"","access_code":"ok123456"}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Empty access code (would open the file to code-less requests).
	resp = e.do(t, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"ok","html_content":"<p>x</p>","access_code":""}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Access code with invalid characters.
	resp = e.do(t, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"ok","html_content":"<p>x</p>","access_code":"has space"}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Slug collision -> 409.
	resp = e.do(t, http.MethodPatch, "/api/files/"+a.Slug,
		`{"name":"x","slug":"`+b.Slug+`","html_content":"<p>x</p>","access_code":"ok123456"}`, cookie)
	assertStatus(t, resp, http.StatusConflict)
	resp.Body.Close()
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// Missing html_content.
	resp := e.do(t, http.MethodPost, "/api/files", `{"name":"x","html_content":""}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Over 5MB.
	big := strings.Repeat("a", (5<<20)+1)
	body, _ := json.Marshal(map[string]string{"name": "big", "html_content": big})
	resp = e.do(t, http.MethodPost, "/api/files", string(body), cookie)
	assertStatus(t, resp, http.StatusRequestEntityTooLarge)
	resp.Body.Close()

	// Unknown kind -> 400.
	resp = e.do(t, http.MethodPost, "/api/files", `{"name":"x","kind":"pdf","html_content":"x"}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
}

func TestConfiguredFileSizeLimitAcrossRESTCreateAndUpdate(t *testing.T) {
	cfg := config.Default()
	cfg.MaxFileSizeMB = 20
	cfg.MaxFileSizeBytes = 20 << 20
	e := newEnvWithConfig(t, cfg)
	cookie := e.authCookie(t)

	body, err := json.Marshal(map[string]string{
		"name": "ten-mib", "kind": "html", "html_content": strings.Repeat("a", 10<<20),
	})
	if err != nil {
		t.Fatalf("marshal upload: %v", err)
	}
	resp := e.do(t, http.MethodPost, "/api/files", string(body), cookie)
	assertStatus(t, resp, http.StatusCreated)
	created := decodeFile(t, resp)

	body, err = json.Marshal(map[string]string{
		"name": "nineteen-mib", "kind": "html", "html_content": strings.Repeat("b", 19<<20),
	})
	if err != nil {
		t.Fatalf("marshal 19MiB upload: %v", err)
	}
	resp = e.do(t, http.MethodPost, "/api/files", string(body), cookie)
	assertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	// encoding/json expands each control byte to a six-byte \u00XX escape.
	// The decoded file is valid and below 20 MiB even though its request body
	// exceeds the old 2x transport allowance.
	body, err = json.Marshal(map[string]string{
		"name": "escaped", "kind": "html", "html_content": strings.Repeat("\x01", 7<<20),
	})
	if err != nil {
		t.Fatalf("marshal escaped upload: %v", err)
	}
	resp = e.do(t, http.MethodPost, "/api/files", string(body), cookie)
	assertStatus(t, resp, http.StatusCreated)
	resp.Body.Close()

	body, err = json.Marshal(map[string]string{
		"name": "over-twenty-mib", "kind": "html", "html_content": strings.Repeat("c", (20<<20)+1),
	})
	if err != nil {
		t.Fatalf("marshal oversized upload: %v", err)
	}
	resp = e.do(t, http.MethodPost, "/api/files", string(body), cookie)
	assertStatus(t, resp, http.StatusRequestEntityTooLarge)
	if msg := bodyString(t, resp); !strings.Contains(msg, "20MB") {
		t.Errorf("oversized response = %q, want configured 20MB limit", strings.TrimSpace(msg))
	}

	body, err = json.Marshal(map[string]string{
		"name": created.Name, "slug": created.Slug, "access_code": created.AccessCode,
		"html_content": strings.Repeat("d", 19<<20),
	})
	if err != nil {
		t.Fatalf("marshal 19MiB update: %v", err)
	}
	resp = e.do(t, http.MethodPatch, "/api/files/"+created.Slug, string(body), cookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

func TestCreateKind(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// An explicit kind round-trips in the response.
	resp := e.do(t, http.MethodPost, "/api/files", `{"name":"notes","kind":"markdown","html_content":"# hi"}`, cookie)
	assertStatus(t, resp, http.StatusCreated)
	if got := decodeFile(t, resp); got.Kind != "markdown" {
		t.Errorf("kind = %q, want markdown", got.Kind)
	}

	// Omitting kind defaults to html (back-compat).
	resp = e.do(t, http.MethodPost, "/api/files", `{"name":"page","html_content":"<p>x</p>"}`, cookie)
	assertStatus(t, resp, http.StatusCreated)
	if got := decodeFile(t, resp); got.Kind != "html" {
		t.Errorf("default kind = %q, want html", got.Kind)
	}
}

func TestTrashAndRestore(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>x</p>")

	// Delete -> 204.
	resp := e.do(t, http.MethodDelete, "/api/files/"+f.Slug, "", cookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	// Not in active list.
	resp = e.do(t, http.MethodGet, "/api/files", "", cookie)
	if b := bodyString(t, resp); strings.Contains(b, f.Slug) {
		t.Error("deleted file still in active list")
	}

	// In trashed list.
	resp = e.do(t, http.MethodGet, "/api/files?deleted=true", "", cookie)
	if b := bodyString(t, resp); !strings.Contains(b, f.Slug) {
		t.Error("deleted file not in trashed list")
	}

	// Restore -> 200.
	resp = e.do(t, http.MethodPost, "/api/files/"+f.Slug+"/restore", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Restoring again -> 404 (no longer deleted).
	resp = e.do(t, http.MethodPost, "/api/files/"+f.Slug+"/restore", "", cookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

func TestHardDelete(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>x</p>")

	// Active file -> 404 (only trashed files can be purged).
	resp := e.do(t, http.MethodDelete, "/api/files/"+f.Slug+"/permanent", "", cookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()

	// Soft delete, then purge -> 204.
	resp = e.do(t, http.MethodDelete, "/api/files/"+f.Slug, "", cookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
	resp = e.do(t, http.MethodDelete, "/api/files/"+f.Slug+"/permanent", "", cookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	// Gone from the trashed list.
	resp = e.do(t, http.MethodGet, "/api/files?deleted=true", "", cookie)
	if b := bodyString(t, resp); strings.Contains(b, f.Slug) {
		t.Error("purged file still in trashed list")
	}

	// Purging again -> 404; restore is impossible too.
	resp = e.do(t, http.MethodDelete, "/api/files/"+f.Slug+"/permanent", "", cookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
	resp = e.do(t, http.MethodPost, "/api/files/"+f.Slug+"/restore", "", cookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()

	// The freed slug can be taken by another file.
	other := e.createViaAPI(t, cookie, "other", "<p>y</p>")
	upd := `{"name":"other","slug":"` + f.Slug + `","html_content":"<p>y</p>","access_code":"ok123456"}`
	resp = e.do(t, http.MethodPatch, "/api/files/"+other.Slug, upd, cookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

func TestSetExpiry(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>x</p>")

	// max_views forces public.
	resp := e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"max_views":3}`, cookie)
	assertStatus(t, resp, http.StatusOK)
	got := decodeFile(t, resp)
	if !got.IsPublic {
		t.Error("setting a view limit must force public")
	}
	if got.MaxViews == nil || *got.MaxViews != 3 {
		t.Errorf("max_views = %v, want 3", got.MaxViews)
	}

	// ttl sets an expiry timestamp.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"ttl":"24h"}`, cookie)
	assertStatus(t, resp, http.StatusOK)
	got = decodeFile(t, resp)
	if got.ExpiresAt == nil {
		t.Error("ttl should set expires_at")
	}
	if got.MaxViews != nil {
		t.Error("ttl should clear max_views")
	}

	// Mutually exclusive -> 400.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"ttl":"24h","max_views":5}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// An arbitrary amount plus a unit is accepted, not just the old presets.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"ttl":"3mo"}`, cookie)
	assertStatus(t, resp, http.StatusOK)
	got = decodeFile(t, resp)
	if got.ExpiresAt == nil {
		t.Error("a calendar-unit ttl should set expires_at")
	}

	// Invalid ttl -> 400.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"ttl":"99x"}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Non-positive max_views -> 400.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{"max_views":0}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Clearing keeps visibility (currently public) and nulls the limits.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/expiry", `{}`, cookie)
	assertStatus(t, resp, http.StatusOK)
	got = decodeFile(t, resp)
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
	assertStatus(t, resp, http.StatusOK)
	if got := decodeFile(t, resp); got.Name != "new name" {
		t.Errorf("name = %q, want %q", got.Name, "new name")
	}

	// Empty/whitespace name -> 400.
	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/name", `{"name":"  "}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Unknown slug -> 404.
	resp = e.do(t, http.MethodPatch, "/api/files/nope/name", `{"name":"x"}`, cookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

func TestVisibilityTagsRefresh(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>x</p>")

	resp := e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/visibility", `{"is_public":true}`, cookie)
	assertStatus(t, resp, http.StatusOK)
	if got := decodeFile(t, resp); !got.IsPublic {
		t.Error("visibility not set to public")
	}

	resp = e.do(t, http.MethodPatch, "/api/files/"+f.Slug+"/tags", `{"tags":" a , b ,a "}`, cookie)
	assertStatus(t, resp, http.StatusOK)
	if got := decodeFile(t, resp); got.Tags != "a,b" {
		t.Errorf("tags = %q, want normalized %q", got.Tags, "a,b")
	}

	resp = e.do(t, http.MethodPost, "/api/files/"+f.Slug+"/refresh-code", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	if got := decodeFile(t, resp); got.AccessCode == f.AccessCode || got.AccessCode == "" {
		t.Errorf("refresh-code did not produce a new code: %q", got.AccessCode)
	}
}

// createRaw inserts a file directly with a controlled slug/access_code/visibility,
// which the API's Create (random slug, private) can't express.
func (e *testEnv) createRaw(t *testing.T, slug, code, html string, public bool) {
	t.Helper()
	if _, err := e.queries.CreateFile(context.Background(), sqlcgen.CreateFileParams{
		Slug:        slug,
		Name:        slug,
		HtmlContent: html,
		Kind:        "html",
		IsPublic:    public,
		AccessCode:  code,
		UserID:      e.admin.ID,
	}); err != nil {
		t.Fatalf("CreateFile(raw): %v", err)
	}
}

// createRawKind is createRaw with an explicit kind, for exercising the
// non-html render paths.
func (e *testEnv) createRawKind(t *testing.T, slug, kind, source string) {
	t.Helper()
	if _, err := e.queries.CreateFile(context.Background(), sqlcgen.CreateFileParams{
		Slug:        slug,
		Name:        slug,
		HtmlContent: source,
		Kind:        kind,
		IsPublic:    true,
		AccessCode:  "c",
		UserID:      e.admin.ID,
	}); err != nil {
		t.Fatalf("CreateFile(raw %s): %v", kind, err)
	}
}

func TestRenderByKind(t *testing.T) {
	e := newEnv(t)

	// Markdown is rendered to HTML.
	e.createRawKind(t, "md", "markdown", "# Title\n\nsome **bold** text\n")
	resp := e.do(t, http.MethodGet, "/res/md?code=c", "", nil)
	assertStatus(t, resp, http.StatusOK)
	if b := bodyString(t, resp); !strings.Contains(b, "Title</h1>") || !strings.Contains(b, "<strong>bold</strong>") {
		t.Errorf("markdown not rendered to HTML: %q", b)
	}

	// Text is HTML-escaped and its newlines preserved (never interpreted).
	e.createRawKind(t, "note", "txt", "a <b> tag\nsecond line")
	resp = e.do(t, http.MethodGet, "/res/note?code=c", "", nil)
	assertStatus(t, resp, http.StatusOK)
	b := bodyString(t, resp)
	if strings.Contains(b, "<b> tag") {
		t.Errorf("txt content should be escaped, not interpreted: %q", b)
	}
	if !strings.Contains(b, "a &lt;b&gt; tag\nsecond line") {
		t.Errorf("txt content not preserved/escaped: %q", b)
	}
}

func TestRenderAccessControl(t *testing.T) {
	e := newEnv(t)

	// Missing slug -> 404.
	resp := e.do(t, http.MethodGet, "/res/nope", "", nil)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()

	// Private file, no session -> 403.
	e.createRaw(t, "priv", "code1", "<h1>secret</h1>", false)
	resp = e.do(t, http.MethodGet, "/res/priv", "", nil)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

	// Public file, wrong code -> 403.
	e.createRaw(t, "pub", "rightcode", "<h1>hello</h1>", true)
	resp = e.do(t, http.MethodGet, "/res/pub?code=wrong", "", nil)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

	// Public file, correct code -> 200 + content, and code_success_count
	// bumps (anonymous access), not success_count (session access).
	resp = e.do(t, http.MethodGet, "/res/pub?code=rightcode", "", nil)
	assertStatus(t, resp, http.StatusOK)
	if b := bodyString(t, resp); b != "<h1>hello</h1>" {
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
	assertStatus(t, resp, http.StatusOK)
	if b := bodyString(t, resp); b != "<h1>secret</h1>" {
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
	resp := e.do(t, http.MethodGet, "/res/gone", "", cookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
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
	resp := e.do(t, http.MethodGet, "/res/limited?code=c", "", nil)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Second anonymous access is over quota -> 403, and the file flips private.
	resp = e.do(t, http.MethodGet, "/res/limited?code=c", "", nil)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

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
	resp = e.do(t, http.MethodGet, "/api/files/limited", "", e.authCookie(t))
	assertStatus(t, resp, http.StatusOK)
	if got := decodeFile(t, resp); got.ExpiredReason != "views" || got.ExpiredAt == nil {
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

	resp := e.do(t, http.MethodGet, "/res/stale?code=c", "", nil)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

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
	resp = e.do(t, http.MethodPatch, "/api/files/stale/expiry", `{"ttl":"24h"}`, e.authCookie(t))
	assertStatus(t, resp, http.StatusOK)
	if got := decodeFile(t, resp); got.ExpiredReason != "" || got.ExpiredAt != nil {
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
		resp := e.do(t, http.MethodGet, "/res/quota", "", cookie)
		assertStatus(t, resp, http.StatusOK)
		resp.Body.Close()
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
	resp := e.do(t, http.MethodGet, "/api/files/"+f.Slug+"/download", "", nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()

	// Authenticated -> 200 with attachment disposition + raw content.
	resp = e.do(t, http.MethodGet, "/api/files/"+f.Slug+"/download", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if b := bodyString(t, resp); b != "<h1>dl</h1>" {
		t.Errorf("download body = %q", b)
	}

	// Missing slug -> 404.
	resp = e.do(t, http.MethodGet, "/api/files/nope/download", "", cookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

func TestBackupDownload(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	// Put some data in the DB so the snapshot isn't trivially empty.
	e.createViaAPI(t, cookie, "doc", "<h1>backed up</h1>")

	// Unauthenticated -> 401 (requireAuth middleware).
	resp := e.do(t, http.MethodGet, "/api/backup", "", nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()

	// Authenticated -> 200, attachment, and a valid SQLite file.
	resp = e.do(t, http.MethodGet, "/api/backup", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	// A signed-in non-admin -> 403. The snapshot is the whole database, so
	// it carries every user's files, password hash and API key.
	_, otherCookie := e.newUser(t, "other")
	other := e.do(t, http.MethodGet, "/api/backup", "", otherCookie)
	assertStatus(t, other, http.StatusForbidden)
	other.Body.Close()

	body := bodyString(t, resp)
	// Every SQLite database file begins with this 16-byte magic header.
	if !strings.HasPrefix(body, "SQLite format 3\x00") {
		t.Errorf("backup body does not look like a SQLite database (first bytes: %q)", body[:min(16, len(body))])
	}
}

// TestBackupRestoreRoundTrip is the whole point of the pair: download a
// snapshot, change everything, upload the snapshot back, and find the original
// state — through the same live server, with no restart in between.
func TestBackupRestoreRoundTrip(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	original := e.createViaAPI(t, cookie, "before backup", "<h1>original</h1>")
	snapshot := e.download(t, "/api/backup", cookie)

	// Diverge: add a file, delete the original one.
	added := e.createViaAPI(t, cookie, "after backup", "<p>new</p>")
	resp := e.do(t, http.MethodDelete, "/api/files/"+original.Slug, "", cookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	restored := e.restore(t, snapshot, cookie)
	assertStatus(t, restored, http.StatusOK)
	var out struct {
		Users int64 `json:"users"`
		Files int64 `json:"files"`
	}
	if err := json.NewDecoder(restored.Body).Decode(&out); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	restored.Body.Close()
	if out.Users != 1 || out.Files != 1 {
		t.Errorf("restore reported %+v, want 1 user / 1 file", out)
	}

	// The same running server now serves the snapshot's data: the original file
	// is back and the one added afterwards is gone. A fresh session is needed
	// because the snapshot's sessions table replaced the live one.
	after := e.cookieFor(t, e.admin.ID)
	resp = e.do(t, http.MethodGet, "/api/files/"+original.Slug, "", after)
	assertStatus(t, resp, http.StatusOK)
	if got := decodeFile(t, resp); got.HTMLContent != "<h1>original</h1>" {
		t.Errorf("restored content = %q", got.HTMLContent)
	}
	resp = e.do(t, http.MethodGet, "/api/files/"+added.Slug, "", after)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()

	// And the restored database keeps working for writes — the id sequence came
	// across with the rows, so a new file doesn't collide with a restored id.
	e.createViaAPI(t, after, "after restore", "<p>fresh</p>")
}

// download returns a response body as bytes, for endpoints that stream files.
func (e *testEnv) download(t *testing.T, path string, cookie *http.Cookie) []byte {
	t.Helper()
	resp := e.do(t, http.MethodGet, path, "", cookie)
	assertStatus(t, resp, http.StatusOK)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return body
}

// restore POSTs a raw database file to the restore endpoint. The body is the
// file itself, with no multipart wrapper.
func (e *testEnv) restore(t *testing.T, snapshot []byte, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/api/backup/restore", bytes.NewReader(snapshot))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /api/backup/restore: %v", err)
	}
	return resp
}

func TestRestoreRejections(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	kept := e.createViaAPI(t, cookie, "keep me", "<p>safe</p>")
	snapshot := e.download(t, "/api/backup", cookie)

	// A signed-in non-admin: this writes over everyone's data at once.
	_, otherCookie := e.newUser(t, "other")
	resp := e.restore(t, snapshot, otherCookie)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

	resp = e.restore(t, snapshot, nil)
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
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := e.restore(t, c.body, cookie)
			assertStatus(t, resp, c.want)
			resp.Body.Close()
		})
	}

	// Every rejection above must have left the live data untouched — that is
	// what makes it safe to report the problem instead of a lost database.
	resp = e.do(t, http.MethodGet, "/api/files/"+kept.Slug, "", cookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

func TestSPAFallback(t *testing.T) {
	e := newEnv(t)
	resp := e.do(t, http.MethodGet, "/", "", nil)
	assertStatus(t, resp, http.StatusOK)
	if b := bodyString(t, resp); !strings.Contains(strings.ToLower(b), "<!doctype html") {
		t.Errorf("SPA fallback did not serve index.html: %q", b)
	}
}

func sqlNullInt64(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: true}
}

// cookieFrom extracts the session cookie set by a login/register/setup response.
func cookieFrom(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return &http.Cookie{Name: c.Name, Value: c.Value}
		}
	}
	t.Fatal("response did not set a session cookie")
	return nil
}

func TestSetupFlow(t *testing.T) {
	e := newBareEnv(t)

	// Empty database → needs setup.
	resp := e.do(t, http.MethodGet, "/api/setup/status", "", nil)
	assertStatus(t, resp, http.StatusOK)
	if b := bodyString(t, resp); !strings.Contains(b, `"needs_setup":true`) {
		t.Errorf("status body = %q, want needs_setup true", b)
	}

	// Registration is blocked before setup.
	resp = e.do(t, http.MethodPost, "/api/auth/register",
		`{"username":"u","nickname":"n","password":"secret1"}`, nil)
	assertStatus(t, resp, http.StatusConflict)
	resp.Body.Close()

	// Setup creates the super admin, writes configs, and logs in.
	resp = e.do(t, http.MethodPost, "/api/setup",
		`{"username":"boss","nickname":"Boss","password":"secret1","allow_registration":true,"mcp_enabled":false}`, nil)
	assertStatus(t, resp, http.StatusCreated)
	cookie := cookieFrom(t, resp)
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/api/auth/me", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	if b := bodyString(t, resp); !strings.Contains(b, `"is_admin":true`) {
		t.Errorf("me body = %q, want is_admin true", b)
	}

	// Second setup attempt is rejected.
	resp = e.do(t, http.MethodPost, "/api/setup",
		`{"username":"evil","nickname":"E","password":"secret1"}`, nil)
	assertStatus(t, resp, http.StatusConflict)
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/api/setup/status", "", nil)
	if b := bodyString(t, resp); !strings.Contains(b, `"needs_setup":false`) || !strings.Contains(b, `"allow_registration":true`) {
		t.Errorf("status body after setup = %q", b)
	}
}

func TestSetupStatusExposesRuntimeUploadAndShareConfig(t *testing.T) {
	cfg := config.Default()
	cfg.MaxFileSizeMB = 20
	cfg.MaxFileSizeBytes = 20 << 20
	cfg.PublicShareBaseURL = "https://share.example.com"
	e := newBareEnvWithConfig(t, cfg)

	resp := e.do(t, http.MethodGet, "/api/setup/status", "", nil)
	assertStatus(t, resp, http.StatusOK)
	var status struct {
		MaxFileSizeBytes   int64  `json:"max_file_size_bytes"`
		PublicShareBaseURL string `json:"public_share_base_url"`
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode setup status: %v", err)
	}
	if status.MaxFileSizeBytes != 20<<20 {
		t.Errorf("max_file_size_bytes = %d, want %d", status.MaxFileSizeBytes, 20<<20)
	}
	if status.PublicShareBaseURL != "https://share.example.com" {
		t.Errorf("public_share_base_url = %q", status.PublicShareBaseURL)
	}
}

func TestRegisterRespectsConfig(t *testing.T) {
	e := newEnv(t) // super admin exists, no configs → registration disabled
	resp := e.do(t, http.MethodPost, "/api/auth/register",
		`{"username":"u2","nickname":"N","password":"secret1"}`, nil)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

	// Admin enables registration.
	cookie := e.authCookie(t)
	resp = e.do(t, http.MethodPut, "/api/settings", `{"allow_registration":true}`, cookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Now registration works and logs the new user in as a non-admin.
	resp = e.do(t, http.MethodPost, "/api/auth/register",
		`{"username":"u2","nickname":"N","password":"secret1"}`, nil)
	assertStatus(t, resp, http.StatusCreated)
	userCookie := cookieFrom(t, resp)
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/api/auth/me", "", userCookie)
	assertStatus(t, resp, http.StatusOK)
	if b := bodyString(t, resp); !strings.Contains(b, `"is_admin":false`) {
		t.Errorf("me body = %q, want is_admin false", b)
	}

	// Duplicate username → 409; weak password → 400.
	resp = e.do(t, http.MethodPost, "/api/auth/register",
		`{"username":"u2","nickname":"N","password":"secret1"}`, nil)
	assertStatus(t, resp, http.StatusConflict)
	resp.Body.Close()
	resp = e.do(t, http.MethodPost, "/api/auth/register",
		`{"username":"u3","nickname":"N","password":"short"}`, nil)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	// Non-admin users cannot change settings.
	resp = e.do(t, http.MethodPut, "/api/settings", `{"allow_registration":false}`, userCookie)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()
}

func TestUpdateProfile(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// Nickname change.
	resp := e.do(t, http.MethodPatch, "/api/user", `{"nickname":"New Name"}`, cookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
	resp = e.do(t, http.MethodGet, "/api/auth/me", "", cookie)
	if b := bodyString(t, resp); !strings.Contains(b, `"nickname":"New Name"`) {
		t.Errorf("me body = %q, want updated nickname", b)
	}

	// Password change requires the current password.
	resp = e.do(t, http.MethodPatch, "/api/user",
		`{"current_password":"wrong","new_password":"secret2"}`, cookie)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()
	resp = e.do(t, http.MethodPatch, "/api/user",
		`{"current_password":"`+testPass+`","new_password":"secret2"}`, cookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	// Old password no longer logs in; the new one does.
	resp = e.do(t, http.MethodPost, "/api/auth/login",
		`{"username":"`+testUser+`","password":"`+testPass+`"}`, nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
	resp = e.do(t, http.MethodPost, "/api/auth/login",
		`{"username":"`+testUser+`","password":"secret2"}`, nil)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
}

func TestAPIKeyLifecycle(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	// MCP disabled → no key issued.
	resp := e.do(t, http.MethodPost, "/api/user/api-key", "", cookie)
	assertStatus(t, resp, http.StatusConflict)
	resp.Body.Close()

	resp = e.do(t, http.MethodPut, "/api/settings", `{"mcp_enabled":true}`, cookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	keyOf := func(resp *http.Response) string {
		t.Helper()
		defer resp.Body.Close()
		var body struct {
			APIKey string `json:"api_key"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode api key: %v", err)
		}
		return body.APIKey
	}

	// First ensure creates a key; a second ensure returns the same key.
	resp = e.do(t, http.MethodPost, "/api/user/api-key", "", cookie)
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
	MatchedContent bool   `json:"matched_content"`
	Snippet        string `json:"snippet"`
}

func (e *testEnv) search(t *testing.T, cookie *http.Cookie, query string) []searchResp {
	t.Helper()
	resp := e.do(t, http.MethodGet, "/api/search?"+query, "", cookie)
	assertStatus(t, resp, http.StatusOK)
	defer resp.Body.Close()
	var out []searchResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	return out
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
	resp := e.do(t, http.MethodGet, "/api/search?q=hello", "", nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
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
		resp := e.do(t, http.MethodDelete, "/api/files/"+c.slug, "", c.cookie)
		assertStatus(t, resp, http.StatusNoContent)
		resp.Body.Close()
	}

	resp := e.do(t, http.MethodDelete, "/api/trash", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode empty-trash response: %v", err)
	}
	resp.Body.Close()
	if out.Deleted != 1 {
		t.Errorf("deleted = %d, want 1", out.Deleted)
	}

	// The live file is untouched -- this endpoint is the only bulk delete in
	// the API, so "it took only the trash" is the property that matters.
	resp = e.do(t, http.MethodGet, "/api/files/"+live.Slug, "", cookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	// And it is owner-scoped: the other user still has their own trash.
	resp = e.do(t, http.MethodPost, "/api/files/"+theirs.Slug+"/restore", "", otherCookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Emptying an already-empty trash succeeds with 0 rather than 404.
	resp = e.do(t, http.MethodDelete, "/api/trash", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = e.do(t, http.MethodDelete, "/api/trash", "", nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
}

// rawSessionCookie returns the Set-Cookie exactly as sent, attributes included.
// cookieFrom deliberately keeps only name and value, since its result is meant
// to be attached to a request; these assertions are about the attributes.
func rawSessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatal("response did not set a session cookie")
	return nil
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
	if c := rawSessionCookie(t, resp); c.Secure {
		t.Error("cookie must not be Secure over plain HTTP, or the browser drops it and login appears to fail")
	}
	resp.Body.Close()

	// A terminating proxy is the third deployment: the backend sees HTTP, the
	// browser saw HTTPS, and only the forwarded header says so.
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/api/auth/login", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	resp, err = e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("login through proxy: %v", err)
	}
	assertStatus(t, resp, http.StatusNoContent)
	if c := rawSessionCookie(t, resp); !c.Secure {
		t.Error("cookie must be Secure when the browser reached us over HTTPS")
	}
	resp.Body.Close()

	// Logout mirrors it, so the deletion targets the cookie that was set.
	resp = e.do(t, http.MethodPost, "/api/auth/logout", "", nil)
	assertStatus(t, resp, http.StatusNoContent)
	if c := rawSessionCookie(t, resp); c.Secure {
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
	resp := e.do(t, http.MethodGet, "/api/admin/users", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	defer resp.Body.Close()
	var out []adminUserResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode users: %v", err)
	}
	return out
}

func TestAdminListsUsersWithFileCounts(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	user, userCookie := e.newUser(t, "bob")

	e.createViaAPI(t, cookie, "admin file", "<p>1</p>")
	trashed := e.createViaAPI(t, userCookie, "bob file", "<p>2</p>")
	e.createViaAPI(t, userCookie, "bob live", "<p>3</p>")
	resp := e.do(t, http.MethodDelete, "/api/files/"+trashed.Slug, "", userCookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

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

	// The page is for the super admin only; an ordinary account gets 403 (the
	// path exists, only the privilege is in question), not 404.
	resp = e.do(t, http.MethodGet, "/api/admin/users", "", userCookie)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/admin/users/1/status", `{"disabled":true}`},
		{http.MethodPost, fmt.Sprintf("/api/admin/users/%d/password", user.ID), `{"new_password":"hijacked"}`},
	} {
		resp = e.do(t, c.method, c.path, c.body, userCookie)
		assertStatus(t, resp, http.StatusForbidden)
		resp.Body.Close()
	}
	// ...and to be sure the 403 was the privilege check rather than a bad
	// payload, the password above must not have been applied.
	if !auth.VerifyPassword(e.currentHash(t, user.ID), "secret2") {
		t.Error("a rejected admin request changed the target's password anyway")
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
	resp := e.do(t, http.MethodPatch, "/api/files/"+file.Slug+"/visibility", `{"is_public":true}`, userCookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	if err := e.queries.SetConfig(context.Background(), sqlcgen.SetConfigParams{
		Key: "mcp_enabled", Value: "true",
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	bobKey, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := e.queries.SetUserAPIKey(context.Background(), sqlcgen.SetUserAPIKeyParams{
		ApiKey: sql.NullString{String: bobKey, Valid: true}, ID: user.ID,
	}); err != nil {
		t.Fatalf("SetUserAPIKey: %v", err)
	}

	publicURL := "/res/" + file.Slug + "?code=" + file.AccessCode
	resp = e.do(t, http.MethodGet, publicURL, "", nil)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Suspend.
	resp = e.do(t, http.MethodPatch, fmt.Sprintf("/api/admin/users/%d/status", user.ID), `{"disabled":true}`, adminCookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	if users := e.adminUsers(t, adminCookie); !users[1].Disabled {
		t.Error("list should report the account as disabled")
	}
	// The password still verifies, so this 403 is about the account's state.
	resp = e.do(t, http.MethodPost, "/api/auth/login", `{"username":"bob","password":"secret2"}`, nil)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()
	// An existing session stops working on its very next request.
	resp = e.do(t, http.MethodGet, "/api/files", "", userCookie)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
	// The MCP key dies with it, or suspension would leave the agent door open.
	resp = e.mcpCall(t, bobKey, "list_files", map[string]any{})
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
	// And the account's public links read as nonexistent, not merely forbidden.
	resp = e.do(t, http.MethodGet, publicURL, "", nil)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
	// The super admin's own files are unaffected by someone else's suspension.
	adminFile := e.createViaAPI(t, adminCookie, "admin file", "<p>admin</p>")
	resp = e.do(t, http.MethodGet, "/res/"+adminFile.Slug, "", adminCookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Restore: everything comes back, including the link.
	resp = e.do(t, http.MethodPatch, fmt.Sprintf("/api/admin/users/%d/status", user.ID), `{"disabled":false}`, adminCookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
	resp = e.do(t, http.MethodPost, "/api/auth/login", `{"username":"bob","password":"secret2"}`, nil)
	assertStatus(t, resp, http.StatusNoContent)
	restored := cookieFrom(t, resp)
	resp.Body.Close()
	resp = e.do(t, http.MethodGet, "/api/files", "", restored)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	resp = e.do(t, http.MethodGet, publicURL, "", nil)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
}

// The super admin is the only account that can lift a suspension, so letting it
// suspend itself would be a one-way door out of the app.
func TestAdminCannotDisableSuperAdmin(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	resp := e.do(t, http.MethodPatch, "/api/admin/users/1/status", `{"disabled":true}`, cookie)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/api/auth/me", "", cookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// An unknown id is a missing object (404); a non-numeric one could never
	// name a row and is a malformed request (400).
	resp = e.do(t, http.MethodPatch, "/api/admin/users/9999/status", `{"disabled":true}`, cookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
	resp = e.do(t, http.MethodPatch, "/api/admin/users/abc/status", `{"disabled":true}`, cookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()
}

func TestAdminResetPassword(t *testing.T) {
	e := newEnv(t)
	adminCookie := e.authCookie(t)
	user, userCookie := e.newUser(t, "bob")
	path := fmt.Sprintf("/api/admin/users/%d/password", user.ID)

	// Too short is rejected before anything is written.
	resp := e.do(t, http.MethodPost, path, `{"new_password":"abc"}`, adminCookie)
	assertStatus(t, resp, http.StatusBadRequest)
	resp.Body.Close()

	resp = e.do(t, http.MethodPost, path, `{"new_password":"brand-new-pass"}`, adminCookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

	// The new password works and the old one doesn't -- no current password
	// was needed, which is the point: there is no self-service recovery.
	resp = e.do(t, http.MethodPost, "/api/auth/login", `{"username":"bob","password":"brand-new-pass"}`, nil)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()
	resp = e.do(t, http.MethodPost, "/api/auth/login", `{"username":"bob","password":"secret2"}`, nil)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()

	// Unlike a self-service change, an admin reset ends the target's existing
	// sessions: it is what you reach for when an account is compromised.
	resp = e.do(t, http.MethodGet, "/api/files", "", userCookie)
	assertStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()

	resp = e.do(t, http.MethodPost, "/api/admin/users/9999/password", `{"new_password":"whatever1"}`, adminCookie)
	assertStatus(t, resp, http.StatusNotFound)
	resp.Body.Close()
}

// --- MCP endpoint ---

// enableMCP turns the mcp_enabled config on and issues the admin an API key.
func (e *testEnv) enableMCP(t *testing.T) string {
	t.Helper()
	if err := e.queries.SetConfig(context.Background(), sqlcgen.SetConfigParams{Key: "mcp_enabled", Value: "true"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if err := e.queries.SetUserAPIKey(context.Background(), sqlcgen.SetUserAPIKeyParams{
		ApiKey: sql.NullString{String: key, Valid: true}, ID: e.admin.ID,
	}); err != nil {
		t.Fatalf("SetUserAPIKey: %v", err)
	}
	return key
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
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	return resp
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

// mcpUpload uploads one file over MCP and returns its structured output.
func (e *testEnv) mcpUpload(t *testing.T, key, name, kind, content string) map[string]any {
	t.Helper()
	res := decodeMCPResult(t, e.mcpCall(t, key, "upload_file", map[string]any{
		"name": name, "kind": kind, "content": content,
	}))
	if res.IsError {
		t.Fatalf("upload_file error: %s", res.text())
	}
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
	res := decodeMCPResult(t, e.mcpCall(t, key, "search_files", map[string]any{"query": "x"}))
	if res.IsError {
		t.Errorf("valid key got tool error: %s", res.text())
	}
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
	resp := e.do(t, http.MethodGet, e.pathOf(t, url), "", nil)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

	// publish_file → same URL becomes anonymously viewable, rendered as HTML.
	res := decodeMCPResult(t, e.mcpCall(t, key, "publish_file", map[string]any{"slug": slug}))
	if res.IsError {
		t.Fatalf("publish_file error: %s", res.text())
	}
	resp = e.do(t, http.MethodGet, e.pathOf(t, url), "", nil)
	assertStatus(t, resp, http.StatusOK)
	if b := bodyString(t, resp); !strings.Contains(b, "<h1") || !strings.Contains(b, "Hello MCP") {
		t.Errorf("published markdown did not render: %q", b)
	}
}

func TestMCPPublishUsesConfiguredPublicShareOrigin(t *testing.T) {
	cfg := config.Default()
	cfg.PublicShareBaseURL = "https://share.example.com"
	e := newEnvWithConfig(t, cfg)
	key := e.enableMCP(t)

	uploaded := e.mcpUpload(t, key, "shared", "markdown", "# shared")
	slug, _ := uploaded["slug"].(string)
	res := decodeMCPResult(t, e.mcpCall(t, key, "publish_file", map[string]any{"slug": slug}))
	if res.IsError {
		t.Fatalf("publish_file error: %s", res.text())
	}
	url, _ := res.StructuredContent["url"].(string)
	want := "https://share.example.com/res/" + slug
	if !strings.HasPrefix(url, want+"?code=") {
		t.Errorf("publish_file URL = %q, want prefix %q", url, want+"?code=")
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
	res := decodeMCPResult(t, e.mcpCall(t, key, "publish_file", map[string]any{
		"slug": viewsSlug, "max_views": 2,
	}))
	if res.IsError {
		t.Fatalf("publish_file with max_views: %s", res.text())
	}
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
	res = decodeMCPResult(t, e.mcpCall(t, key, "publish_file", map[string]any{
		"slug": ttlSlug, "ttl": "7d",
	}))
	if res.IsError {
		t.Fatalf("publish_file with ttl: %s", res.text())
	}
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
		if res = decodeMCPResult(t, e.mcpCall(t, key, "publish_file", args)); !res.IsError {
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
	if res := decodeMCPResult(t, e.mcpCall(t, key, "publish_file", map[string]any{"slug": slug})); res.IsError {
		t.Fatalf("publish_file: %s", res.text())
	}
	resp := e.do(t, http.MethodGet, e.pathOf(t, url), "", nil)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	res := decodeMCPResult(t, e.mcpCall(t, key, "unpublish_file", map[string]any{"slug": slug}))
	if res.IsError {
		t.Fatalf("unpublish_file: %s", res.text())
	}
	if isPublic, _ := res.StructuredContent["is_public"].(bool); isPublic {
		t.Error("unpublish_file should report the file as private")
	}
	resp = e.do(t, http.MethodGet, e.pathOf(t, url), "", nil)
	assertStatus(t, resp, http.StatusForbidden)
	resp.Body.Close()

	// The document itself survives -- unpublishing is not deleting.
	resp = e.do(t, http.MethodGet, "/api/files/"+slug, "", e.authCookie(t))
	assertStatus(t, resp, http.StatusOK)
	if got := decodeFile(t, resp); got.HTMLContent != "# hi" {
		t.Errorf("content = %q, want it untouched", got.HTMLContent)
	}

	if res = decodeMCPResult(t, e.mcpCall(t, key, "unpublish_file", map[string]any{"slug": "nope"})); !res.IsError {
		t.Error("unpublish_file on an unknown slug should be a tool error")
	}
}

func TestMCPListFiles(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	// Empty project: a plain answer, not an error.
	res := decodeMCPResult(t, e.mcpCall(t, key, "list_files", map[string]any{}))
	if res.IsError || !strings.Contains(res.text(), "no documents") {
		t.Errorf("list_files on an empty project = %v %q", res.IsError, res.text())
	}

	e.mcpUpload(t, key, "first", "markdown", "# one")
	second := e.mcpUpload(t, key, "second", "html", "<p>two</p>")
	secondSlug, _ := second["slug"].(string)
	if res = decodeMCPResult(t, e.mcpCall(t, key, "publish_file", map[string]any{
		"slug": secondSlug, "max_views": 3,
	})); res.IsError {
		t.Fatalf("publish_file: %s", res.text())
	}

	// Another user's files must not appear: list_files has no query to scope it,
	// so the owner filter in ListUserFiles is the only thing standing there.
	_, otherCookie := e.newUser(t, "other")
	e.createViaAPI(t, otherCookie, "not mine", "<p>hidden</p>")

	res = decodeMCPResult(t, e.mcpCall(t, key, "list_files", map[string]any{}))
	if res.IsError {
		t.Fatalf("list_files: %s", res.text())
	}
	if total, _ := res.StructuredContent["total"].(float64); total != 2 {
		t.Errorf("total = %v, want 2 (only the key owner's files)", res.StructuredContent["total"])
	}
	text := res.text()
	if strings.Contains(text, "not mine") {
		t.Error("list_files leaked another user's file")
	}
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
	res := decodeMCPResult(t, e.mcpCall(t, key, "upload_file", map[string]any{
		"name": "x", "kind": "txt", "content": "plain",
	}))
	if !res.IsError || !strings.Contains(res.text(), "markdown or html") {
		t.Errorf("txt upload = %v %q, want kind error", res.IsError, res.text())
	}

	res = decodeMCPResult(t, e.mcpCall(t, key, "upload_file", map[string]any{
		"name": "x", "kind": "html", "content": "",
	}))
	if !res.IsError || !strings.Contains(res.text(), "content is required") {
		t.Errorf("empty content = %v %q, want content error", res.IsError, res.text())
	}

	res = decodeMCPResult(t, e.mcpCall(t, key, "upload_file", map[string]any{
		"name": "too-big", "kind": "html", "content": strings.Repeat("x", (5<<20)+1),
	}))
	if !res.IsError || !strings.Contains(res.text(), "5MB") {
		t.Errorf("oversized content = %v %q, want default 5MB error", res.IsError, res.text())
	}
}

func TestConfiguredFileSizeLimitAllowsNineteenMiBMCPUpload(t *testing.T) {
	cfg := config.Default()
	cfg.MaxFileSizeMB = 20
	cfg.MaxFileSizeBytes = 20 << 20
	e := newEnvWithConfig(t, cfg)
	key := e.enableMCP(t)

	res := decodeMCPResult(t, e.mcpCall(t, key, "upload_file", map[string]any{
		"name": "nineteen-mib", "kind": "html", "content": strings.Repeat("a", 19<<20),
	}))
	if res.IsError {
		t.Fatalf("upload_file error: %s", res.text())
	}
	slug, _ := res.StructuredContent["slug"].(string)
	res = decodeMCPResult(t, e.mcpCall(t, key, "update_file", map[string]any{
		"slug": slug, "content": strings.Repeat("b", 19<<20),
	}))
	if res.IsError {
		t.Fatalf("update_file error: %s", res.text())
	}

	res = decodeMCPResult(t, e.mcpCall(t, key, "upload_files", map[string]any{
		"files": []map[string]any{
			{"name": "ten-mib", "kind": "html", "content": strings.Repeat("c", 10<<20)},
			{"name": "over-twenty-mib", "kind": "html", "content": strings.Repeat("d", (20<<20)+1)},
		},
	}))
	if res.IsError {
		t.Fatalf("upload_files error: %s", res.text())
	}
	if got := res.StructuredContent["uploaded"].(float64); got != 1 {
		t.Errorf("upload_files uploaded = %v, want 1", got)
	}
	if got := res.StructuredContent["failed"].(float64); got != 1 {
		t.Errorf("upload_files failed = %v, want 1", got)
	}
	if !strings.Contains(res.text(), "20MB") {
		t.Errorf("upload_files result = %q, want configured 20MB error", res.text())
	}
}

func TestConfiguredFileSizeLimitAllowsEscapedMCPUpload(t *testing.T) {
	cfg := config.Default()
	cfg.MaxFileSizeMB = 20
	cfg.MaxFileSizeBytes = 20 << 20
	e := newEnvWithConfig(t, cfg)
	key := e.enableMCP(t)

	// encoding/json expands each control byte to a six-byte \u00XX escape.
	// Seven MiB therefore exceeds the old ~40 MiB request cap even though the
	// decoded file remains well below the configured 20 MiB per-file limit.
	res := decodeMCPResult(t, e.mcpCall(t, key, "upload_file", map[string]any{
		"name": "escaped", "kind": "html", "content": strings.Repeat("\x01", 7<<20),
	}))
	if res.IsError {
		t.Fatalf("upload_file with escaped content error: %s", res.text())
	}
}

func TestMCPBatchUpload(t *testing.T) {
	e := newEnv(t)
	key := e.enableMCP(t)

	res := decodeMCPResult(t, e.mcpCall(t, key, "upload_files", map[string]any{
		"files": []map[string]any{
			{"name": "a", "kind": "markdown", "content": "# a"},
			{"name": "b", "kind": "html", "content": "<p>b</p>"},
			{"name": "bad", "kind": "pdf", "content": "x"},
		},
	}))
	if res.IsError {
		t.Fatalf("upload_files error: %s", res.text())
	}
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

	res := decodeMCPResult(t, e.mcpCall(t, key, "search_files", map[string]any{"query": "部署"}))
	if res.IsError {
		t.Fatalf("search_files error: %s", res.text())
	}
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
	res = decodeMCPResult(t, e.mcpCall(t, key, "search_files", map[string]any{"query": "不存在的词"}))
	if res.IsError {
		t.Fatalf("search miss errored: %s", res.text())
	}
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
	res := decodeMCPResult(t, e.mcpCall(t, key, "update_file", map[string]any{"slug": slug}))
	if !res.IsError {
		t.Error("update with nothing to change should error")
	}

	res = decodeMCPResult(t, e.mcpCall(t, key, "update_file", map[string]any{
		"slug": slug, "name": "新标题", "content": "# v2",
	}))
	if res.IsError {
		t.Fatalf("update_file error: %s", res.text())
	}

	after, err := e.queries.GetFileBySlugAnyOwner(context.Background(), slug)
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner after update: %v", err)
	}
	if after.Name != "新标题" || after.HtmlContent != "# v2" {
		t.Errorf("update result = %q %q", after.Name, after.HtmlContent)
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
	res := decodeMCPResult(t, e.mcpCall(t, key, "delete_file", map[string]any{"slug": slug}))
	if res.IsError {
		t.Fatalf("delete phase 1 errored: %s", res.text())
	}
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
	res = decodeMCPResult(t, e.mcpCall(t, key, "delete_file", map[string]any{"slug": slug, "confirm": true}))
	if res.IsError {
		t.Fatalf("delete phase 2 errored: %s", res.text())
	}
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
		Slug: "their-doc", Name: "机密文档", HtmlContent: "<p>secret</p>", Kind: "html",
		AccessCode: "code1234", UserID: other.ID,
	})
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	// search does not surface it…
	res := decodeMCPResult(t, e.mcpCall(t, key, "search_files", map[string]any{"query": "机密"}))
	if found := res.StructuredContent["found"].(bool); found {
		t.Error("search must not return another user's files")
	}
	// …list does not either…
	res = decodeMCPResult(t, e.mcpCall(t, key, "list_files", map[string]any{}))
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
		res := decodeMCPResult(t, e.mcpCall(t, key, call.tool, call.args))
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

	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	defer resp.Body.Close()
	assertStatus(t, resp, http.StatusOK)

	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}

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
}

// fingerprint is every column a foreignCase could plausibly mutate, so the
// "nothing changed" assertion catches an endpoint that 404s and writes anyway.
func (e *testEnv) fingerprint(t *testing.T, slug string) string {
	t.Helper()
	f, err := e.queries.GetFileBySlugAnyOwner(context.Background(), slug)
	if err != nil {
		t.Fatalf("fingerprint %q: %v", slug, err)
	}
	return fmt.Sprintf("name=%q content=%q public=%v code=%q tags=%q expires=%v maxviews=%v",
		f.Name, f.HtmlContent, f.IsPublic, f.AccessCode, f.Tags, f.ExpiresAt.Valid, f.MaxViews.Valid)
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
		// The trash listing deliberately doesn't carry html_content (no query
		// reachable from a listing does), so read it directly -- the point of
		// this fingerprint is that the content is untouched.
		var content string
		if err := e.conn.QueryRow(
			`SELECT html_content FROM files WHERE slug = ? AND user_id = ?`,
			slug, ownerID).Scan(&content); err != nil {
			t.Fatalf("read trashed content: %v", err)
		}
		return fmt.Sprintf("name=%q content=%q public=%v code=%q tags=%q",
			f.Name, content, f.IsPublic, f.AccessCode, f.Tags)
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
			resp := e.do(t, http.MethodDelete, "/api/files/"+trashed.Slug, "", ownerCookie)
			assertStatus(t, resp, http.StatusNoContent)
			resp.Body.Close()

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
		route == "/api/search" || route == "/api/trash"
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
		resp := e.do(t, http.MethodGet, path, "", cookie)
		assertStatus(t, resp, http.StatusOK)
		defer resp.Body.Close()
		var out []fileResp
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return out
	}

	if got := list(adminCookie, "/api/files"); len(got) != 2 {
		t.Errorf("admin sees %d files, want only their own 2", len(got))
	}
	other := list(otherCookie, "/api/files")
	if len(other) != 1 || other[0].Slug != otherFile.Slug {
		t.Errorf("other user sees %+v, want only their own file", other)
	}

	// Trash is scoped too.
	resp := e.do(t, http.MethodDelete, "/api/files/"+adminTrash.Slug, "", adminCookie)
	assertStatus(t, resp, http.StatusNoContent)
	resp.Body.Close()

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
	assertStatus(t, resp, http.StatusForbidden)
	if b := bodyString(t, resp); strings.Contains(b, "secret") {
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
	resp = e.do(t, http.MethodGet, "/res/"+file.Slug, "", adminCookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Once public, the stranger's correct code works and counts as a
	// code-based view rather than an owner view.
	resp = e.do(t, http.MethodPatch, "/api/files/"+file.Slug+"/visibility", `{"is_public":true}`, adminCookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = e.do(t, http.MethodGet, "/res/"+file.Slug+"?code="+file.AccessCode, "", otherCookie)
	assertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

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
	if err := e.queries.SetConfig(context.Background(), sqlcgen.SetConfigParams{
		Key: "mcp_enabled", Value: "true",
	}); err != nil {
		t.Fatalf("SetConfig(mcp_enabled): %v", err)
	}
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
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, r)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestAPIKeyRESTAccess(t *testing.T) {
	e := newEnv(t)
	user, _ := e.newUser(t, "bob")
	key := e.apiKeyFor(t, user.ID)

	// Create, then read back, through the key alone.
	resp := e.doKey(t, http.MethodPost, "/api/files",
		`{"name":"via key","html_content":"<p>hi</p>"}`, key)
	assertStatus(t, resp, http.StatusCreated)
	created := decodeFile(t, resp)

	resp = e.doKey(t, http.MethodGet, "/api/files/"+created.Slug, "", key)
	assertStatus(t, resp, http.StatusOK)
	if got := decodeFile(t, resp); got.HTMLContent != "<p>hi</p>" {
		t.Errorf("content = %q, want the uploaded source", got.HTMLContent)
	}

	// The key resolves to its owner, not to whoever happens to be user 1.
	resp = e.doKey(t, http.MethodGet, "/api/auth/me", "", key)
	assertStatus(t, resp, http.StatusOK)
	if body := bodyString(t, resp); !strings.Contains(body, `"bob"`) {
		t.Errorf("me = %s, want username bob", body)
	}

	// Owner scoping is unchanged: bob's key can't see the admin's file.
	adminFile := e.createViaAPI(t, e.authCookie(t), "admins", "<p>secret</p>")
	resp = e.doKey(t, http.MethodGet, "/api/files/"+adminFile.Slug, "", key)
	assertStatus(t, resp, http.StatusNotFound)
	bodyString(t, resp)

	// A key that matches no row is a plain 401.
	resp = e.doKey(t, http.MethodGet, "/api/files", "", "rb_nope")
	assertStatus(t, resp, http.StatusUnauthorized)
	bodyString(t, resp)
}

// TestAPIKeyRESTGates pins the two kill switches: the mcp_enabled config and
// account suspension both make every key dead on its next request.
func TestAPIKeyRESTGates(t *testing.T) {
	e := newEnv(t)
	user, _ := e.newUser(t, "carol")
	key := e.apiKeyFor(t, user.ID)

	resp := e.doKey(t, http.MethodGet, "/api/files", "", key)
	assertStatus(t, resp, http.StatusOK)
	bodyString(t, resp)

	if err := e.queries.SetConfig(context.Background(), sqlcgen.SetConfigParams{
		Key: "mcp_enabled", Value: "false",
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	resp = e.doKey(t, http.MethodGet, "/api/files", "", key)
	assertStatus(t, resp, http.StatusUnauthorized)
	bodyString(t, resp)

	if err := e.queries.SetConfig(context.Background(), sqlcgen.SetConfigParams{
		Key: "mcp_enabled", Value: "true",
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if _, err := e.queries.DisableUser(context.Background(), user.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	resp = e.doKey(t, http.MethodGet, "/api/files", "", key)
	assertStatus(t, resp, http.StatusUnauthorized)
	bodyString(t, resp)
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
	assertStatus(t, resp, http.StatusOK)
	bodyString(t, resp)

	// A non-admin key fails the role check before the auth-method check.
	user, _ := e.newUser(t, "dave")
	userKey := e.apiKeyFor(t, user.ID)
	resp = e.doKey(t, http.MethodGet, "/api/backup", "", userKey)
	assertStatus(t, resp, http.StatusForbidden)
	bodyString(t, resp)
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
	assertStatus(t, resp, http.StatusOK)
	if body := bodyString(t, resp); !strings.Contains(body, `"upload_default_public":true`) {
		t.Errorf("settings after update = %s, want upload_default_public true", body)
	}

	f := e.createViaAPI(t, cookie, "after", "<p>b</p>")
	if !f.IsPublic {
		t.Error("with the config on, a new file is private, want public")
	}
	// And the link works anonymously right away.
	resp = e.do(t, http.MethodGet, "/res/"+f.Slug+"?code="+f.AccessCode, "", nil)
	assertStatus(t, resp, http.StatusOK)
	bodyString(t, resp)

	key := e.apiKeyFor(t, e.admin.ID)
	if out := e.mcpUpload(t, key, "agent-doc", "markdown", "# hi"); out["is_public"] != false {
		t.Errorf("MCP upload with the config on: is_public = %v, want false", out["is_public"])
	}
}
