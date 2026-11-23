package server_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/cron"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// fakeLLM is an OpenAI-compatible chat completions endpoint: it streams
// `reply` in a few chunks, or answers `status` with an error body.
type fakeLLM struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []fakeLLMRequest
	auth   []string
	reply  string
	status int
}

type fakeLLMRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func newFakeLLM(t *testing.T, reply string) *fakeLLM {
	t.Helper()
	f := &fakeLLM{reply: reply, status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req fakeLLMRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		status, reply := f.status, f.reply
		f.mu.Unlock()
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keep-alive\n\n")
		for _, chunk := range strings.SplitAfter(reply, " ") {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": chunk}}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLLM) requests() []fakeLLMRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeLLMRequest(nil), f.reqs...)
}

// configureAI points the server at the fake model.
func (e *testEnv) configureAI(t *testing.T, llm *fakeLLM) {
	t.Helper()
	e.setConfig(t, "ai_base_url", llm.srv.URL+"/v1")
	e.setConfig(t, "ai_model", "test-model")
	e.setConfig(t, "ai_api_key", "sk-test-0123456789abcd")
}

type chatEvent struct {
	Type         string       `json:"type"`
	Text         string       `json:"text"`
	Error        string       `json:"error"`
	Conversation *convResp    `json:"conversation"`
	Message      *chatMsgResp `json:"message"`
}

type convResp struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type chatMsgResp struct {
	ID      int64  `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ask sends one question and reads the whole event stream.
func (e *testEnv) ask(t *testing.T, cookie *http.Cookie, slugAndQuery string, convID int64, content string) []chatEvent {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"conversation_id": convID, "content": content})
	resp := e.do(t, http.MethodPost, "/api/chat/"+pathWithQuery(slugAndQuery, "/messages"), string(body), cookie)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("ask: status %d: %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("the stream does not turn off proxy buffering")
	}
	var events []chatEvent
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev chatEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("event %q: %v", sc.Text(), err)
		}
		events = append(events, ev)
	}
	return events
}

// pathWithQuery inserts a sub-path between "slug" and "?code=..." .
func pathWithQuery(slugAndQuery, sub string) string {
	slug, query, found := strings.Cut(slugAndQuery, "?")
	if !found {
		return slug + sub
	}
	return slug + sub + "?" + query
}

func TestChatStreamsAndStoresConversation(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	llm := newFakeLLM(t, "The answer is forty two.")
	e.configureAI(t, llm)
	e.insertFile(t, "doc", "markdown", "# Guide\n\nThe magic number is 42.", "c", false)

	events := e.ask(t, cookie, "doc", 0, "  What is the magic number?  ")
	if len(events) < 3 || events[0].Type != "start" || events[len(events)-1].Type != "done" {
		t.Fatalf("events = %+v, want start, deltas, done", events)
	}
	var streamed strings.Builder
	deltas := 0
	for _, ev := range events[1 : len(events)-1] {
		if ev.Type != "delta" {
			t.Fatalf("unexpected %q event mid-stream", ev.Type)
		}
		deltas++
		streamed.WriteString(ev.Text)
	}
	if deltas < 2 {
		t.Errorf("the answer arrived in %d delta(s); it should stream in pieces", deltas)
	}
	if streamed.String() != "The answer is forty two." {
		t.Errorf("streamed %q", streamed.String())
	}
	conv := events[0].Conversation
	if conv == nil || conv.Title != "What is the magic number?" {
		t.Fatalf("conversation = %+v, want one titled after the question", conv)
	}
	if done := events[len(events)-1].Message; done == nil || done.Content != "The answer is forty two." {
		t.Errorf("done carries %+v, want the stored answer", done)
	}

	// The model saw the whole document and the question, with the key.
	reqs := llm.requests()
	if len(reqs) != 1 || !reqs[0].Stream || reqs[0].Model != "test-model" {
		t.Fatalf("upstream requests = %+v", reqs)
	}
	if got := reqs[0].Messages; len(got) != 2 || got[0].Role != "system" ||
		!strings.Contains(got[0].Content, "The magic number is 42.") || got[1].Content != "What is the magic number?" {
		t.Errorf("upstream messages = %+v", got)
	}
	if llm.auth[0] != "Bearer sk-test-0123456789abcd" {
		t.Errorf("Authorization = %q", llm.auth[0])
	}

	// A follow-up continues the same conversation, with its history.
	llm.reply = "Yes."
	e.ask(t, cookie, "doc", conv.ID, "Are you sure?")
	reqs = llm.requests()
	if got := reqs[1].Messages; len(got) != 4 || got[1].Role != "user" || got[2].Role != "assistant" ||
		got[2].Content != "The answer is forty two." || got[3].Content != "Are you sure?" {
		t.Errorf("follow-up messages = %+v", got)
	}

	// Both turns are stored, and listed.
	detail := decodeJSON[struct {
		Messages []chatMsgResp `json:"messages"`
	}](t, e.do(t, http.MethodGet, fmt.Sprintf("/api/chat/doc/conversations/%d", conv.ID), "", cookie), http.StatusOK)
	want := []string{"user:What is the magic number?", "assistant:The answer is forty two.", "user:Are you sure?", "assistant:Yes."}
	if len(detail.Messages) != len(want) {
		t.Fatalf("stored %d messages, want %d", len(detail.Messages), len(want))
	}
	for i, m := range detail.Messages {
		if got := m.Role + ":" + m.Content; got != want[i] {
			t.Errorf("message %d = %q, want %q", i, got, want[i])
		}
	}

	// A second conversation about the same document is separate.
	e.ask(t, cookie, "doc", 0, "Another question")
	overview := decodeJSON[struct {
		File struct {
			Owner bool `json:"owner"`
		} `json:"file"`
		AIEnabled     bool       `json:"ai_enabled"`
		Conversations []convResp `json:"conversations"`
	}](t, e.do(t, http.MethodGet, "/api/chat/doc", "", cookie), http.StatusOK)
	if !overview.AIEnabled || !overview.File.Owner || len(overview.Conversations) != 2 ||
		overview.Conversations[0].Title != "Another question" {
		t.Errorf("overview = %+v, want two conversations, newest first", overview)
	}

	// Deleting one takes its messages with it.
	e.expect(t, http.StatusNoContent, http.MethodDelete, fmt.Sprintf("/api/chat/doc/conversations/%d", conv.ID), "", cookie)
	e.expect(t, http.StatusNotFound, http.MethodGet, fmt.Sprintf("/api/chat/doc/conversations/%d", conv.ID), "", cookie)
	var left int
	if err := e.conn.QueryRow(`SELECT COUNT(*) FROM chat_messages WHERE conversation_id = ?`, conv.ID).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d messages of the deleted conversation remain (err %v)", left, err)
	}
}

// A refusal from the provider -- a bad key, an unknown model -- is reported
// with the provider's message, and the question is not stored: the turn did
// not happen.
func TestChatUpstreamRefusalStoresNothing(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	llm := newFakeLLM(t, "unused")
	llm.status = http.StatusUnauthorized
	e.configureAI(t, llm)
	e.createRaw(t, "doc", "c", "<p>x</p>", false)

	resp := e.do(t, http.MethodPost, "/api/chat/doc/messages", `{"content":"hi"}`, cookie)
	body := bodyOf(t, resp, http.StatusBadGateway)
	if !strings.Contains(body, "Incorrect API key provided") || !strings.Contains(body, "401") {
		t.Errorf("error = %q, want the provider's status and message", body)
	}
	var n int
	if err := e.conn.QueryRow(`SELECT (SELECT COUNT(*) FROM chat_conversations) + (SELECT COUNT(*) FROM chat_messages)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d chat rows stored after a refusal (err %v)", n, err)
	}
}

func TestChatRequiresConfigAndValidQuestion(t *testing.T) {
	e := newEnv(t)
	cookie := e.authCookie(t)
	e.createRaw(t, "doc", "c", "<p>x</p>", false)

	e.expect(t, http.StatusConflict, http.MethodPost, "/api/chat/doc/messages", `{"content":"hi"}`, cookie)
	e.configureAI(t, newFakeLLM(t, "ok"))
	e.expect(t, http.StatusBadRequest, http.MethodPost, "/api/chat/doc/messages", `{"content":"   "}`, cookie)
	big, _ := json.Marshal(map[string]string{"content": strings.Repeat("a", 65<<10)})
	e.expect(t, http.StatusRequestEntityTooLarge, http.MethodPost, "/api/chat/doc/messages", string(big), cookie)
	e.expect(t, http.StatusUnauthorized, http.MethodPost, "/api/chat/doc/messages", `{"content":"hi"}`, nil)
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/api/chat/doc", "", nil)
}

// Asking about a document is allowed to whoever may read it -- the owner, or
// a signed-in holder of a public file's code -- and conversations are private
// to the account that had them.
func TestChatAccessAndIsolation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	adminCookie := e.authCookie(t)
	_, otherCookie := e.newUser(t, "other")
	e.configureAI(t, newFakeLLM(t, "ok"))
	e.createRaw(t, "private", "pc", "<p>secret</p>", false)
	e.createRaw(t, "shared", "sc", "<p>public</p>", true)

	// The owner's conversation about the shared document.
	adminConv := e.ask(t, adminCookie, "shared", 0, "admin question")[0].Conversation

	for _, path := range []string{
		"/api/chat/private",           // someone else's private file
		"/api/chat/private?code=pc",   // ...even with its code
		"/api/chat/shared",            // public, but no code
		"/api/chat/shared?code=wrong", // public, wrong code
		"/api/chat/missing?code=sc",   // no such file
		fmt.Sprintf("/api/chat/shared/conversations/%d?code=sc", adminConv.ID), // someone else's conversation
	} {
		e.expect(t, http.StatusNotFound, http.MethodGet, path, "", otherCookie)
	}
	e.expect(t, http.StatusNotFound, http.MethodDelete,
		fmt.Sprintf("/api/chat/shared/conversations/%d?code=sc", adminConv.ID), "", otherCookie)
	// Continuing someone else's conversation is a 404 too, not a new one.
	resp := e.do(t, http.MethodPost, "/api/chat/shared/messages?code=sc",
		fmt.Sprintf(`{"conversation_id":%d,"content":"hijack"}`, adminConv.ID), otherCookie)
	bodyOf(t, resp, http.StatusNotFound)

	// With the code, the other account can ask, and sees only its own.
	e.ask(t, otherCookie, "shared?code=sc", 0, "other question")
	overview := decodeJSON[struct {
		File struct {
			Owner bool `json:"owner"`
		} `json:"file"`
		Conversations []convResp `json:"conversations"`
	}](t, e.do(t, http.MethodGet, "/api/chat/shared?code=sc", "", otherCookie), http.StatusOK)
	if overview.File.Owner || len(overview.Conversations) != 1 || overview.Conversations[0].Title != "other question" {
		t.Errorf("other's overview = %+v, want only their own conversation", overview)
	}
	adminView := decodeJSON[struct {
		Conversations []convResp `json:"conversations"`
	}](t, e.do(t, http.MethodGet, "/api/chat/shared", "", adminCookie), http.StatusOK)
	if len(adminView.Conversations) != 1 || adminView.Conversations[0].ID != adminConv.ID {
		t.Errorf("admin's overview = %+v, want only their own conversation", adminView)
	}

	// A public file past its deadline is offline for a code holder...
	if _, err := e.queries.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{
		ExpiresAt: sql.NullTime{Time: time.Now().Add(-time.Minute), Valid: true},
		Slug:      "shared",
		UserID:    e.admin.ID,
	}); err != nil {
		t.Fatalf("SetFileExpiry: %v", err)
	}
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/chat/shared?code=sc", "", otherCookie)
	// ...but not for its owner. A trashed file is gone for everyone.
	e.expect(t, http.StatusOK, http.MethodGet, "/api/chat/shared", "", adminCookie)
	e.trash(t, adminCookie, "shared")
	e.expect(t, http.StatusNotFound, http.MethodGet, "/api/chat/shared", "", adminCookie)
}

func TestAISettingsAreSuperAdminOnly(t *testing.T) {
	e := newEnv(t)
	adminCookie := e.authCookie(t)
	_, userCookie := e.newUser(t, "plain")
	key := e.enableMCP(t)

	e.expect(t, http.StatusForbidden, http.MethodGet, "/api/settings/ai", "", userCookie)
	e.expect(t, http.StatusForbidden, http.MethodPut, "/api/settings/ai", `{"model":"x"}`, userCookie)
	// The super admin's own API key is a file-scope credential, as everywhere.
	resp := e.doKey(t, http.MethodGet, "/api/settings/ai", "", key)
	bodyOf(t, resp, http.StatusForbidden)

	e.expect(t, http.StatusBadRequest, http.MethodPut, "/api/settings/ai", `{"base_url":"ftp://x"}`, adminCookie)
	got := decodeJSON[map[string]any](t, e.do(t, http.MethodPut, "/api/settings/ai",
		`{"base_url":" https://api.example.com/v1/ ","model":"gpt-x","api_key":"sk-secret-abcdefgh1234"}`, adminCookie), http.StatusOK)
	if got["base_url"] != "https://api.example.com/v1" || got["model"] != "gpt-x" ||
		got["api_key_set"] != true || got["api_key_hint"] != "1234" {
		t.Errorf("AI settings = %v", got)
	}
	if _, leaked := got["api_key"]; leaked {
		t.Error("the API key itself was returned")
	}

	// Saving without api_key keeps the stored one.
	e.expect(t, http.StatusOK, http.MethodPut, "/api/settings/ai", `{"model":"gpt-y"}`, adminCookie)
	if v, _ := e.queries.GetConfig(context.Background(), "ai_api_key"); v != "sk-secret-abcdefgh1234" {
		t.Errorf("stored key = %q after a save without api_key", v)
	}

	// Everyone learns whether Ask AI is on, and nothing else.
	settings := bodyOf(t, e.do(t, http.MethodGet, "/api/settings", "", userCookie), http.StatusOK)
	if !strings.Contains(settings, `"ai_enabled":true`) || strings.Contains(settings, "sk-secret") ||
		strings.Contains(settings, "api.example.com") {
		t.Errorf("GET /api/settings = %s", settings)
	}
	e.expect(t, http.StatusOK, http.MethodPut, "/api/settings/ai", `{"model":""}`, adminCookie)
	if s := bodyOf(t, e.do(t, http.MethodGet, "/api/settings", "", userCookie), http.StatusOK); !strings.Contains(s, `"ai_enabled":false`) {
		t.Errorf("clearing the model left Ask AI on: %s", s)
	}
}

// The /res button is offered to signed-in readers only, and only while a
// model is configured; see askai.go.
func TestAskAIButtonOnRenderedPages(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	adminCookie := e.authCookie(t)
	_, otherCookie := e.newUser(t, "other")
	e.createRaw(t, "doc", "code1", "<html><body><p>x</p></body></html>", true)
	e.createRaw(t, "once", "code2", "<p>y</p>", true)
	if _, err := e.queries.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{MaxViews: sqlNullInt64(5), Slug: "once", UserID: e.admin.ID}); err != nil {
		t.Fatalf("SetFileExpiry: %v", err)
	}

	page := func(path string, cookie *http.Cookie) string {
		t.Helper()
		return bodyOf(t, e.do(t, http.MethodGet, path, "", cookie), http.StatusOK)
	}
	if strings.Contains(page("/res/doc", adminCookie), "renderbin-ask-ai") {
		t.Error("button shown while no model is configured")
	}

	e.configureAI(t, newFakeLLM(t, "ok"))
	if p := page("/res/doc", adminCookie); !strings.Contains(p, "renderbin-ask-ai") || !strings.Contains(p, `("/view/doc")`) {
		t.Error("the owner gets no button, or one with a code they do not need")
	}
	if p := page("/res/doc?code=code1", otherCookie); !strings.Contains(p, `("/view/doc?code=code1")`) {
		t.Error("a signed-in code holder gets no button carrying the code")
	}
	if strings.Contains(page("/res/doc?code=code1", nil), "renderbin-ask-ai") {
		t.Error("an anonymous visitor got the button")
	}
	if strings.Contains(page("/res/once?code=code2", otherCookie), "renderbin-ask-ai") {
		t.Error("a view-limited file offered the button to a non-owner")
	}
	if !strings.Contains(page("/res/once", adminCookie), "renderbin-ask-ai") {
		t.Error("a view-limited file hid the button from its owner")
	}
}

// Conversations outlive a trashed file but not a permanently deleted one, nor
// a deleted account; the hourly job removes them.
func TestCollectChats(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cookie := e.authCookie(t)
	other, otherCookie := e.newUser(t, "other")
	e.configureAI(t, newFakeLLM(t, "ok"))
	e.createRaw(t, "keep", "c1", "<p>1</p>", true)
	e.createRaw(t, "gone", "c2", "<p>2</p>", false)
	e.ask(t, cookie, "keep", 0, "q1")
	e.ask(t, cookie, "gone", 0, "q2")
	e.ask(t, otherCookie, "keep?code=c1", 0, "q3")

	e.trash(t, cookie, "gone")
	if convs, _, err := cron.CollectChats(ctx, e.conn); err != nil || convs != 0 {
		t.Fatalf("CollectChats removed %d conversations of a trashed file (err %v)", convs, err)
	}
	e.expect(t, http.StatusNoContent, http.MethodDelete, "/api/files/gone/permanent", "", cookie)
	e.expect(t, http.StatusOK, http.MethodDelete, fmt.Sprintf("/api/admin/users/%d", other.ID), "", cookie)

	convs, msgs, err := cron.CollectChats(ctx, e.conn)
	if err != nil || convs != 2 || msgs != 4 {
		t.Errorf("CollectChats = %d conversations, %d messages, %v; want 2, 4", convs, msgs, err)
	}
	var left int
	if err := e.conn.QueryRow(`SELECT COUNT(*) FROM chat_messages`).Scan(&left); err != nil || left != 2 {
		t.Errorf("%d messages left, want the 2 of the surviving conversation", left)
	}
}
