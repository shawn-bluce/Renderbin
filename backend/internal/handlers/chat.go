package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
	"github.com/shawn-bluce/renderbin/backend/internal/site"
)

// ChatHandler serves "Ask AI" at /api/chat/{slug}/...: a signed-in reader's
// conversations with the configured model about one document.
//
// Who may ask about a document is who may read it (GetChatFile): its owner, or
// anyone signed in who holds the access code of a public file, passed as
// ?code= exactly as on /res. Conversations are private to the account that
// had them -- every conversation and message query is scoped by user_id, so
// two readers of the same document never see each other's.
//
// It is reached from the app's /view/{slug} page, never from the sandboxed
// document itself: that page runs in an opaque origin with no cookie, and the
// document's own scripts can read anything rendered there. See
// injectAskAIWidget for the button that leads from one to the other.
type ChatHandler struct {
	queries *sqlcgen.Queries
	conn    *sql.DB
	blobs   *blobstore.Store
	logger  *slog.Logger
	client  *http.Client
}

func NewChatHandler(queries *sqlcgen.Queries, conn *sql.DB, blobs *blobstore.Store, logger *slog.Logger) *ChatHandler {
	return &ChatHandler{queries: queries, conn: conn, blobs: blobs, logger: logger, client: newLLMClient()}
}

// maxChatMessageBytes bounds one question. The document is what is large, and
// it is read from the store, not sent by the client.
const maxChatMessageBytes = 64 << 10

// chatTitleRunes is how much of the first question names a conversation.
const chatTitleRunes = 40

type chatFileResponse struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Owner bool   `json:"owner"`
}

type conversationResponse struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type chatMessageResponse struct {
	ID        int64  `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

func toConversation(c sqlcgen.ChatConversation) conversationResponse {
	return conversationResponse{
		ID: c.ID, Title: c.Title,
		CreatedAt: c.CreatedAt.Format(timeLayout), UpdatedAt: c.UpdatedAt.Format(timeLayout),
	}
}

func toChatMessage(m sqlcgen.ChatMessage) chatMessageResponse {
	return chatMessageResponse{ID: m.ID, Role: m.Role, Content: m.Content, CreatedAt: m.CreatedAt.Format(timeLayout)}
}

// chatFile resolves {slug} (plus ?code=) to a file the caller may read, or
// answers 404 -- the same answer whether the file is missing, someone else's
// and private, or the code is wrong.
func (h *ChatHandler) chatFile(w http.ResponseWriter, r *http.Request, user sqlcgen.User) (sqlcgen.File, bool) {
	file, err := h.queries.GetChatFile(r.Context(), sqlcgen.GetChatFileParams{
		Slug:       chi.URLParam(r, "slug"),
		ViewerID:   user.ID,
		AccessCode: r.URL.Query().Get("code"),
	})
	if err != nil {
		queryError(w, h.logger, "get chat file", err)
		return sqlcgen.File{}, false
	}
	return file, true
}

// conversationID parses {id}; anything unparsable is a missing conversation.
func conversationID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		notFound(w)
		return 0, false
	}
	return id, true
}

type chatOverviewResponse struct {
	File          chatFileResponse       `json:"file"`
	AIEnabled     bool                   `json:"ai_enabled"`
	Conversations []conversationResponse `json:"conversations"`
}

// Overview answers GET /api/chat/{slug}: the document (for the page's title
// and its frame) and the caller's conversations about it, newest first.
func (h *ChatHandler) Overview(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := h.chatFile(w, r, user)
	if !ok {
		return
	}
	convs, err := h.queries.ListChatConversations(r.Context(), sqlcgen.ListChatConversationsParams{
		UserID: user.ID, FileID: file.ID,
	})
	if err != nil {
		serverError(w, h.logger, "list conversations", err)
		return
	}
	resp := chatOverviewResponse{
		File:          chatFileResponse{Slug: file.Slug, Name: file.Name, Kind: file.Kind, Owner: file.UserID == user.ID},
		AIEnabled:     aiEnabled(r.Context(), h.queries),
		Conversations: make([]conversationResponse, 0, len(convs)),
	}
	for _, c := range convs {
		resp.Conversations = append(resp.Conversations, toConversation(c))
	}
	writeJSON(w, resp)
}

type conversationDetailResponse struct {
	Conversation conversationResponse  `json:"conversation"`
	Messages     []chatMessageResponse `json:"messages"`
}

// Conversation answers GET /api/chat/{slug}/conversations/{id}.
func (h *ChatHandler) Conversation(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := h.chatFile(w, r, user)
	if !ok {
		return
	}
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	conv, err := h.queries.GetChatConversation(r.Context(), sqlcgen.GetChatConversationParams{
		ID: id, UserID: user.ID, FileID: file.ID,
	})
	if err != nil {
		queryError(w, h.logger, "get conversation", err)
		return
	}
	msgs, err := h.queries.ListChatMessages(r.Context(), sqlcgen.ListChatMessagesParams{
		ConversationID: conv.ID, UserID: user.ID,
	})
	if err != nil {
		serverError(w, h.logger, "list messages", err)
		return
	}
	resp := conversationDetailResponse{Conversation: toConversation(conv), Messages: make([]chatMessageResponse, 0, len(msgs))}
	for _, m := range msgs {
		resp.Messages = append(resp.Messages, toChatMessage(m))
	}
	writeJSON(w, resp)
}

// DeleteConversation answers DELETE /api/chat/{slug}/conversations/{id}.
func (h *ChatHandler) DeleteConversation(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := h.chatFile(w, r, user)
	if !ok {
		return
	}
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	tx, err := h.conn.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, h.logger, "begin delete conversation", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	q := h.queries.WithTx(tx)
	n, err := q.DeleteChatConversation(r.Context(), sqlcgen.DeleteChatConversationParams{
		ID: id, UserID: user.ID, FileID: file.ID,
	})
	if err != nil {
		serverError(w, h.logger, "delete conversation", err)
		return
	}
	if n == 0 {
		notFound(w)
		return
	}
	// Only reached once the owner-scoped delete above matched.
	if err := q.DeleteChatMessagesOf(r.Context(), id); err != nil {
		serverError(w, h.logger, "delete conversation messages", err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, h.logger, "commit delete conversation", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type sendMessageRequest struct {
	// ConversationID continues a conversation; absent or 0 starts a new one,
	// created only once the model has accepted the question.
	ConversationID int64  `json:"conversation_id"`
	Content        string `json:"content"`
}

// Send answers POST /api/chat/{slug}/messages: it asks the model, with the
// whole document and the conversation so far as context, and streams the
// answer back as it is written.
//
// Everything that can fail before the model starts answering -- a bad request,
// AI not configured, the provider refusing the request -- is an ordinary HTTP
// error and stores nothing, so the question simply did not happen. Once the
// provider has accepted it, the question is stored and the response is a 200
// stream of newline-delimited JSON events:
//
//	{"type":"start","conversation":{...},"message":{...the stored question}}
//	{"type":"delta","text":"..."}            (repeated)
//	{"type":"done","message":{...the stored answer} | null}
//	{"type":"error","error":"...","message":{...partial answer} | null}
//
// Whatever was written is stored, also when the stream fails midway or the
// reader stops it (closes the connection): the conversation then shows the
// answer as far as it got, which is what the reader saw.
func (h *ChatHandler) Send(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := h.chatFile(w, r, user)
	if !ok {
		return
	}
	var req sendMessageRequest
	if !decodeJSON(w, r, &req, 2*maxChatMessageBytes+4<<10) {
		return
	}
	question := strings.TrimSpace(req.Content)
	switch {
	case question == "":
		http.Error(w, "content must not be empty", http.StatusBadRequest)
		return
	case len(question) > maxChatMessageBytes:
		http.Error(w, fmt.Sprintf("a message is limited to %d KB", maxChatMessageBytes>>10), http.StatusRequestEntityTooLarge)
		return
	case !utf8.ValidString(question):
		http.Error(w, "content must be UTF-8", http.StatusBadRequest)
		return
	}
	cfg := loadAIConfig(r.Context(), h.queries)
	if !cfg.ready() {
		http.Error(w, "AI is not configured on this server", http.StatusConflict)
		return
	}

	// The conversation so far, when continuing one.
	var conv *sqlcgen.ChatConversation
	var history []sqlcgen.ChatMessage
	if req.ConversationID != 0 {
		c, err := h.queries.GetChatConversation(r.Context(), sqlcgen.GetChatConversationParams{
			ID: req.ConversationID, UserID: user.ID, FileID: file.ID,
		})
		if err != nil {
			queryError(w, h.logger, "get conversation", err)
			return
		}
		conv = &c
		history, err = h.queries.ListChatMessages(r.Context(), sqlcgen.ListChatMessagesParams{
			ConversationID: c.ID, UserID: user.ID,
		})
		if err != nil {
			serverError(w, h.logger, "list messages", err)
			return
		}
	}

	doc, err := h.documentText(file)
	if err != nil {
		serverError(w, h.logger, "read document for chat", err, "slug", file.Slug)
		return
	}
	messages := make([]llmMessage, 0, len(history)+2)
	messages = append(messages, llmMessage{Role: "system", Content: chatSystemPrompt(file, doc)})
	for _, m := range history {
		messages = append(messages, llmMessage{Role: m.Role, Content: m.Content})
	}
	messages = append(messages, llmMessage{Role: "user", Content: question})

	stream, err := openLLMStream(r.Context(), h.client, cfg, messages)
	if err != nil {
		if ue, ok := isUpstream(err); ok {
			http.Error(w, ue.Error(), http.StatusBadGateway)
			return
		}
		if r.Context().Err() != nil {
			return // the reader went away
		}
		h.logger.Warn("ask AI", "error", err)
		http.Error(w, "could not reach the AI service: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer stream.Close()

	// The provider accepted the question: from here on it is part of the
	// conversation. The writes use a context that outlives the request, so a
	// reader who stops the answer still gets what was written stored.
	store := context.WithoutCancel(r.Context())
	if conv == nil {
		c, err := h.queries.CreateChatConversation(store, sqlcgen.CreateChatConversationParams{
			UserID: user.ID, FileID: file.ID, Title: chatTitle(question),
		})
		if err != nil {
			serverError(w, h.logger, "create conversation", err)
			return
		}
		conv = &c
	}
	asked, err := h.queries.InsertChatMessage(store, sqlcgen.InsertChatMessageParams{
		ConversationID: conv.ID, Role: "user", Content: question, UserID: user.ID,
	})
	if err != nil {
		serverError(w, h.logger, "store question", err)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// Nginx buffers proxied responses by default, which would hold the whole
	// answer back until it is complete; this header turns that off per response.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	emit := func(v any) {
		// A write error means the reader is gone; the request context is then
		// cancelled and the stream read below fails, which ends the loop.
		_ = enc.Encode(v)
		_ = rc.Flush()
	}
	emit(map[string]any{"type": "start", "conversation": toConversation(*conv), "message": toChatMessage(asked)})

	var answer strings.Builder
	var streamErr error
	for {
		delta, done, err := stream.next()
		if delta != "" {
			answer.WriteString(delta)
			emit(map[string]any{"type": "delta", "text": delta})
		}
		if err != nil {
			streamErr = err
			break
		}
		if done {
			break
		}
	}

	var saved *chatMessageResponse
	if answer.Len() > 0 {
		m, err := h.queries.InsertChatMessage(store, sqlcgen.InsertChatMessageParams{
			ConversationID: conv.ID, Role: "assistant", Content: answer.String(), UserID: user.ID,
		})
		if err != nil {
			h.logger.Error("store answer", "error", err)
		} else {
			resp := toChatMessage(m)
			saved = &resp
		}
	}
	if err := h.queries.TouchChatConversation(store, sqlcgen.TouchChatConversationParams{ID: conv.ID, UserID: user.ID}); err != nil {
		h.logger.Warn("touch conversation", "error", err)
	}

	switch {
	case r.Context().Err() != nil:
		// Stopped by the reader; nobody is listening for a final event.
	case streamErr != nil:
		msg := "the answer was cut off"
		if ue, ok := isUpstream(streamErr); ok {
			msg = ue.Error()
		} else {
			h.logger.Warn("ask AI stream", "error", streamErr)
		}
		emit(map[string]any{"type": "error", "error": msg, "message": saved})
	default:
		emit(map[string]any{"type": "done", "message": saved})
	}
}

// chatTitle names a conversation after its first question.
func chatTitle(question string) string {
	return shorten(strings.Join(strings.Fields(question), " "), chatTitleRunes)
}

// documentText is the document as the model sees it: its stored source --
// Markdown, HTML or text, exactly as uploaded -- or, for a site, every text
// entry under its path. Binary entries (images, fonts) are left out.
func (h *ChatHandler) documentText(file sqlcgen.File) (string, error) {
	data, err := readContent(h.blobs, file)
	if err != nil {
		return "", err
	}
	if file.Kind != KindSite {
		return strings.ToValidUTF8(string(data), "�"), nil
	}
	arc, err := site.OpenBytes(data)
	if err != nil {
		return "", fmt.Errorf("open site archive: %w", err)
	}
	entries, err := arc.Entries()
	if err != nil {
		return "", fmt.Errorf("read site archive: %w", err)
	}
	var b strings.Builder
	for _, e := range entries {
		if !utf8.Valid(e.Data) || strings.ContainsRune(string(e.Data), 0) {
			continue
		}
		fmt.Fprintf(&b, "=== %s ===\n%s\n\n", e.Path, e.Data)
	}
	return b.String(), nil
}

// chatSystemPrompt puts the whole document in front of the conversation.
func chatSystemPrompt(file sqlcgen.File, doc string) string {
	format := map[string]string{
		KindHTML:     "HTML source",
		KindMarkdown: "Markdown source",
		KindText:     "plain text",
		KindSite:     "a multi-page website; each file is introduced by a line \"=== path ===\"",
	}[file.Kind]
	return "You are a helpful assistant. The user is reading the document below and is asking you about it. " +
		"Answer based on the document. When a question goes beyond what the document says, say so, then answer from general knowledge. " +
		"Reply in the language the user writes in, and format replies in Markdown.\n\n" +
		"Document title: " + file.Name + "\n" +
		"Format: " + format + "\n\n" +
		"<document>\n" + doc + "\n</document>"
}
