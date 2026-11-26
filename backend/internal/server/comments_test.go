package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shawn-bluce/renderbin/backend/internal/cron"
)

type commentResp struct {
	ID        int64  `json:"id"`
	ParentID  int64  `json:"parent_id"`
	Quote     string `json:"quote"`
	Body      string `json:"body"`
	Author    string `json:"author"`
	Mine      bool   `json:"mine"`
	CanDelete bool   `json:"can_delete"`
}

func (e *testEnv) comments(t *testing.T, cookie *http.Cookie, slugAndQuery string) []commentResp {
	t.Helper()
	slug, query, _ := strings.Cut(slugAndQuery, "?")
	path := "/api/comments/" + slug
	if query != "" {
		path += "?" + query
	}
	var out struct {
		Comments []commentResp `json:"comments"`
	}
	if err := json.Unmarshal([]byte(bodyOf(t, e.do(t, http.MethodGet, path, "", cookie), http.StatusOK)), &out); err != nil {
		t.Fatalf("decode comments: %v", err)
	}
	return out.Comments
}

// comment posts one comment and returns it; want is the expected status.
func (e *testEnv) comment(t *testing.T, cookie *http.Cookie, slugAndQuery string, want int, parent int64, quote, body string) commentResp {
	t.Helper()
	slug, query, _ := strings.Cut(slugAndQuery, "?")
	path := "/api/comments/" + slug
	if query != "" {
		path += "?" + query
	}
	req, _ := json.Marshal(map[string]any{"parent_id": parent, "quote": quote, "body": body})
	resp := e.do(t, http.MethodPost, path, string(req), cookie)
	if want != http.StatusCreated {
		e.expectStatus(t, resp, want)
		return commentResp{}
	}
	var c commentResp
	if err := json.Unmarshal([]byte(bodyOf(t, resp, want)), &c); err != nil {
		t.Fatalf("decode comment: %v", err)
	}
	return c
}

func (e *testEnv) expectStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer resp.Body.Close()
	assertStatus(t, resp, want)
}

// Comments are shared among everyone who may read the document; replies go one
// level deep; the author and the document's owner may delete, and deleting a
// comment takes its replies along.
func TestCommentsThreadsAndDeletion(t *testing.T) {
	e := newEnv(t)
	owner := e.authCookie(t)
	_, reader := e.newUser(t, "reader")
	_, stranger := e.newUser(t, "stranger")
	e.createRaw(t, "doc", "code1", "<p>hello world</p>", true)

	// A reader holding the code comments; the owner replies.
	top := e.comment(t, reader, "doc?code=code1", http.StatusCreated, 0, "hello world", "what is this?")
	if top.Author != "reader" || !top.Mine || !top.CanDelete || top.Quote != "hello world" || top.ParentID != 0 {
		t.Fatalf("created comment = %+v", top)
	}
	reply := e.comment(t, owner, "doc", http.StatusCreated, top.ID, "ignored for a reply", "a greeting")
	if reply.ParentID != top.ID || reply.Quote != "" || reply.Mine != true {
		t.Fatalf("reply = %+v, want a reply with no quote of its own", reply)
	}

	// Both see both, each with their own mine/can_delete.
	seenByOwner := e.comments(t, owner, "doc")
	if len(seenByOwner) != 2 || seenByOwner[0].ID != top.ID || seenByOwner[1].ID != reply.ID {
		t.Fatalf("owner sees %+v", seenByOwner)
	}
	if seenByOwner[0].Mine || !seenByOwner[0].CanDelete || !seenByOwner[1].Mine {
		t.Errorf("owner flags = %+v", seenByOwner)
	}
	seenByReader := e.comments(t, reader, "doc?code=code1")
	if len(seenByReader) != 2 || !seenByReader[0].Mine || !seenByReader[0].CanDelete || seenByReader[1].Mine || seenByReader[1].CanDelete {
		t.Errorf("reader flags = %+v", seenByReader)
	}

	// One level only, and only onto this document's top-level comments.
	e.comment(t, reader, "doc?code=code1", http.StatusBadRequest, reply.ID, "", "reply to a reply")
	e.comment(t, reader, "doc?code=code1", http.StatusBadRequest, 99999, "", "reply to nothing")
	e.createRaw(t, "other", "code2", "<p>x</p>", true)
	e.comment(t, owner, "other", http.StatusBadRequest, top.ID, "", "reply across documents")
	// Input rules.
	e.comment(t, reader, "doc?code=code1", http.StatusBadRequest, 0, "", "no quote")
	e.comment(t, reader, "doc?code=code1", http.StatusBadRequest, 0, "q", "   ")
	e.comment(t, reader, "doc?code=code1", http.StatusRequestEntityTooLarge, 0, strings.Repeat("x", 2001), "b")
	e.comment(t, reader, "doc?code=code1", http.StatusRequestEntityTooLarge, 0, "q", strings.Repeat("x", 8193))

	// The reader may not delete the owner's reply; a stranger sees nothing.
	e.expect(t, http.StatusForbidden, http.MethodDelete, fmt.Sprintf("/api/comments/doc/%d?code=code1", reply.ID), "", reader)
	e.expect(t, http.StatusNotFound, http.MethodDelete, fmt.Sprintf("/api/comments/doc/%d", top.ID), "", stranger)
	if got := e.comments(t, owner, "doc"); len(got) != 2 {
		t.Fatalf("a refused delete removed comments: %+v", got)
	}

	// The owner may delete someone else's comment; its reply goes with it.
	e.expect(t, http.StatusNoContent, http.MethodDelete, fmt.Sprintf("/api/comments/doc/%d", top.ID), "", owner)
	if got := e.comments(t, owner, "doc"); len(got) != 0 {
		t.Fatalf("after deleting the thread %+v remain", got)
	}

	// An author deletes their own; a second delete is a 404.
	mine := e.comment(t, reader, "doc?code=code1", http.StatusCreated, 0, "hello", "mine")
	e.expect(t, http.StatusNoContent, http.MethodDelete, fmt.Sprintf("/api/comments/doc/%d?code=code1", mine.ID), "", reader)
	e.expect(t, http.StatusNotFound, http.MethodDelete, fmt.Sprintf("/api/comments/doc/%d?code=code1", mine.ID), "", reader)
}

// Who may comment is who may read: the same rule as /res and the chat.
func TestCommentsFollowReadAccess(t *testing.T) {
	e := newEnv(t)
	owner := e.authCookie(t)
	_, reader := e.newUser(t, "reader")
	e.createRaw(t, "pub", "code1", "<p>a</p>", true)
	e.createRaw(t, "priv", "code2", "<p>b</p>", false)
	e.createRaw(t, "gone", "code3", "<p>c</p>", true)
	c := e.comment(t, owner, "priv", http.StatusCreated, 0, "b", "owner note")

	// Anonymous: 401. Wrong or missing code, private file, trashed file: 404.
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/comments/pub?code=code1", "", nil)
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/comments/pub", "", reader)
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/comments/pub?code=nope", "", reader)
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/comments/priv?code=code2", "", reader)
	e.comment(t, reader, "priv?code=code2", http.StatusNotFound, 0, "b", "sneaky")
	e.expect(t, http.StatusNotFound, http.MethodDelete, fmt.Sprintf("/api/comments/priv/%d?code=code2", c.ID), "", reader)
	e.comment(t, reader, "pub?code=code1", http.StatusCreated, 0, "a", "fine")
	e.trash(t, owner, "gone")
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/comments/gone?code=code3", "", reader)
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/comments/gone", "", owner)

	// A comment id of another document is not reachable through this one.
	other := e.comment(t, owner, "pub", http.StatusCreated, 0, "a", "x")
	e.expect(t, http.StatusNotFound, http.MethodDelete, fmt.Sprintf("/api/comments/priv/%d", other.ID), "", owner)
	if got := e.comments(t, owner, "pub"); len(got) != 2 {
		t.Errorf("pub has %d comments, want 2", len(got))
	}
}

// Comments outlive a trashed file but not a permanently deleted one, nor their
// author's account; the hourly job removes them.
func TestCollectComments(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	owner := e.authCookie(t)
	author, authorCookie := e.newUser(t, "author")
	e.createRaw(t, "keep", "c1", "<p>1</p>", true)
	e.createRaw(t, "gone", "c2", "<p>2</p>", true)
	e.comment(t, owner, "keep", http.StatusCreated, 0, "1", "stays")
	e.comment(t, owner, "gone", http.StatusCreated, 0, "2", "goes with the file")
	e.comment(t, authorCookie, "keep?code=c1", http.StatusCreated, 0, "1", "goes with the author")

	e.trash(t, owner, "gone")
	if n, _, err := cron.CollectChats(ctx, e.conn); err != nil || n != 0 {
		t.Fatalf("CollectChats removed %d rows for a trashed file (err %v)", n, err)
	}
	e.expect(t, http.StatusNoContent, http.MethodDelete, "/api/files/gone/permanent", "", owner)
	e.expect(t, http.StatusOK, http.MethodDelete, fmt.Sprintf("/api/admin/users/%d", author.ID), "", owner)
	if n, _, err := cron.CollectChats(ctx, e.conn); err != nil || n != 2 {
		t.Errorf("CollectChats removed %d, want 2 (err %v)", n, err)
	}
	if got := e.comments(t, owner, "keep"); len(got) != 1 || got[0].Body != "stays" {
		t.Errorf("surviving comments = %+v", got)
	}
}

// "Explain" asks what a selected passage is, from the document alone, and
// stores the answer as an AI comment on that passage: shared like any comment,
// shown with an AI marker, deletable by the same people.
func TestExplainStoresAnAIComment(t *testing.T) {
	e := newEnv(t)
	owner := e.authCookie(t)
	_, reader := e.newUser(t, "reader")
	llm := newFakeLLM(t, "It is the project's codename.")
	e.configureAI(t, llm)
	e.insertFile(t, "doc", "markdown", "# Plan\n\nCodename Falcon ships in May.", "c", true)

	explain := func(cookie *http.Cookie, path, quote string) *http.Response {
		body, _ := json.Marshal(map[string]any{"quote": quote})
		return e.do(t, http.MethodPost, path, string(body), cookie)
	}
	var got struct {
		commentResp
		IsAI bool `json:"is_ai"`
	}
	if err := json.Unmarshal([]byte(bodyOf(t, explain(reader, "/api/comments/doc/explain?code=c", "Falcon"), http.StatusCreated)), &got); err != nil {
		t.Fatal(err)
	}
	if !got.IsAI || got.Quote != "Falcon" || got.Body != "It is the project's codename." || got.Author != "reader" || !got.Mine || got.ParentID != 0 {
		t.Fatalf("explanation = %+v", got)
	}

	// The model got the document, the selection and the "do not guess" rule,
	// and was not allowed to fall back on general knowledge.
	reqs := llm.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d upstream requests", len(reqs))
	}
	sys := reqs[0].Messages[0].Content
	if !strings.Contains(sys, "<selection>\nFalcon\n</selection>") || !strings.Contains(sys, "Do not guess") ||
		!strings.Contains(sys, "Codename Falcon ships in May.") || strings.Contains(sys, "general knowledge") {
		t.Errorf("explain system prompt = %q", sys)
	}

	// Everyone who may read the document sees it, flagged; the owner may delete it.
	seen := e.comments(t, owner, "doc")
	if len(seen) != 1 || seen[0].ID != got.ID || seen[0].CanDelete != true || seen[0].Mine {
		t.Fatalf("owner sees %+v", seen)
	}
	var listing struct {
		Comments []struct {
			IsAI bool `json:"is_ai"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(bodyOf(t, e.do(t, http.MethodGet, "/api/comments/doc", "", owner), http.StatusOK)), &listing); err != nil || len(listing.Comments) != 1 || !listing.Comments[0].IsAI {
		t.Errorf("listing = %+v (%v), want one AI comment", listing, err)
	}
	// A reply to it works like a reply to any top-level comment.
	e.comment(t, owner, "doc", http.StatusCreated, got.ID, "", "thanks")
	e.expect(t, http.StatusNoContent, http.MethodDelete, fmt.Sprintf("/api/comments/doc/%d", got.ID), "", owner)
	if left := e.comments(t, owner, "doc"); len(left) != 0 {
		t.Errorf("after deleting the explanation %+v remain", left)
	}

	// Validation and access: nothing reaches the model.
	e.expectStatus(t, explain(reader, "/api/comments/doc/explain?code=c", "  "), http.StatusBadRequest)
	e.expectStatus(t, explain(reader, "/api/comments/doc/explain?code=c", strings.Repeat("x", 2001)), http.StatusRequestEntityTooLarge)
	e.expectStatus(t, explain(reader, "/api/comments/doc/explain", "Falcon"), http.StatusNotFound)
	e.expectStatus(t, explain(nil, "/api/comments/doc/explain?code=c", "Falcon"), http.StatusUnauthorized)
	if n := len(llm.requests()); n != 1 {
		t.Errorf("a rejected request reached the model (%d upstream requests)", n)
	}
}

// A model that is not configured, refuses, or says nothing leaves no comment.
func TestExplainStoresNothingOnFailure(t *testing.T) {
	e := newEnv(t)
	owner := e.authCookie(t)
	e.insertFile(t, "doc", "markdown", "# Plan", "c", false)
	body := `{"quote":"Plan"}`

	e.expect(t, http.StatusConflict, http.MethodPost, "/api/comments/doc/explain", body, owner) // no model yet

	llm := newFakeLLM(t, "ok")
	e.configureAI(t, llm)
	llm.mu.Lock()
	llm.status = http.StatusUnauthorized
	llm.mu.Unlock()
	if msg := bodyOf(t, e.do(t, http.MethodPost, "/api/comments/doc/explain", body, owner), http.StatusBadGateway); !strings.Contains(msg, "Incorrect API key") {
		t.Errorf("the provider's refusal was not passed on: %q", msg)
	}

	llm.mu.Lock()
	llm.status, llm.reply = http.StatusOK, ""
	llm.mu.Unlock()
	e.expect(t, http.StatusBadGateway, http.MethodPost, "/api/comments/doc/explain", body, owner) // empty answer

	if got := e.comments(t, owner, "doc"); len(got) != 0 {
		t.Errorf("failed explanations left comments: %+v", got)
	}
}
