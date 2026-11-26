package handlers

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// CommentsHandler serves comments on a document at /api/comments/{slug}/...:
// a reader selects a passage and writes a note about it, and everyone who may
// read the document sees the notes.
//
// Who may read the document is who may comment on it, decided exactly as for
// "Ask AI" (readableFile): its owner, or a signed-in holder of a public file's
// access code, passed as ?code=. Comments are shared among those readers, which
// is why the queries are keyed by file and not by user; the file lookup is what
// guards them. A comment can be deleted by its author and by the document's
// owner -- the owner decides what stays on their document -- and deleting a
// top-level comment takes its replies with it. There is no editing.
//
// A comment can also be an AI explanation of its quoted passage (Explain): the
// model's answer, stored like any comment, authored by the reader who asked and
// marked is_ai so it is shown with an AI badge.
//
// Like the chat, it is reached from the app's /view/{slug} page, never from the
// sandboxed document itself.
type CommentsHandler struct {
	queries *sqlcgen.Queries
	conn    *sql.DB
	blobs   *blobstore.Store
	logger  *slog.Logger
	client  *http.Client
}

func NewCommentsHandler(queries *sqlcgen.Queries, conn *sql.DB, blobs *blobstore.Store, logger *slog.Logger) *CommentsHandler {
	return &CommentsHandler{queries: queries, conn: conn, blobs: blobs, logger: logger, client: newLLMClient()}
}

const (
	// maxQuoteBytes bounds the passage a comment quotes (and the passage "Ask
	// AI" is asked to explain). The page's script cuts a selection to 500
	// characters, which is at most 2000 bytes; this is the same limit enforced
	// where it counts.
	maxQuoteBytes = 2000
	// maxCommentBytes bounds the text of one comment.
	maxCommentBytes = 8 << 10
)

type commentResponse struct {
	ID int64 `json:"id"`
	// ParentID is 0 for a top-level comment.
	ParentID int64  `json:"parent_id"`
	Quote    string `json:"quote"`
	Body     string `json:"body"`
	// IsAI: the body is the model's explanation of the quote, requested by Author.
	IsAI   bool   `json:"is_ai"`
	Author string `json:"author"`
	// Mine: the caller wrote it. CanDelete: the caller wrote it or owns the
	// document.
	Mine      bool   `json:"mine"`
	CanDelete bool   `json:"can_delete"`
	CreatedAt string `json:"created_at"`
}

func authorName(nickname, username string) string {
	if n := strings.TrimSpace(nickname); n != "" {
		return n
	}
	return username
}

func toComment(c sqlcgen.FileComment, nickname, username string, viewer, fileOwner int64) commentResponse {
	return commentResponse{
		ID: c.ID, ParentID: c.ParentID.Int64, Quote: c.Quote, Body: c.Body, IsAI: c.IsAi,
		Author:    authorName(nickname, username),
		Mine:      c.UserID == viewer,
		CanDelete: c.UserID == viewer || fileOwner == viewer,
		CreatedAt: c.CreatedAt.Format(timeLayout),
	}
}

// List answers GET /api/comments/{slug}: every comment on the document, oldest
// first, replies included (they carry their parent_id).
func (h *CommentsHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := readableFile(w, r, h.queries, h.logger, user)
	if !ok {
		return
	}
	rows, err := h.queries.ListFileComments(r.Context(), file.ID)
	if err != nil {
		serverError(w, h.logger, "list comments", err)
		return
	}
	out := make([]commentResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, commentResponse{
			ID: row.ID, ParentID: row.ParentID.Int64, Quote: row.Quote, Body: row.Body, IsAI: row.IsAi,
			Author:    authorName(row.AuthorNickname, row.AuthorUsername),
			Mine:      row.UserID == user.ID,
			CanDelete: row.UserID == user.ID || file.UserID == user.ID,
			CreatedAt: row.CreatedAt.Format(timeLayout),
		})
	}
	writeJSON(w, map[string]any{"comments": out})
}

type createCommentRequest struct {
	// ParentID replies to a top-level comment; 0 or absent starts a new one.
	ParentID int64 `json:"parent_id"`
	// Quote is the selected passage a top-level comment is about; a reply has
	// none (it is about its parent).
	Quote string `json:"quote"`
	Body  string `json:"body"`
}

// Create answers POST /api/comments/{slug}.
func (h *CommentsHandler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := readableFile(w, r, h.queries, h.logger, user)
	if !ok {
		return
	}
	var req createCommentRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}
	body := strings.TrimSpace(req.Body)
	quote := strings.TrimSpace(req.Quote)
	switch {
	case body == "":
		http.Error(w, "body must not be empty", http.StatusBadRequest)
		return
	case len(body) > maxCommentBytes:
		http.Error(w, "a comment is limited to 8 KB", http.StatusRequestEntityTooLarge)
		return
	case !utf8.ValidString(body) || !utf8.ValidString(quote):
		http.Error(w, "comment text must be UTF-8", http.StatusBadRequest)
		return
	case len(quote) > maxQuoteBytes:
		http.Error(w, "the quoted passage is too long", http.StatusRequestEntityTooLarge)
		return
	case req.ParentID < 0:
		http.Error(w, "invalid parent_id", http.StatusBadRequest)
		return
	case req.ParentID == 0 && quote == "":
		http.Error(w, "a comment must quote the passage it is about", http.StatusBadRequest)
		return
	}
	params := sqlcgen.InsertFileCommentParams{FileID: file.ID, UserID: user.ID, Quote: quote, Body: body}
	if req.ParentID != 0 {
		params.ParentID = sql.NullInt64{Int64: req.ParentID, Valid: true}
		params.Quote = "" // a reply is about its parent, not a passage
	}
	c, err := h.queries.InsertFileComment(r.Context(), params)
	if err != nil {
		if err == sql.ErrNoRows && req.ParentID != 0 {
			// No such top-level comment on this document.
			http.Error(w, "can only reply to a top-level comment of this document", http.StatusBadRequest)
			return
		}
		serverError(w, h.logger, "insert comment", err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, toComment(c, user.Nickname, user.Username, user.ID, file.UserID))
}

// Delete answers DELETE /api/comments/{slug}/{id}: the comment and its replies.
func (h *CommentsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := readableFile(w, r, h.queries, h.logger, user)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		notFound(w)
		return
	}
	c, err := h.queries.GetFileComment(r.Context(), sqlcgen.GetFileCommentParams{ID: id, FileID: file.ID})
	if err != nil {
		queryError(w, h.logger, "get comment", err)
		return
	}
	if c.UserID != user.ID && file.UserID != user.ID {
		// The caller can read the comment, so it is not hidden from them; they
		// are simply not allowed to remove it.
		http.Error(w, "only the author or the document's owner can delete a comment", http.StatusForbidden)
		return
	}
	if _, err := h.queries.DeleteFileCommentThread(r.Context(), sqlcgen.DeleteFileCommentThreadParams{
		FileID: file.ID, ID: c.ID,
	}); err != nil {
		serverError(w, h.logger, "delete comment", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type explainRequest struct {
	Quote string `json:"quote"`
}

// maxExplainRunes bounds what one explanation stores; the prompt asks for a
// short answer, this only stops a model that does not listen.
const maxExplainRunes = 6000

// explainSystemPrompt asks what the selected passage is, from the document
// alone. Unlike the chat's ordinary prompt it does not allow filling a gap from
// general knowledge: when the document does not say, the answer is that it does
// not.
func explainSystemPrompt(file sqlcgen.File, doc, quote string) string {
	return "You are a helpful assistant. The user is reading the document below and selected the passage inside <selection>; they want to know what it is. " +
		"Explain it using only the document: find where the passage appears, read the text around it, and say what it refers to or means in that context. " +
		"If the document does not give enough information to tell, say plainly that the document does not make clear what it is and what is missing. " +
		"Do not guess, and do not fill the gap with outside knowledge. " +
		"Keep the answer short and direct, in the language of the document, and format it in Markdown.\n\n" +
		documentBlock(file, doc) + "\n\n<selection>\n" + quote + "\n</selection>"
}

// Explain answers POST /api/comments/{slug}/explain: it asks the model what the
// quoted passage is, from the document alone, and stores the answer as an AI
// comment on that passage. The answer is collected whole, not streamed, since
// it appears as a comment card only once it exists. Nothing is stored when the
// model cannot be reached, refuses, or says nothing.
func (h *CommentsHandler) Explain(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := readableFile(w, r, h.queries, h.logger, user)
	if !ok {
		return
	}
	var req explainRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}
	quote := strings.TrimSpace(req.Quote)
	switch {
	case quote == "":
		http.Error(w, "quote must not be empty", http.StatusBadRequest)
		return
	case len(quote) > maxQuoteBytes:
		http.Error(w, "the quoted passage is too long", http.StatusRequestEntityTooLarge)
		return
	case !utf8.ValidString(quote):
		http.Error(w, "quote must be UTF-8", http.StatusBadRequest)
		return
	}
	cfg := loadAIConfig(r.Context(), h.queries)
	if !cfg.ready() {
		http.Error(w, "AI is not configured on this server", http.StatusConflict)
		return
	}
	doc, err := documentText(h.blobs, file)
	if err != nil {
		serverError(w, h.logger, "read document for explain", err, "slug", file.Slug)
		return
	}
	stream, err := openLLMStream(r.Context(), h.client, cfg, []llmMessage{
		{Role: "system", Content: explainSystemPrompt(file, doc, quote)},
		{Role: "user", Content: "What is the selected passage?"},
	})
	if err != nil {
		h.explainFailed(w, r, err)
		return
	}
	defer stream.Close()
	var answer strings.Builder
	for {
		delta, done, err := stream.next()
		answer.WriteString(delta)
		if err != nil {
			h.explainFailed(w, r, err)
			return
		}
		if done {
			break
		}
	}
	text := shorten(strings.TrimSpace(answer.String()), maxExplainRunes)
	if text == "" {
		http.Error(w, "the AI service returned an empty answer", http.StatusBadGateway)
		return
	}
	c, err := h.queries.InsertFileComment(context.WithoutCancel(r.Context()), sqlcgen.InsertFileCommentParams{
		FileID: file.ID, UserID: user.ID, Quote: quote, Body: text, IsAi: true,
	})
	if err != nil {
		serverError(w, h.logger, "insert explanation", err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, toComment(c, user.Nickname, user.Username, user.ID, file.UserID))
}

// explainFailed answers a model failure the way the chat does: the provider's
// own refusal as it put it, anything else as unreachable.
func (h *CommentsHandler) explainFailed(w http.ResponseWriter, r *http.Request, err error) {
	if ue, ok := isUpstream(err); ok {
		http.Error(w, ue.Error(), http.StatusBadGateway)
		return
	}
	if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
		return // the reader went away
	}
	h.logger.Warn("explain", "error", err)
	http.Error(w, "could not reach the AI service: "+err.Error(), http.StatusBadGateway)
}
