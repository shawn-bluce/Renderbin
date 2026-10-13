package server_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/handlers"
)

// TestSetupIsAtomicUnderConcurrency pins the fix for a first-run race.
//
// Setup used to read CountUsers, decide the table was empty, spend ~80ms
// hashing a password, and only then insert. Six concurrent requests all passed
// the check and created six accounts -- of which only id=1 got the
// non-transferable super-admin role, so a double-clicked welcome form left a
// stray account behind and a freshly exposed instance could be raced for
// ownership.
func TestSetupIsAtomicUnderConcurrency(t *testing.T) {
	e := newBareEnv(t)

	const attempts = 6
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses []int
	)
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := fmt.Sprintf(
				`{"username":"user%d","nickname":"User %d","password":"password123"}`, i, i)
			resp := e.do(t, http.MethodPost, "/api/setup", body, nil)
			defer resp.Body.Close()
			mu.Lock()
			statuses = append(statuses, resp.StatusCode)
			mu.Unlock()
		}()
	}
	wg.Wait()

	created := 0
	for _, s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			// The loser of the race, reported exactly as a late sequential
			// request already was.
		default:
			t.Errorf("unexpected status %d from concurrent setup", s)
		}
	}
	if created != 1 {
		t.Errorf("%d of %d concurrent setups succeeded, want exactly 1", created, attempts)
	}

	count, err := e.queries.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 1 {
		t.Errorf("users table holds %d rows after concurrent setup, want 1", count)
	}
}

// TestJSONEndpointsCapRequestBodies covers every endpoint that decodes a small
// JSON payload. encoding/json buffers the whole top-level value before it can
// decode it, so an uncapped Decode turns the request body into heap: a 64MB
// body on the unauthenticated login endpoint took this server from 21MB to
// 423MB of RSS.
func TestJSONEndpointsCapRequestBodies(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	file := e.createViaAPI(t, cookie, "doc", "<p>hi</p>")

	// Registration is gated before the body is read, so leaving it off would
	// make that case 403 without ever exercising the cap.
	enable := e.do(t, http.MethodPut, "/api/settings", `{"allow_registration":true}`, cookie)
	enable.Body.Close()

	// Comfortably over the 64KB cap, small enough to keep the test quick.
	huge := strings.Repeat("A", 200*1024)

	cases := []struct {
		name, method, path, body string
		cookie                   *http.Cookie
	}{
		{"login (unauthenticated)", http.MethodPost, "/api/auth/login",
			fmt.Sprintf(`{"username":"x","password":%q}`, huge), nil},
		{"register (unauthenticated)", http.MethodPost, "/api/auth/register",
			fmt.Sprintf(`{"username":"x","nickname":"x","password":%q}`, huge), nil},
		{"setup (unauthenticated)", http.MethodPost, "/api/setup",
			fmt.Sprintf(`{"username":"x","nickname":%q,"password":"password123"}`, huge), nil},
		{"rename", http.MethodPatch, "/api/files/" + file.Slug + "/name",
			fmt.Sprintf(`{"name":%q}`, huge), cookie},
		{"tags", http.MethodPatch, "/api/files/" + file.Slug + "/tags",
			fmt.Sprintf(`{"tags":%q}`, huge), cookie},
		{"expiry", http.MethodPatch, "/api/files/" + file.Slug + "/expiry",
			fmt.Sprintf(`{"ttl":%q}`, huge), cookie},
		{"visibility", http.MethodPatch, "/api/files/" + file.Slug + "/visibility",
			fmt.Sprintf(`{"is_public":true,"pad":%q}`, huge), cookie},
		{"profile", http.MethodPatch, "/api/user",
			fmt.Sprintf(`{"nickname":%q}`, huge), cookie},
		{"settings", http.MethodPut, "/api/settings",
			fmt.Sprintf(`{"allow_registration":true,"pad":%q}`, huge), cookie},
		{"admin status", http.MethodPatch, "/api/admin/users/2/status",
			fmt.Sprintf(`{"disabled":true,"pad":%q}`, huge), cookie},
		{"admin password", http.MethodPost, "/api/admin/users/2/password",
			fmt.Sprintf(`{"new_password":%q}`, huge), cookie},
		{"admin create", http.MethodPost, "/api/admin/users",
			fmt.Sprintf(`{"username":"x","nickname":%q}`, huge), cookie},
		{"admin quota", http.MethodPatch, "/api/admin/users/2/quota",
			fmt.Sprintf(`{"quota_bytes":1,"pad":%q}`, huge), cookie},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := e.do(t, c.method, c.path, c.body, c.cookie)
			body := bodyString(t, resp)
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Errorf("status = %d (%s), want 413 -- this endpoint decodes an uncapped body",
					resp.StatusCode, strings.TrimSpace(body))
			}
		})
	}
}

// TestPasswordByteLimitIsReported pins the fix for a first-screen 500.
//
// bcrypt refuses anything over 72 *bytes*, and nothing validated against it, so
// the error surfaced as "internal error". A Chinese character is three bytes,
// which meant an ordinary 25-character passphrase was rejected with no
// explanation on the very first page of the app.
func TestPasswordByteLimitIsReported(t *testing.T) {
	overLimit := strings.Repeat("密", 25) // 75 bytes
	atLimit := strings.Repeat("密", 24)   // 72 bytes

	t.Run("setup rejects it with a message, not a 500", func(t *testing.T) {
		e := newBareEnv(t)
		body, _ := json.Marshal(map[string]string{
			"username": "zhang", "nickname": "zhangsan", "password": overLimit,
		})
		resp := e.do(t, http.MethodPost, "/api/setup", string(body), nil)
		if msg := bodyOf(t, resp, http.StatusBadRequest); !strings.Contains(msg, "72 bytes") {
			t.Errorf("message = %q, want it to name the byte limit", strings.TrimSpace(msg))
		}
	})

	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		e := newBareEnv(t)
		body, _ := json.Marshal(map[string]string{
			"username": "zhang", "nickname": "zhangsan", "password": atLimit,
		})
		e.expect(t, http.StatusCreated, http.MethodPost, "/api/setup", string(body), nil)
	})

	t.Run("a long CJK nickname is measured in runes", func(t *testing.T) {
		e := newEnv(t)
		// 22 characters, 66 bytes: within the documented 64-character limit,
		// but over it when the limit is applied to bytes.
		body, _ := json.Marshal(map[string]any{"nickname": strings.Repeat("名", 22)})
		e.expect(t, http.StatusNoContent, http.MethodPatch, "/api/user", string(body), e.authCookie(t))
	})
}

// TestUnmatchedAPIPathsAnswerJSON404 pins that /api never falls through to the
// SPA. It used to: chi propagated the root NotFound into the subrouter, so a
// mistyped endpoint answered 200 with index.html, fetch() threw an opaque JSON
// parse error, and any uptime check saw a healthy 200 for a route that does not
// exist.
func TestUnmatchedAPIPathsAnswerJSON404(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	paths := []string{
		"/api/nope",
		"/api/v2/files",
		"/api/files/abc/nope",
		"/api/admin/nope",
		"/api/auth/reset-password",
	}
	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			t.Run(method+" "+path, func(t *testing.T) {
				resp := e.do(t, method, path, "", cookie)
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusNotFound {
					t.Errorf("status = %d, want 404", resp.StatusCode)
				}
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("Content-Type = %q, want JSON -- an API client should never be handed the SPA", ct)
				}
			})
		}
	}

	// The SPA catch-all still answers everything outside /api.
	resp := e.do(t, http.MethodGet, "/some/spa/route", "", cookie)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Error("client-side routes must still fall back to the SPA")
	}
}

// TestRenderSandboxesUserContent pins the header that separates "we serve
// uploader HTML verbatim" from "any account can take over any other".
//
// Documents are served from the same origin as the API. The session cookie is
// HttpOnly, which stops a script reading it but not from using it: a
// same-origin fetch carries it automatically. Omitting allow-same-origin puts
// the document in an opaque origin, so those fetches go out cross-origin,
// without cookies, and are refused for lack of CORS headers.
func TestRenderSandboxesUserContent(t *testing.T) {
	e := newEnv(t)
	e.createRaw(t, "doc", "code", "<script>fetch('/api/files')</script>", true)

	for _, tc := range []struct {
		name, url string
		status    int
	}{
		{"served", "/res/doc?code=code", http.StatusOK},
		{"blocked", "/res/doc?code=wrong", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := e.do(t, http.MethodGet, tc.url, "", nil)
			defer resp.Body.Close()
			assertStatus(t, resp, tc.status)

			csp := resp.Header.Get("Content-Security-Policy")
			if !strings.HasPrefix(csp, "sandbox") {
				t.Fatalf("Content-Security-Policy = %q, want a sandbox policy", csp)
			}
			// The whole point. With allow-same-origin the sandbox stops
			// isolating the document from the app it is served beside.
			if strings.Contains(csp, "allow-same-origin") {
				t.Errorf("CSP grants allow-same-origin (%q); uploader scripts would act as the viewer", csp)
			}
			// Scripts still run -- the feature is serving authored HTML, and
			// the isolation is what makes that safe rather than banning it.
			if !strings.Contains(csp, "allow-scripts") {
				t.Errorf("CSP = %q, want scripts still permitted inside the sandbox", csp)
			}
			// The access code is in the query string, so a referrer leaving
			// this origin would hand it to a third party.
			if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy = %q, want no-referrer", got)
			}
			if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

// TestAdminCannotResetOwnPassword pins the second half of the takeover chain
// the sandbox above closes.
//
// The endpoint exists to rescue a locked-out account and deliberately does not
// ask for the old password. Targeting yourself with it turned that into a way
// for a session to change the password it was authenticated with while knowing
// nothing about it -- one request that both takes the account and locks its
// owner out, with the reset-password CLI as the only way back.
func TestAdminCannotResetOwnPassword(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	before := e.currentHash(t, e.admin.ID)

	path := fmt.Sprintf("/api/admin/users/%d/password", e.admin.ID)
	resp := e.do(t, http.MethodPost, path, `{"new_password":"attacker-owns-you"}`, cookie)
	if msg := bodyOf(t, resp, http.StatusForbidden); !strings.Contains(msg, "profile settings") {
		t.Errorf("message = %q, want it to point at the self-service path", strings.TrimSpace(msg))
	}
	if after := e.currentHash(t, e.admin.ID); after != before {
		t.Error("the password changed despite the 403")
	}

	// Someone else's password is still resettable -- that is the feature.
	other, _ := e.newUser(t, "colleague")
	otherPath := fmt.Sprintf("/api/admin/users/%d/password", other.ID)
	ok := e.do(t, http.MethodPost, otherPath, `{"new_password":"fresh-password"}`, cookie)
	defer ok.Body.Close()
	assertStatus(t, ok, http.StatusNoContent)
}

// TestOversizedDocumentReportsItsLimit pins that the advertised 5MB limit is
// reachable and that exceeding it says so.
//
// The raw-body cap used to be maxHTMLBytes+64KB, while the 5MB check ran on the
// decoded string. JSON escaping costs a byte per quote, backslash and newline,
// so a legal 5MB document blew the raw cap first and came back as 400 "invalid
// request body" -- sending people to look for a syntax error that did not
// exist.
func TestOversizedDocumentReportsItsLimit(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	t.Run("a 5MB document full of escapes is accepted", func(t *testing.T) {
		// Every other byte needs escaping in JSON, so the encoded body is
		// roughly 1.5x the document.
		content := strings.Repeat("a\n", (5<<20)/2)
		body, _ := json.Marshal(map[string]string{"name": "big", "html_content": content})
		resp := e.do(t, http.MethodPost, "/api/files", string(body), cookie)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("status = %d, want 201 for a document within the documented limit", resp.StatusCode)
		}
	})

	t.Run("over the limit is 413 naming the limit", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{
			"name": "toobig", "html_content": strings.Repeat("a", (5<<20)+1),
		})
		resp := e.do(t, http.MethodPost, "/api/files", string(body), cookie)
		if msg := bodyOf(t, resp, http.StatusRequestEntityTooLarge); !strings.Contains(msg, "5MB") {
			t.Errorf("message = %q, want it to name the 5MB limit", strings.TrimSpace(msg))
		}
	})

	t.Run("an absurd body is refused outright", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{
			"name": "absurd", "html_content": strings.Repeat("a", 12<<20),
		})
		e.expect(t, http.StatusRequestEntityTooLarge, http.MethodPost, "/api/files", string(body), cookie)
	})
}

// TestNameLengthIsCapped pins a bound on files.name, which is echoed into the
// Content-Disposition header of every download: a name longer than the reverse
// proxy's header buffer turns that download into a 502 with nothing in this
// app's logs to explain it.
func TestNameLengthIsCapped(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	long := strings.Repeat("x", 256)

	body, _ := json.Marshal(map[string]string{"name": long, "html_content": "<p>hi</p>"})
	e.expect(t, http.StatusBadRequest, http.MethodPost, "/api/files", string(body), cookie)

	file := e.createViaAPI(t, cookie, "ok", "<p>hi</p>")
	renameBody, _ := json.Marshal(map[string]string{"name": long})
	e.expect(t, http.StatusBadRequest, http.MethodPatch, "/api/files/"+file.Slug+"/name", string(renameBody), cookie)
}

type usageResp struct {
	UsedBytes  int64 `json:"used_bytes"`
	QuotaBytes int64 `json:"quota_bytes"`
}

func (e *testEnv) usage(t *testing.T, cookie *http.Cookie) usageResp {
	t.Helper()
	return decodeJSON[usageResp](t, e.do(t, http.MethodGet, "/api/user/usage", "", cookie), http.StatusOK)
}

// TestStorageQuotaIsEnforced covers the per-account limit end to end: the
// figure the dashboard shows, the upload that trips it, and the two ways it
// deliberately does not behave.
func TestStorageQuotaIsEnforced(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	if got := e.usage(t, cookie).QuotaBytes; got != 100<<20 {
		t.Errorf("default quota = %d, want 100MB", got)
	}

	// Squeeze the admin's own quota down to something a test can fill.
	quota := fmt.Sprintf(`{"quota_bytes":%d}`, 4096)
	e.expect(t, http.StatusNoContent, http.MethodPatch,
		fmt.Sprintf("/api/admin/users/%d/quota", e.admin.ID), quota, cookie)

	content := strings.Repeat("a", 2000)
	first := e.createViaAPI(t, cookie, "one", content)
	if u := e.usage(t, cookie); u.UsedBytes != 2000 {
		t.Errorf("used = %d after one 2000-byte file, want 2000", u.UsedBytes)
	}

	// Second file fits; third does not.
	e.createViaAPI(t, cookie, "two", content)
	body, _ := json.Marshal(map[string]string{"name": "three", "html_content": content})
	over := e.do(t, http.MethodPost, "/api/files", string(body), cookie)
	if msg := bodyOf(t, over, http.StatusRequestEntityTooLarge); !strings.Contains(msg, "quota") {
		t.Errorf("message = %q, want it to say the quota is full", strings.TrimSpace(msg))
	}

	t.Run("an in-place edit is not charged twice", func(t *testing.T) {
		// Rewriting an existing 2000-byte file with 2000 bytes leaves usage
		// unchanged, so it must be allowed even with the account at its limit.
		edit, _ := json.Marshal(map[string]string{
			"name": "one", "slug": first.Slug, "access_code": first.AccessCode,
			"html_content": strings.Repeat("b", 2000),
		})
		resp := e.do(t, http.MethodPatch, "/api/files/"+first.Slug, string(edit), cookie)
		defer resp.Body.Close()
		assertStatus(t, resp, http.StatusOK)
	})

	t.Run("trashing a file does not free quota", func(t *testing.T) {
		// A trashed row still occupies the database, and emptying the trash is
		// a user action -- if soft delete freed quota it would be an unlimited
		// bypass.
		e.trash(t, cookie, first.Slug)
		if u := e.usage(t, cookie); u.UsedBytes != 4000 {
			t.Errorf("used = %d after trashing a file, want it unchanged at 4000", u.UsedBytes)
		}

		e.expect(t, http.StatusOK, http.MethodDelete, "/api/trash", "", cookie)
		if u := e.usage(t, cookie); u.UsedBytes != 2000 {
			t.Errorf("used = %d after emptying the trash, want 2000", u.UsedBytes)
		}
	})
}

type createdUserResp struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Password string `json:"password"`
}

// TestAdminCreateAccount covers adding a colleague without opening global
// self-registration, which was the only way to do it before.
func TestAdminCreateAccount(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	created := decodeJSON[createdUserResp](t, e.do(t, http.MethodPost, "/api/admin/users",
		`{"username":"colleague","nickname":"A Colleague"}`, cookie), http.StatusCreated)

	if created.Password == "" {
		t.Fatal("no password returned; the caller has no way to hand the account over")
	}
	if len(created.Password) < 12 {
		t.Errorf("generated password is %d characters, want something not worth guessing", len(created.Password))
	}

	// The returned password is the real one.
	login, _ := json.Marshal(map[string]string{
		"username": created.Username, "password": created.Password,
	})
	ok := e.do(t, http.MethodPost, "/api/auth/login", string(login), nil)
	defer ok.Body.Close()
	assertStatus(t, ok, http.StatusNoContent)

	t.Run("registration stays closed throughout", func(t *testing.T) {
		// The whole point of this endpoint: no global toggle had to be flipped.
		body := `{"username":"stranger","nickname":"Stranger","password":"password123"}`
		resp := e.do(t, http.MethodPost, "/api/auth/register", body, nil)
		defer resp.Body.Close()
		assertStatus(t, resp, http.StatusForbidden)
	})

	t.Run("a duplicate username is a 409", func(t *testing.T) {
		resp := e.do(t, http.MethodPost, "/api/admin/users",
			`{"username":"colleague","nickname":"Again"}`, cookie)
		defer resp.Body.Close()
		assertStatus(t, resp, http.StatusConflict)
	})
}

// TestAdminDeleteAccount covers removing an account and everything it owns.
//
// The files are hard-deleted with it rather than trashed: leaving them would
// leave rows owned by an id that no longer exists, which no listing shows, no
// link serves, and nothing in the app can clean up.
func TestAdminDeleteAccount(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	victim, victimCookie := e.newUser(t, "leaver")

	active := e.createViaAPI(t, victimCookie, "active", "<p>a</p>")
	trashed := e.createViaAPI(t, victimCookie, "trashed", "<p>b</p>")
	del := e.do(t, http.MethodDelete, "/api/files/"+trashed.Slug, "", victimCookie)
	del.Body.Close()

	out := decodeJSON[struct {
		DeletedFiles int64 `json:"deleted_files"`
	}](t, e.do(t, http.MethodDelete, fmt.Sprintf("/api/admin/users/%d", victim.ID), "", cookie), http.StatusOK)
	if out.DeletedFiles != 2 {
		t.Errorf("deleted_files = %d, want 2 (the trashed one counts)", out.DeletedFiles)
	}

	// The account is gone, its session no longer resolves, and its links 404.
	if _, err := e.queries.GetUserByID(context.Background(), victim.ID); err == nil {
		t.Error("the user row survived the delete")
	}
	me := e.do(t, http.MethodGet, "/api/auth/me", "", victimCookie)
	defer me.Body.Close()
	assertStatus(t, me, http.StatusUnauthorized)

	render := e.do(t, http.MethodGet, "/res/"+active.Slug+"?code="+active.AccessCode, "", nil)
	defer render.Body.Close()
	assertStatus(t, render, http.StatusNotFound)

	// No orphan rows: a file owned by an id that no longer exists would be
	// invisible to every listing and impossible to remove through the app.
	var orphans int
	if err := e.conn.QueryRow(
		`SELECT COUNT(*) FROM files WHERE user_id NOT IN (SELECT id FROM users)`).Scan(&orphans); err != nil {
		t.Fatalf("count orphan files: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d files are owned by a user that no longer exists", orphans)
	}

	t.Run("the super admin cannot be deleted", func(t *testing.T) {
		// It is the only account that can manage accounts, so this would be a
		// one-way door out of the app.
		resp := e.do(t, http.MethodDelete,
			fmt.Sprintf("/api/admin/users/%d", handlers.SuperAdminID), "", cookie)
		defer resp.Body.Close()
		assertStatus(t, resp, http.StatusForbidden)
	})

	t.Run("an unknown id is a 404 and commits nothing", func(t *testing.T) {
		before, err := e.queries.CountUsers(context.Background())
		if err != nil {
			t.Fatalf("CountUsers: %v", err)
		}
		resp := e.do(t, http.MethodDelete, "/api/admin/users/9999", "", cookie)
		defer resp.Body.Close()
		assertStatus(t, resp, http.StatusNotFound)
		after, err := e.queries.CountUsers(context.Background())
		if err != nil {
			t.Fatalf("CountUsers: %v", err)
		}
		if before != after {
			t.Errorf("user count changed from %d to %d on a 404", before, after)
		}
	})
}

// TestListingsCarrySizeWithoutContent pins both halves of the listing fix: the
// size the dashboard needs is present, and the document body that used to come
// with it -- 350MB of RSS to answer a 13KB request -- is not.
func TestListingsCarrySizeWithoutContent(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	content := strings.Repeat("x", 1234)
	file := e.createViaAPI(t, cookie, "sized", content)

	listed := decodeJSON[[]struct {
		Slug        string `json:"slug"`
		Size        int64  `json:"size"`
		HTMLContent string `json:"html_content"`
	}](t, e.do(t, http.MethodGet, "/api/files", "", cookie), http.StatusOK)
	if len(listed) != 1 {
		t.Fatalf("listed %d files, want 1", len(listed))
	}
	if listed[0].Size != int64(len(content)) {
		t.Errorf("size = %d, want %d", listed[0].Size, len(content))
	}
	if listed[0].HTMLContent != "" {
		t.Error("the listing returned the document body; that is the read this change exists to avoid")
	}

	// The size follows an edit, because the column is written by the same
	// statement as the content.
	edit, _ := json.Marshal(map[string]string{
		"name": "sized", "slug": file.Slug, "access_code": file.AccessCode,
		"html_content": "short",
	})
	updated := e.do(t, http.MethodPatch, "/api/files/"+file.Slug, string(edit), cookie)
	updated.Body.Close()

	var size int64
	if err := e.conn.QueryRow(
		`SELECT content_size FROM files WHERE slug = ?`, file.Slug).Scan(&size); err != nil {
		t.Fatalf("read content_size: %v", err)
	}
	if size != int64(len("short")) {
		t.Errorf("content_size = %d after an edit, want %d", size, len("short"))
	}

	// Single-file reads still carry the content -- that is what the editor loads.
	one := e.do(t, http.MethodGet, "/api/files/"+file.Slug, "", cookie)
	if got := decodeFile(t, one, http.StatusOK).HTMLContent; got != "short" {
		t.Errorf("GET one file returned %q, want the content", got)
	}
}

// TestSearchReturnsContentSnippets covers the SQL half of the snippet change:
// the excerpt is now cut out by substr() in the query rather than by slicing a
// full document in Go, so this exercises instr()/substr() against a live
// database. TestSnippetFromWindow in internal/handlers covers the Go half.
func TestSearchReturnsContentSnippets(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	content := strings.Repeat("a", 500) + "NEEDLE" + strings.Repeat("b", 500)
	e.createViaAPI(t, cookie, "haystack", content)
	// CJK: SQLite's instr()/substr() count characters, which is what keeps the
	// window from splitting a multi-byte rune.
	e.createViaAPI(t, cookie, "chinese", strings.Repeat("汉", 300)+"目标"+strings.Repeat("字", 300))

	cases := []struct {
		query, want string
	}{
		{"needle", "…" + strings.Repeat("a", 100) + "NEEDLE" + strings.Repeat("b", 100) + "…"},
		{"目标", "…" + strings.Repeat("汉", 100) + "目标" + strings.Repeat("字", 100) + "…"},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			results := e.search(t, cookie, "q="+url.QueryEscape(c.query)+"&content=true")
			if len(results) != 1 {
				t.Fatalf("got %d results, want 1", len(results))
			}
			if !results[0].MatchedContent {
				t.Error("matched_content = false, want a content hit")
			}
			if results[0].Snippet != c.want {
				t.Errorf("snippet = %q, want %q", results[0].Snippet, c.want)
			}
		})
	}
}

// TestQuotaAppliesPerAccount pins that the limit is the target account's, not a
// global one, and that lowering it below current usage blocks new writes
// without touching stored files.
func TestQuotaAppliesPerAccount(t *testing.T) {
	e := newEnv(t)
	adminCookie := e.authCookie(t)
	other, otherCookie := e.newUser(t, "colleague")

	existing := e.createViaAPI(t, otherCookie, "existing", strings.Repeat("x", 3000))

	e.expect(t, http.StatusNoContent, http.MethodPatch,
		fmt.Sprintf("/api/admin/users/%d/quota", other.ID), `{"quota_bytes":100}`, adminCookie)

	body, _ := json.Marshal(map[string]string{"name": "new", "html_content": "x"})
	e.expect(t, http.StatusRequestEntityTooLarge, http.MethodPost, "/api/files", string(body), otherCookie)

	// The file they already had is untouched and still served.
	read := e.do(t, http.MethodGet, "/api/files/"+existing.Slug, "", otherCookie)
	defer read.Body.Close()
	assertStatus(t, read, http.StatusOK)

	// The admin's own uploads are unaffected: quotas are per account.
	e.createViaAPI(t, adminCookie, "mine", "<p>fine</p>")
}

// TestMCPRespectsAccountLimits pins that an API key is not a way around the
// limits the web API enforces.
//
// The quota and the name cap live on the account, not on the protocol, so both
// ways in have to apply them. They did not: MCP validated only the 5MB
// per-document ceiling, so a key could upload 20 files of 5MB per call without
// bound while the dashboard's own uploads were being refused — and the usage
// readout would sail past the quota with nothing able to explain it.
func TestMCPRespectsAccountLimits(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	key := e.enableMCP(t)

	// A quota small enough to fill in one upload.
	e.expect(t, http.StatusNoContent, http.MethodPatch,
		fmt.Sprintf("/api/admin/users/%d/quota", e.admin.ID), `{"quota_bytes":4096}`, cookie)

	fill := e.mcpUpload(t, key, "first", "markdown", strings.Repeat("a", 3000))
	if fill["slug"] == nil {
		t.Fatalf("the first upload should fit the quota: %v", fill)
	}

	t.Run("upload_file stops at the quota", func(t *testing.T) {
		resp := e.mcpCall(t, key, "upload_file", map[string]any{
			"name": "second", "kind": "markdown", "content": strings.Repeat("b", 3000),
		})
		res := decodeMCPResult(t, resp)
		if !res.IsError {
			t.Fatal("upload succeeded past the account's quota")
		}
		if !strings.Contains(res.text(), "quota") {
			t.Errorf("error = %q, want it to name the quota", res.text())
		}
	})

	t.Run("upload_files stops at the quota too", func(t *testing.T) {
		// The batch tool builds each file through the same path, so a partial
		// batch must report the failures rather than storing them.
		resp := e.mcpCall(t, key, "upload_files", map[string]any{
			"files": []map[string]any{
				{"name": "a", "kind": "markdown", "content": strings.Repeat("c", 3000)},
				{"name": "b", "kind": "markdown", "content": strings.Repeat("d", 3000)},
			},
		})
		if text := decodeMCPResult(t, resp).text(); !strings.Contains(text, "quota") {
			t.Errorf("batch result = %q, want the quota named", text)
		}
	})

	t.Run("update_file stops at the quota", func(t *testing.T) {
		resp := e.mcpCall(t, key, "update_file", map[string]any{
			"slug": fill["slug"], "content": strings.Repeat("e", 5000),
		})
		res := decodeMCPResult(t, resp)
		if !res.IsError {
			t.Fatal("update grew the account past its quota")
		}
	})

	t.Run("an in-place rewrite of the same size is still allowed", func(t *testing.T) {
		resp := e.mcpCall(t, key, "update_file", map[string]any{
			"slug": fill["slug"], "content": strings.Repeat("f", 3000),
		})
		if res := decodeMCPResult(t, resp); res.IsError {
			t.Errorf("same-size rewrite refused: %s", res.text())
		}
	})

	t.Run("the name cap applies", func(t *testing.T) {
		// Unbounded here, the name would land in Content-Disposition and blow
		// past a reverse proxy's header buffer on every download.
		resp := e.mcpCall(t, key, "upload_file", map[string]any{
			"name": strings.Repeat("x", 5000), "kind": "markdown", "content": "hi",
		})
		res := decodeMCPResult(t, resp)
		if !res.IsError {
			t.Fatal("MCP accepted a 5000-byte file name")
		}
		if !strings.Contains(res.text(), "name") {
			t.Errorf("error = %q, want it to be about the name", res.text())
		}
	})

	// Whatever was refused above, nothing landed: the account is still inside
	// its limit and the readout the dashboard shows agrees.
	if u := e.usage(t, cookie); u.UsedBytes > u.QuotaBytes {
		t.Errorf("used %d bytes against a %d-byte quota", u.UsedBytes, u.QuotaBytes)
	}
}

// TestAppPagesAreNotFramable pins the clickjacking header across the routes it
// actually has to cover.
//
// It was skipped for anything starting with "/res/", but only /res/{slug} is a
// real route -- "/res/" and "/res/a/b" fall through to the SPA catch-all, so
// the app's own pages were framable at those URLs. The exemption now follows
// the render handler instead of a path prefix.
func TestAppPagesAreNotFramable(t *testing.T) {
	e := newEnv(t)
	e.createRaw(t, "doc", "code", "<p>shared</p>", true)

	framable := []string{"/res/doc?code=code"}
	notFramable := []string{
		"/", "/settings", "/api/health", "/res/", "/res/a/b", "/res/nope?code=x",
	}

	for _, path := range framable {
		resp := e.do(t, http.MethodGet, path, "", nil)
		resp.Body.Close()
		if got := resp.Header.Get("X-Frame-Options"); got != "" {
			t.Errorf("%s: X-Frame-Options = %q, want it unset so an owner can embed their own page", path, got)
		}
	}
	for _, path := range notFramable {
		resp := e.do(t, http.MethodGet, path, "", nil)
		resp.Body.Close()
		if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", path, got)
		}
	}
}

// TestSuperAdminEndpointsRejectOrdinaryUsers pins the role check on every
// endpoint that is the super admin's alone. An ordinary account gets 403 (the
// path exists, only the privilege is in question), not 404 -- and the 403 must
// be the privilege check firing before the work, so each target is checked for
// side effects afterwards. (Key-authenticated requests are refused even for
// the super admin: TestAPIKeyCannotUseSuperAdminEndpoints.)
func TestSuperAdminEndpointsRejectOrdinaryUsers(t *testing.T) {
	e := newEnv(t)
	victim, _ := e.newUser(t, "victim")
	caller, cookie := e.newUser(t, "regular")
	kept := e.createViaAPI(t, e.authCookie(t), "keep me", "<p>safe</p>")
	snapshot := e.downloadBackup(t, e.authCookie(t))
	id := fmt.Sprint(victim.ID)

	for _, c := range []struct{ name, method, path, body string }{
		{"download backup", http.MethodGet, "/api/backup", ""},
		{"restore backup", http.MethodPost, "/api/backup/restore", string(snapshot)},
		{"write settings", http.MethodPut, "/api/settings", `{"allow_registration":true}`},
		{"list accounts", http.MethodGet, "/api/admin/users", ""},
		{"create account", http.MethodPost, "/api/admin/users", `{"username":"sneaky","nickname":"Sneaky"}`},
		{"delete account", http.MethodDelete, "/api/admin/users/" + id, ""},
		{"delete the super admin", http.MethodDelete, fmt.Sprintf("/api/admin/users/%d", handlers.SuperAdminID), ""},
		{"suspend account", http.MethodPatch, "/api/admin/users/" + id + "/status", `{"disabled":true}`},
		{"set own quota", http.MethodPatch, fmt.Sprintf("/api/admin/users/%d/quota", caller.ID), `{"quota_bytes":999999}`},
		{"reset password", http.MethodPost, "/api/admin/users/" + id + "/password", `{"new_password":"hijacked"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			e.expect(t, http.StatusForbidden, c.method, c.path, c.body, cookie)
		})
	}

	ctx := context.Background()
	if got, err := e.queries.GetUserByID(ctx, victim.ID); err != nil {
		t.Errorf("victim row disappeared: %v", err)
	} else if got.DisabledAt.Valid || !auth.VerifyPassword(got.PasswordHash, "secret2") {
		t.Errorf("a rejected admin request changed the victim: %+v", got)
	}
	if got, err := e.queries.GetUserByID(ctx, caller.ID); err != nil || got.QuotaBytes != caller.QuotaBytes {
		t.Errorf("caller's row or quota changed: %+v, %v", got, err)
	}
	if _, err := e.queries.GetUserByUsername(ctx, "sneaky"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("a rejected create made the account anyway: %v", err)
	}
	if _, err := e.queries.GetUserByID(ctx, handlers.SuperAdminID); err != nil {
		t.Errorf("super admin row disappeared: %v", err)
	}
	e.expect(t, http.StatusOK, http.MethodGet, "/api/files/"+kept.Slug, "", e.authCookie(t))
	if cfg, err := e.queries.GetConfig(ctx, "allow_registration"); err == nil && cfg == "true" {
		t.Error("a rejected settings write took effect")
	}
}
