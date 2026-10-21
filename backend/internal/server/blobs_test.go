package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Content lives in the blob store: what the HTTP surface does with it.
// ---------------------------------------------------------------------------

func (e *testEnv) orphaned(t *testing.T, blobID string) bool {
	t.Helper()
	var n int
	if err := e.conn.QueryRow(`SELECT orphaned_at IS NOT NULL FROM blobs WHERE id = ?`, blobID).Scan(&n); err != nil {
		t.Fatalf("read blob %s: %v", blobID, err)
	}
	return n == 1
}

func TestContentIsStoredAsABlob(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<h1>on disk</h1>")

	row := e.fileRow(t, f.Slug)
	onDisk, err := os.ReadFile(filepath.Join(e.blobs.Dir(), row.BlobID[:2], row.BlobID))
	if err != nil || string(onDisk) != "<h1>on disk</h1>" {
		t.Fatalf("blob file = %q, %v; want the document", onDisk, err)
	}
	if e.orphaned(t, row.BlobID) {
		t.Error("a created file's blob is marked orphaned, so the collector would delete it")
	}
}

// The dashboard's editor sends the whole document with every save, rename
// included. A save that leaves the content as it was keeps the blob; one that
// changes it writes a new blob and lets go of the old.
func TestEditKeepsUnchangedContent(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>v1</p>")
	v1 := e.fileRow(t, f.Slug).BlobID

	patch := func(name, content string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{
			"name": name, "slug": f.Slug, "access_code": f.AccessCode, "html_content": content,
		})
		resp := e.do(t, http.MethodPatch, "/api/files/"+f.Slug, string(body), cookie)
		assertStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	}

	patch("renamed", "<p>v1</p>")
	if got := e.fileRow(t, f.Slug).BlobID; got != v1 {
		t.Errorf("a rename-only save wrote a new blob (%s -> %s)", v1, got)
	}

	patch("renamed", "<p>v2</p>")
	row := e.fileRow(t, f.Slug)
	if row.BlobID == v1 || string(e.content(t, row)) != "<p>v2</p>" {
		t.Fatalf("after an edit: blob %s holds %q", row.BlobID, e.content(t, row))
	}
	if !e.orphaned(t, v1) || e.orphaned(t, row.BlobID) {
		t.Error("an edit must orphan the old blob and adopt the new one")
	}
}

func TestDownloadStreamsFromTheBlob(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<h1>ranged</h1>")

	resp := e.raw(t, http.MethodGet, "/api/files/"+f.Slug+"/download", nil, cookie, map[string]string{"Range": "bytes=0-3"})
	assertStatus(t, resp, http.StatusPartialContent)
	if body := bodyString(t, resp); body != "<h1>" {
		t.Errorf("ranged download = %q, want the first four bytes", body)
	}
}

// A file whose blob has gone missing is data loss: it answers 500 and is
// logged, rather than serving an empty document as if nothing were wrong.
func TestMissingContentIsAnError(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	f := e.createViaAPI(t, cookie, "doc", "<p>x</p>")
	if err := e.blobs.Remove(e.fileRow(t, f.Slug).BlobID); err != nil {
		t.Fatal(err)
	}
	resp := e.do(t, http.MethodGet, "/api/files/"+f.Slug, "", cookie)
	assertStatus(t, resp, http.StatusInternalServerError)
	resp.Body.Close()
	resp = e.do(t, http.MethodGet, "/res/"+f.Slug, "", cookie)
	assertStatus(t, resp, http.StatusInternalServerError)
	resp.Body.Close()
}
