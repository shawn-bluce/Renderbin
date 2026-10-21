package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type sized struct {
	Size int64 `json:"size"`
}

type sizedFile struct {
	sized
	Slug       string `json:"slug"`
	AccessCode string `json:"access_code"`
}

// Every endpoint reports a file's size from the files.content_size column, so
// the numbers a client sees must agree whichever endpoint it asked: the write
// that stored the bytes, a metadata change after it, a read, and the listing.
func TestSizeIsConsistentAcrossEndpoints(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)

	sizeOf := func(resp *http.Response, want int) int64 {
		t.Helper()
		return decodeJSON[sized](t, resp, want).Size
	}

	content := strings.Repeat("é", 700) // multi-byte: bytes, not characters
	createBody, _ := json.Marshal(map[string]string{"name": "sized", "html_content": content})
	createResp := e.do(t, http.MethodPost, "/api/files", string(createBody), cookie)
	file := decodeJSON[sizedFile](t, createResp, http.StatusCreated)
	if file.Size != int64(len(content)) {
		t.Errorf("create size = %d, want %d", file.Size, len(content))
	}
	base := "/api/files/" + file.Slug
	if got := sizeOf(e.do(t, http.MethodGet, base, "", cookie), http.StatusOK); got != int64(len(content)) {
		t.Errorf("get size = %d, want %d", got, len(content))
	}
	if got := sizeOf(e.do(t, http.MethodPatch, base+"/visibility", `{"is_public":true}`, cookie), http.StatusOK); got != int64(len(content)) {
		t.Errorf("visibility size = %d, want %d", got, len(content))
	}

	longer := content + strings.Repeat("x", 300)
	body, _ := json.Marshal(map[string]string{
		"name": "sized", "slug": file.Slug, "html_content": longer, "access_code": file.AccessCode,
	})
	if got := sizeOf(e.do(t, http.MethodPatch, base, string(body), cookie), http.StatusOK); got != int64(len(longer)) {
		t.Errorf("update size = %d, want %d", got, len(longer))
	}
	if got := sizeOf(e.do(t, http.MethodPatch, base+"/name", `{"name":"renamed"}`, cookie), http.StatusOK); got != int64(len(longer)) {
		t.Errorf("rename size = %d, want %d", got, len(longer))
	}

	// A site's size is its stored (compressed) archive, on create and replace.
	site := e.uploadSite(t, cookie, "s", map[string]string{"index.html": "<h1>" + strings.Repeat("a", 2000) + "</h1>"})
	if want := e.archiveSize(t, e.fileRow(t, site.Slug)); site.Size != want {
		t.Errorf("site create size = %d, want %d", site.Size, want)
	}
}
