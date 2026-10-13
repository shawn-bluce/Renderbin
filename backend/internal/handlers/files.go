package handlers

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
	"github.com/shawn-bluce/renderbin/backend/internal/site"
)

// maxHTMLBytes caps stored HTML at 5 MB per file. maxFileBody is the cap on the
// raw request body carrying it.
//
// maxFileBody is deliberately far above maxHTMLBytes rather than "5MB plus a
// little". The body is JSON, and JSON escaping costs an extra byte for every
// quote, backslash, tab and newline in the document -- a 5MB HTML file has well
// over a hundred thousand of those. The cap used to be maxHTMLBytes+64KB, so a
// legal 5MB document tripped the reader before the decoder ever ran and the
// user got 400 "invalid request body" for a file that was within the advertised
// limit. Leaving room for a 2x expansion means the explicit length check below
// is what rejects an oversized document, with a message that says so.
const (
	maxHTMLBytes = 5 << 20
	maxFileBody  = 2*maxHTMLBytes + 64<<10
)

// maxNameBytes bounds a file's display name. It is not cosmetic: the name goes
// into the Content-Disposition header of every download, and a name longer than
// the reverse proxy's header buffer (Nginx defaults to 4-8KB) turns that
// download into a 502 with nothing in the app's own logs to explain it.
const maxNameBytes = 255

// maxTagsBytes bounds the raw comma-separated tag string before normalizeTags
// splits it, which it does into one intermediate string per comma.
const maxTagsBytes = 1024

// slugPattern restricts custom slugs to URL-safe characters so they can be
// dropped into /res/{slug} without escaping.
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// validPathToken is slugPattern minus "." and "..". Slugs and access codes are
// both path segments of a site URL (/res/{slug}/{code}/...), and a browser
// resolves dot segments before it sends the request, so a site whose slug or
// code is ".." could never be reached -- the segment simply vanishes. (A
// single document at /res/.. was never reachable either.)
func validPathToken(s string) bool {
	return slugPattern.MatchString(s) && s != "." && s != ".."
}

// withFreshSlug runs insert under a freshly generated short slug, retrying up
// to 5 times on UNIQUE collisions — 8-char slugs carry only 24 bits of
// entropy, so a collision is a realistic event once enough files (including
// soft-deleted ones, which keep their slug) accumulate. Shared by every path
// that creates a file or a site, over HTTP and MCP alike.
func withFreshSlug(insert func(slug string) (sqlcgen.File, error)) (sqlcgen.File, error) {
	const maxSlugAttempts = 5
	for attempt := 1; ; attempt++ {
		slug, err := auth.NewShortToken()
		if err != nil {
			return sqlcgen.File{}, err
		}
		file, err := insert(slug)
		if err == nil {
			return file, nil
		}
		if !strings.Contains(err.Error(), "UNIQUE") || attempt >= maxSlugAttempts {
			return sqlcgen.File{}, err
		}
	}
}

// createFileWithFreshSlug inserts a document under a fresh slug (params.Slug is
// overwritten).
func createFileWithFreshSlug(ctx context.Context, queries *sqlcgen.Queries, params sqlcgen.CreateFileParams) (sqlcgen.File, error) {
	return withFreshSlug(func(slug string) (sqlcgen.File, error) {
		params.Slug = slug
		return queries.CreateFile(ctx, params)
	})
}

type FilesHandler struct {
	queries *sqlcgen.Queries
	logger  *slog.Logger
	// preview signs the owner's preview links into private sites; see
	// previewSigner. Its key lives only in this process.
	preview *previewSigner
}

func NewFilesHandler(queries *sqlcgen.Queries, logger *slog.Logger) *FilesHandler {
	return &FilesHandler{queries: queries, logger: logger, preview: newPreviewSigner()}
}

type fileResponse struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	HTMLContent string `json:"html_content,omitempty"`
	// Size is the stored source's length in bytes (for a site, its compressed
	// archive), read from the files.content_size column the INSERT and UPDATE
	// statements keep current. It is the one source of truth for every
	// endpoint, including the listings, whose rows carry no content to measure.
	Size             int64   `json:"size"`
	IsPublic         bool    `json:"is_public"`
	AccessCode       string  `json:"access_code"`
	Tags             string  `json:"tags"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	SuccessCount     int64   `json:"success_count"`
	CodeSuccessCount int64   `json:"code_success_count"`
	FailureCount     int64   `json:"failure_count"`
	ExpiresAt        *string `json:"expires_at"`
	MaxViews         *int64  `json:"max_views"`
	ViewCount        int64   `json:"view_count"`
	// The last time a limit took this file offline, and which limit it was
	// (ExpiryReasonTTL / ExpiryReasonViews). Only the most recent event is
	// kept, and configuring a new limit clears both.
	ExpiredAt     *string `json:"expired_at"`
	ExpiredReason string  `json:"expired_reason"`
	// Entries lists a site's files. Only the single-file endpoints fill it;
	// the listings never read the archive.
	Entries []site.EntryInfo `json:"entries,omitempty"`
}

const timeLayout = "2006-01-02T15:04:05Z07:00"

func toFileResponse(f sqlcgen.File) fileResponse {
	resp := fileResponse{
		Slug:             f.Slug,
		Name:             f.Name,
		Kind:             f.Kind,
		HTMLContent:      f.HtmlContent,
		Size:             f.ContentSize,
		IsPublic:         f.IsPublic,
		AccessCode:       f.AccessCode,
		Tags:             f.Tags,
		CreatedAt:        f.CreatedAt.Format(timeLayout),
		UpdatedAt:        f.UpdatedAt.Format(timeLayout),
		SuccessCount:     f.SuccessCount,
		CodeSuccessCount: f.CodeSuccessCount,
		FailureCount:     f.FailureCount,
		ViewCount:        f.ViewCount,
		ExpiredReason:    f.ExpiredReason,
	}
	resp.ExpiresAt = fmtNullTime(f.ExpiresAt)
	resp.ExpiredAt = fmtNullTime(f.ExpiredAt)
	if f.MaxViews.Valid {
		v := f.MaxViews.Int64
		resp.MaxViews = &v
	}
	return resp
}

// fmtNullTime renders a nullable timestamp as the wire's nullable string.
func fmtNullTime(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.Format(timeLayout)
	return &s
}

// fileMetaResponse is toFileResponse without the document source: what every
// endpoint but Get returns for a file it just wrote.
func fileMetaResponse(f sqlcgen.File) fileResponse {
	resp := toFileResponse(f)
	resp.HTMLContent = ""
	return resp
}

// writeFile finishes a handler whose last step is one query returning the
// file's row: no row is the usual 404, any other error is a logged 500, and
// success answers with the row's metadata.
func (h *FilesHandler) writeFile(w http.ResponseWriter, op string, file sqlcgen.File, err error) {
	if err != nil {
		queryError(w, h.logger, op, err)
		return
	}
	writeJSON(w, fileMetaResponse(file))
}

// normalizeTags trims whitespace around each comma-separated tag, drops
// empty entries, and dedupes while preserving first-seen order, so the
// stored value is always a clean canonical form regardless of what the
// client sent.
func normalizeTags(raw string) string {
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	return strings.Join(tags, ",")
}

// The listing and search queries deliberately don't select html_content (see
// queries/files.sql), so sqlc emits a distinct row struct for each of them
// instead of the shared File model. These converters funnel those rows back
// into one File with an empty HtmlContent, so toFileResponse stays the single
// place that formats a file for the wire.
//
// The list, trash and name-search rows are field-for-field the same struct, so
// they share listRowToFile through a type conversion -- which, unlike a
// struct literal, stops compiling if one of them gains a column the others
// lack. Only the content search has extra fields, and keeps its own.
func listRowToFile(r sqlcgen.ListUserFilesRow) sqlcgen.File {
	return sqlcgen.File{
		ID: r.ID, Slug: r.Slug, Name: r.Name, IsPublic: r.IsPublic,
		AccessCode: r.AccessCode, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		DeletedAt: r.DeletedAt, SuccessCount: r.SuccessCount, FailureCount: r.FailureCount,
		Tags: r.Tags, ExpiresAt: r.ExpiresAt, MaxViews: r.MaxViews, ViewCount: r.ViewCount,
		Kind: r.Kind, CodeSuccessCount: r.CodeSuccessCount, UserID: r.UserID,
		ExpiredAt: r.ExpiredAt, ExpiredReason: r.ExpiredReason, ContentSize: r.ContentSize,
	}
}

func contentSearchRowToFile(r sqlcgen.SearchUserFilesWithContentRow) sqlcgen.File {
	return sqlcgen.File{
		ID: r.ID, Slug: r.Slug, Name: r.Name, IsPublic: r.IsPublic,
		AccessCode: r.AccessCode, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		DeletedAt: r.DeletedAt, SuccessCount: r.SuccessCount, FailureCount: r.FailureCount,
		Tags: r.Tags, ExpiresAt: r.ExpiresAt, MaxViews: r.MaxViews, ViewCount: r.ViewCount,
		Kind: r.Kind, CodeSuccessCount: r.CodeSuccessCount, UserID: r.UserID,
		ExpiredAt: r.ExpiredAt, ExpiredReason: r.ExpiredReason, ContentSize: r.ContentSize,
	}
}

func (h *FilesHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	var resp []fileResponse
	if r.URL.Query().Get("deleted") == "true" {
		rows, err := h.queries.ListUserDeletedFiles(r.Context(), user.ID)
		if err != nil {
			serverError(w, h.logger, "list deleted files", err)
			return
		}
		resp = make([]fileResponse, 0, len(rows))
		for _, row := range rows {
			resp = append(resp, toFileResponse(listRowToFile(sqlcgen.ListUserFilesRow(row))))
		}
	} else {
		rows, err := h.queries.ListUserFiles(r.Context(), user.ID)
		if err != nil {
			serverError(w, h.logger, "list files", err)
			return
		}
		resp = make([]fileResponse, 0, len(rows))
		for _, row := range rows {
			resp = append(resp, toFileResponse(listRowToFile(row)))
		}
	}

	writeJSON(w, resp)
}

// searchResultResponse augments the list-row fields with where the query
// matched: a name hit renders as a plain title row, while a content-only hit
// additionally carries the weakened snippet (±100 runes around the match).
type searchResultResponse struct {
	fileResponse
	MatchedName    bool   `json:"matched_name"`
	MatchedContent bool   `json:"matched_content"`
	Snippet        string `json:"snippet,omitempty"`
}

// Search implements GET /api/search?q=...&content=true — substring
// search scoped to the current user's own (non-deleted) files. Name-only by
// default; content=true also searches the stored source.
func (h *FilesHandler) Search(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	includeContent := r.URL.Query().Get("content") == "true"

	resp := make([]searchResultResponse, 0)
	switch {
	case q == "":
		// Nothing to search; answer with no rows.
	case includeContent:
		rows, err := h.queries.SearchUserFilesWithContent(r.Context(), sqlcgen.SearchUserFilesWithContentParams{
			UserID:       user.ID,
			NameQuery:    q,
			ContentQuery: q,
		})
		if err != nil {
			serverError(w, h.logger, "search files", err)
			return
		}
		for _, row := range rows {
			res := searchResultResponse{
				fileResponse:   toFileResponse(contentSearchRowToFile(row)),
				MatchedName:    containsFold(row.Name, q),
				MatchedContent: row.MatchPos > 0,
			}
			// A title hit renders as a plain row, so only a content-*only* hit
			// carries a snippet -- but matched_content above reports the truth
			// either way, since a consumer other than the dashboard has no
			// reason to read it as "matched content and nothing else".
			if !res.MatchedName {
				res.Snippet = snippetFromWindow(row.SnippetWindow, q, row.MatchPos, row.ContentChars)
			}
			resp = append(resp, res)
		}
	default:
		rows, err := h.queries.SearchUserFilesByName(r.Context(), sqlcgen.SearchUserFilesByNameParams{
			UserID:    user.ID,
			NameQuery: q,
		})
		if err != nil {
			serverError(w, h.logger, "search files", err)
			return
		}
		for _, row := range rows {
			resp = append(resp, searchResultResponse{
				fileResponse: toFileResponse(listRowToFile(sqlcgen.ListUserFilesRow(row))),
				MatchedName:  true,
			})
		}
	}

	writeJSON(w, resp)
}

// containsFold reports whether s contains substr case-insensitively (full
// Unicode folding — a superset of the SQL queries' ASCII-only lower()).
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// snippetRadius is how many runes of context to keep on each side of a
// content match in search results.
const snippetRadius = 100

// snippetFromWindow turns the bounded window SearchUserFilesWithContent
// extracts around a match into the excerpt the dashboard shows: the match plus
// up to snippetRadius runes either side, with an ellipsis on each end that was
// truncated.
//
// The window comes from SQL rather than the whole document on purpose -- see
// the query -- so this never sees the file's full source and cannot be given a
// 5MB string to slice. It receives the window, the 1-based character offset of
// the match within the *document* (0 when the content did not match at all),
// and the document's length in characters, which together are everything
// needed to decide about ellipses.
//
// The window already starts at the right place (SQL clips it to 100 characters
// before the match), so only its tail may need trimming: when the match sits
// near the start of the document the "before" part is short and SQL's
// fixed-length window runs further past the match than snippetRadius allows.
func snippetFromWindow(window, query string, matchPos, contentChars int64) string {
	if matchPos <= 0 || window == "" {
		return ""
	}

	start := max(int64(1), matchPos-snippetRadius) // 1-based, in characters
	runes := []rune(window)
	// Characters of the window we keep: everything up to snippetRadius past
	// the end of the match.
	keep := (matchPos - start) + int64(utf8.RuneCountInString(query)) + snippetRadius
	if keep < int64(len(runes)) {
		runes = runes[:keep]
	}

	snippet := string(runes)
	if start > 1 {
		snippet = "…" + snippet
	}
	if start-1+int64(len(runes)) < contentChars {
		snippet += "…"
	}
	return snippet
}

type createFileRequest struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	HTMLContent string `json:"html_content"`
}

// contentProblem applies the two rules every stored document has to satisfy
// and returns the status and message for the first one it breaks (0, "" when
// the content is fine). field names the input in the message, since the web API
// calls it html_content and MCP calls it content. Shared by both protocols so
// the limit and its wording are stated once.
func contentProblem(field, content string) (int, string) {
	switch {
	case content == "":
		return http.StatusBadRequest, field + " is required"
	case len(content) > maxHTMLBytes:
		return http.StatusRequestEntityTooLarge, field + " exceeds 5MB"
	}
	return 0, ""
}

// validateContent is contentProblem for an HTTP handler: it writes the
// response and returns false when the content is unusable.
func validateContent(w http.ResponseWriter, content string) bool {
	if status, msg := contentProblem("html_content", content); status != 0 {
		http.Error(w, msg, status)
		return false
	}
	return true
}

// nameTooLong enforces maxNameBytes on a display name that is already trimmed.
func nameTooLong(name string) error {
	if len(name) > maxNameBytes {
		return fmt.Errorf("name must be at most %d bytes", maxNameBytes)
	}
	return nil
}

// cleanName trims a display name, defaults it to "Untitled", and enforces
// maxNameBytes. The pure core of normalizeName and of MCP's uploads.
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Untitled"
	}
	return name, nameTooLong(name)
}

// normalizeName is cleanName for an HTTP handler: it writes the response and
// returns false when the name is unusable.
func normalizeName(w http.ResponseWriter, name string) (string, bool) {
	name, err := cleanName(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	return name, true
}

// errOverQuota is what a caller shows when a write would exceed the owner's
// storage limit. It is a sentinel so the HTTP layer can map it to 413 while the
// MCP layer passes the message straight through as a tool error.
var errOverQuota = errors.New("storage quota exceeded")

// enforceQuota reports an error when storing newSize bytes would push the
// account past its quota, given that replacing bytes of theirs are about to be
// freed (0 on upload, the old document's size on an edit).
//
// **Every path that stores content must call this**, not just the HTTP one.
// `users.quota_bytes` is worth nothing if one way in ignores it, and MCP is a
// way in: an API key could upload 20 files of 5MB per call, without limit,
// while the dashboard's own uploads were being refused.
//
// The sum comes from the content_size column, so this costs an indexed
// aggregate rather than a read of every document the account owns. It is a
// check-then-write, so two simultaneous uploads can both pass and overshoot by
// one file; that is an accepted rounding error on a limit whose purpose is to
// stop a runaway account, not to bill anyone.
func enforceQuota(ctx context.Context, queries *sqlcgen.Queries, user sqlcgen.User, newSize, replacing int64) error {
	used, err := queries.SumUserContentSize(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("sum user content size: %w", err)
	}
	if used-replacing+newSize > user.QuotaBytes {
		return fmt.Errorf("%w: this account may store %d bytes and is using %d",
			errOverQuota, user.QuotaBytes, used)
	}
	return nil
}

// checkQuota is enforceQuota for an HTTP handler: it writes the response and
// reports whether the caller should continue.
func (h *FilesHandler) checkQuota(w http.ResponseWriter, r *http.Request, user sqlcgen.User, newSize, replacing int64) bool {
	err := enforceQuota(r.Context(), h.queries, user, newSize, replacing)
	switch {
	case err == nil:
		return true
	case errors.Is(err, errOverQuota):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	default:
		serverError(w, h.logger, "check quota", err, "user_id", user.ID)
	}
	return false
}

func (h *FilesHandler) Create(w http.ResponseWriter, r *http.Request) {
	// requireAuth resolved the session; the file is attributed to its uploader.
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	var req createFileRequest
	if !decodeJSON(w, r, &req, maxFileBody) {
		return
	}
	if !validateContent(w, req.HTMLContent) {
		return
	}
	kind, ok := normalizeKind(req.Kind)
	if !ok {
		http.Error(w, "kind must be one of html, markdown, txt", http.StatusBadRequest)
		return
	}
	name, ok := normalizeName(w, req.Name)
	if !ok {
		return
	}
	req.Name = name
	if !h.checkQuota(w, r, user, int64(len(req.HTMLContent)), 0) {
		return
	}

	accessCode, err := auth.NewAccessCode()
	if err != nil {
		serverError(w, h.logger, "generate access code", err)
		return
	}

	file, err := createFileWithFreshSlug(r.Context(), h.queries, sqlcgen.CreateFileParams{
		Name:        req.Name,
		HtmlContent: req.HTMLContent,
		Kind:        kind,
		// The instance-wide default visibility. Deliberately not a request
		// field: visibility changes are their own endpoint, and a per-request
		// override here would let one protocol bypass the policy the admin set.
		IsPublic:   configBool(r.Context(), h.queries, ConfigUploadDefaultPublic),
		AccessCode: accessCode,
		UserID:     user.ID,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "slug already taken", http.StatusConflict)
			return
		}
		serverError(w, h.logger, "create file", err)
		return
	}

	writeJSONStatus(w, http.StatusCreated, fileMetaResponse(file))
}

// userFile loads the caller's own live file by slug, answering 404 (a slug
// that is missing, trashed or someone else's is one and the same case) or 500
// itself and reporting whether the caller should continue.
func (h *FilesHandler) userFile(w http.ResponseWriter, r *http.Request, user sqlcgen.User, slug string) (sqlcgen.File, bool) {
	file, err := h.queries.GetUserFileBySlug(r.Context(), sqlcgen.GetUserFileBySlugParams{
		Slug:   slug,
		UserID: user.ID,
	})
	if err != nil {
		queryError(w, h.logger, "get file", err)
		return sqlcgen.File{}, false
	}
	return file, true
}

// ownedFileSize is the lookup an edit starts with: what the write is about to
// replace (for the quota) and the file's kind. A slug that matches nothing 404s
// here rather than after the quota check, which keeps a missing file from being
// reported as a full one.
func (h *FilesHandler) ownedFileSize(w http.ResponseWriter, r *http.Request, user sqlcgen.User, slug string) (sqlcgen.GetUserFileSizeRow, bool) {
	old, err := h.queries.GetUserFileSize(r.Context(), sqlcgen.GetUserFileSizeParams{
		Slug:   slug,
		UserID: user.ID,
	})
	if err != nil {
		queryError(w, h.logger, "get file size", err)
		return sqlcgen.GetUserFileSizeRow{}, false
	}
	return old, true
}

// Get returns a single file including its html_content, at GET
// /api/files/{slug}. This is the only endpoint that returns content, and it
// feeds the editor. Scoped to the caller's own files, so another user's slug
// 404s exactly like one that never existed.
func (h *FilesHandler) Get(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := h.userFile(w, r, user, chi.URLParam(r, "slug"))
	if !ok {
		return
	}

	resp := toFileResponse(file)
	if file.Kind == KindSite {
		// A site has no single source to edit; the dashboard shows what it
		// holds instead, read from the zip's central directory alone.
		entries, err := site.List(file.Archive)
		if err != nil {
			serverError(w, h.logger, "list site entries", err, "slug", file.Slug)
			return
		}
		resp.Entries = entries
	}
	writeJSON(w, resp)
}

type updateFileRequest struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	HTMLContent string `json:"html_content"`
	AccessCode  string `json:"access_code"`
}

// Update replaces a file's name, slug, html_content, and access_code at PATCH
// /api/files/{slug}. The slug and access code are user-editable (custom
// values); a slug collision with an existing file returns 409.
//
// A site takes the same request minus html_content: its content is replaced
// only as a whole archive, at PUT /api/files/{slug}/site.
func (h *FilesHandler) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	oldSlug := chi.URLParam(r, "slug")

	var req updateFileRequest
	if !decodeJSON(w, r, &req, maxFileBody) {
		return
	}
	req.Slug = strings.TrimSpace(req.Slug)
	if !validPathToken(req.Slug) {
		http.Error(w, "slug must be 1-128 chars of letters, digits, '.', '_' or '-' (and not '.' or '..')", http.StatusBadRequest)
		return
	}
	req.AccessCode = strings.TrimSpace(req.AccessCode)
	// Same charset as the slug, and {1,128} forbids the empty string — an
	// empty stored access_code would compare equal to a missing ?code= param in
	// accessCodeMatches, opening the file to everyone.
	if !validPathToken(req.AccessCode) {
		http.Error(w, "access_code must be 1-128 chars of letters, digits, '.', '_' or '-' (and not '.' or '..')", http.StatusBadRequest)
		return
	}
	name, ok := normalizeName(w, req.Name)
	if !ok {
		return
	}
	req.Name = name

	// What this edit replaces, so an in-place edit isn't charged twice against
	// the quota.
	old, ok := h.ownedFileSize(w, r, user, oldSlug)
	if !ok {
		return
	}

	var (
		file sqlcgen.File
		err  error
	)
	if old.Kind == KindSite {
		if req.HTMLContent != "" {
			http.Error(w, "a site's content is replaced as a whole zip at PUT /api/files/{slug}/site", http.StatusBadRequest)
			return
		}
		file, err = h.queries.UpdateSiteMeta(r.Context(), sqlcgen.UpdateSiteMetaParams{
			Name:       req.Name,
			NewSlug:    req.Slug,
			AccessCode: req.AccessCode,
			Slug:       oldSlug,
			UserID:     user.ID,
		})
	} else {
		if !validateContent(w, req.HTMLContent) {
			return
		}
		if !h.checkQuota(w, r, user, int64(len(req.HTMLContent)), old.ContentSize) {
			return
		}
		file, err = h.queries.UpdateFile(r.Context(), sqlcgen.UpdateFileParams{
			Name:        req.Name,
			NewSlug:     req.Slug,
			HtmlContent: req.HTMLContent,
			AccessCode:  req.AccessCode,
			Slug:        oldSlug,
			UserID:      user.ID,
		})
	}
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		http.Error(w, "slug already taken", http.StatusConflict)
		return
	}
	h.writeFile(w, "update file", file, err)
}

// Restore un-deletes a soft-deleted file at POST /api/files/{slug}/restore.
func (h *FilesHandler) Restore(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")

	file, err := h.queries.RestoreFile(r.Context(), sqlcgen.RestoreFileParams{
		Slug:   slug,
		UserID: user.ID,
	})
	h.writeFile(w, "restore file", file, err)
}

// A ttl is "<positive integer><unit>", e.g. 1d, 36h, 2w, 6mo, 1y — the same
// vocabulary for the HTTP SetExpiry handler and the MCP publish_file tool. It
// used to be a fixed preset set (24h/48h/72h/7d/30d); those all still parse
// under this grammar, so nothing that spoke the old vocabulary broke.
var ttlPattern = regexp.MustCompile(`^([0-9]{1,6})(h|d|w|mo|y)$`)

// ttlSyntax is the syntax half of the ttl error message, spelled out once.
const ttlSyntax = "ttl must be a positive whole number followed by h, d, w, mo or y (e.g. 1d, 36h, 2w, 6mo, 1y)"

// maxTTLYears bounds how far ahead a link may be scheduled to expire. It is a
// sanity cap, not a product limit: without one, "999999y" overflows time.Time
// into a deadline in the past, i.e. a link that is born expired.
const maxTTLYears = 10

// ttlDeadline turns a ttl spec into the instant the link should stop working,
// measured from now. Hours, days and weeks are fixed spans; months and years
// step the calendar, so "1mo" lands on the same day of the next month rather
// than 30 days out. Returns a client-facing message when the spec is unusable.
func ttlDeadline(spec string, now time.Time) (time.Time, string) {
	m := ttlPattern.FindStringSubmatch(spec)
	if m == nil {
		return time.Time{}, ttlSyntax
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return time.Time{}, ttlSyntax
	}

	var deadline time.Time
	switch m[2] {
	case "h":
		deadline = now.Add(time.Duration(n) * time.Hour)
	case "d":
		deadline = now.AddDate(0, 0, n)
	case "w":
		deadline = now.AddDate(0, 0, 7*n)
	case "mo":
		deadline = now.AddDate(0, n, 0)
	case "y":
		deadline = now.AddDate(n, 0, 0)
	}
	if deadline.After(now.AddDate(maxTTLYears, 0, 0)) {
		return time.Time{}, fmt.Sprintf("ttl must not exceed %d years", maxTTLYears)
	}
	return deadline, ""
}

// Why a file was last taken offline, stored in files.expired_reason.
const (
	ExpiryReasonTTL   = "ttl"   // the time window passed
	ExpiryReasonViews = "views" // the view quota ran out
)

// expiryLimit is a validated expiry setting: exactly one of a deadline or a
// view quota, or neither. Both the HTTP handler and the MCP tool build one of
// these, so the "mutually exclusive" rule is stated once.
type expiryLimit struct {
	expiresAt sql.NullTime
	maxViews  sql.NullInt64
	set       bool // false means "no limit", i.e. clear whatever was there
}

// parseExpiryLimit validates a ttl spec and/or a max-view count. It returns a
// client-facing message for invalid input; nil ttl and nil maxViews mean "no
// limit", which is a valid request rather than an error.
func parseExpiryLimit(ttl *string, maxViews *int64) (expiryLimit, string) {
	switch {
	case ttl != nil && maxViews != nil:
		return expiryLimit{}, "ttl and max_views are mutually exclusive"
	case ttl != nil:
		deadline, errMsg := ttlDeadline(*ttl, time.Now())
		if errMsg != "" {
			return expiryLimit{}, errMsg
		}
		return expiryLimit{
			expiresAt: sql.NullTime{Time: deadline, Valid: true},
			set:       true,
		}, ""
	case maxViews != nil:
		if *maxViews <= 0 {
			return expiryLimit{}, "max_views must be positive"
		}
		return expiryLimit{
			maxViews: sql.NullInt64{Int64: *maxViews, Valid: true},
			set:      true,
		}, ""
	default:
		return expiryLimit{}, ""
	}
}

type setExpiryRequest struct {
	TTL      *string `json:"ttl"`
	MaxViews *int64  `json:"max_views"`
}

// SetExpiry configures a file's link expiry at PATCH /api/files/{slug}/expiry.
// A ttl and a max-view count are mutually exclusive; setting either
// forces the file Public. Sending neither clears any existing limit without
// changing visibility. Expiry itself is enforced lazily in Render.
func (h *FilesHandler) SetExpiry(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")

	var req setExpiryRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}
	limit, errMsg := parseExpiryLimit(req.TTL, req.MaxViews)
	if errMsg != "" {
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	var (
		file sqlcgen.File
		err  error
	)
	if limit.set {
		file, err = h.queries.SetFileExpiry(r.Context(), sqlcgen.SetFileExpiryParams{
			ExpiresAt: limit.expiresAt,
			MaxViews:  limit.maxViews,
			Slug:      slug,
			UserID:    user.ID,
		})
	} else {
		file, err = h.queries.ClearFileExpiry(r.Context(), sqlcgen.ClearFileExpiryParams{
			Slug:   slug,
			UserID: user.ID,
		})
	}
	h.writeFile(w, "set expiry", file, err)
}

type renameFileRequest struct {
	Name string `json:"name"`
}

func (h *FilesHandler) Rename(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")

	var req renameFileRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := nameTooLong(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	file, err := h.queries.RenameFile(r.Context(), sqlcgen.RenameFileParams{
		Name:   name,
		Slug:   slug,
		UserID: user.ID,
	})
	h.writeFile(w, "rename file", file, err)
}

type setVisibilityRequest struct {
	IsPublic bool `json:"is_public"`
}

func (h *FilesHandler) SetVisibility(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")

	var req setVisibilityRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}

	file, err := h.queries.SetFileVisibility(r.Context(), sqlcgen.SetFileVisibilityParams{
		IsPublic: req.IsPublic,
		Slug:     slug,
		UserID:   user.ID,
	})
	h.writeFile(w, "set file visibility", file, err)
}

type setTagsRequest struct {
	Tags string `json:"tags"`
}

func (h *FilesHandler) SetTags(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")

	var req setTagsRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}
	if len(req.Tags) > maxTagsBytes {
		http.Error(w, fmt.Sprintf("tags must be at most %d bytes", maxTagsBytes), http.StatusBadRequest)
		return
	}

	file, err := h.queries.SetFileTags(r.Context(), sqlcgen.SetFileTagsParams{
		Tags:   normalizeTags(req.Tags),
		Slug:   slug,
		UserID: user.ID,
	})
	h.writeFile(w, "set file tags", file, err)
}

func (h *FilesHandler) RefreshCode(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")

	accessCode, err := auth.NewAccessCode()
	if err != nil {
		serverError(w, h.logger, "generate access code", err)
		return
	}

	file, err := h.queries.RefreshFileAccessCode(r.Context(), sqlcgen.RefreshFileAccessCodeParams{
		AccessCode: accessCode,
		Slug:       slug,
		UserID:     user.ID,
	})
	h.writeFile(w, "refresh access code", file, err)
}

// Delete moves a file to the trash at DELETE /api/files/{slug}. A slug that
// is unknown, already trashed, or owned by someone else matches no row and
// 404s -- reporting 204 for those would be a success-shaped no-op.
func (h *FilesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")

	rows, err := h.queries.SoftDeleteFile(r.Context(), sqlcgen.SoftDeleteFileParams{
		Slug:   slug,
		UserID: user.ID,
	})
	if err != nil {
		serverError(w, h.logger, "delete file", err)
		return
	}
	if rows == 0 {
		notFound(w)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HardDelete permanently removes a trashed file at DELETE
// /api/files/{slug}/permanent. Only soft-deleted files qualify — an active
// file or unknown slug is a 404 — so the recycle bin is the only path to
// irreversible deletion.
func (h *FilesHandler) HardDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")

	rows, err := h.queries.HardDeleteFile(r.Context(), sqlcgen.HardDeleteFileParams{
		Slug:   slug,
		UserID: user.ID,
	})
	if err != nil {
		serverError(w, h.logger, "hard delete file", err)
		return
	}
	if rows == 0 {
		notFound(w)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type emptyTrashResponse struct {
	Deleted int64 `json:"deleted"`
}

// EmptyTrash permanently deletes every file in the caller's trash at
// DELETE /api/trash, and reports how many rows went. It lives outside
// /api/files on purpose: a static segment there (as /api/files/search once was)
// shadows any custom slug of the same name, and this one is a *delete*, so that
// bug would silently aim at the wrong file.
//
// An already-empty trash is a success with deleted=0, not a 404: the request
// asks for a state ("my trash is empty") and that state holds either way.
func (h *FilesHandler) EmptyTrash(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	rows, err := h.queries.HardDeleteUserTrash(r.Context(), user.ID)
	if err != nil {
		serverError(w, h.logger, "empty trash", err)
		return
	}
	h.logger.Info("trash emptied", "user_id", user.ID, "files", rows)

	writeJSON(w, emptyTrashResponse{Deleted: rows})
}

const accessDeniedBody = `<!doctype html><html><head><meta charset="utf-8"><title>Access denied</title></head><body><p>Access denied.</p></body></html>`

// userContentCSP sandboxes everything served at /res/{slug}, and it is the one
// header standing between "we serve uploader HTML verbatim" and "any account
// can take over any other".
//
// Uploaded documents are served from the same origin as the app's own API. The
// session cookie is HttpOnly, which stops a script reading it -- but not from
// *using* it: a same-origin fetch carries it automatically, and SameSite=Lax
// has nothing to say about a request that is not cross-site. So a document
// uploaded by one user and opened by another, signed in, ran as that viewer.
// It could read and delete their files, and against the super admin it could
// download the whole database (every password hash and API key) or reset their
// password and lock them out. That was fine while the only uploader was the
// person running the server; it stopped being fine when the app grew accounts
// and a registration toggle, and the reasoning did not get revisited.
//
// Omitting allow-same-origin is the entire point: it puts the document in an
// opaque origin, so relative fetches to /api are cross-origin, arrive without
// cookies, and are refused for lack of CORS headers. document.cookie and
// storage are dead there too. Everything a shared document legitimately does
// still works -- scripts run, links navigate, forms submit, popups open -- so
// this costs the feature nothing.
//
// Operators who want to remove even the sandbox can serve /res from a separate
// hostname, which is the stronger form of the same fix.
const userContentCSP = "sandbox allow-scripts allow-forms allow-modals " +
	"allow-popups allow-popups-to-escape-sandbox allow-downloads " +
	"allow-top-navigation-by-user-activation"

// setUserContentHeaders prepares a response carrying uploader-authored bytes.
// Referrer-Policy is not decoration: the access code travels in the URL (the
// query string of a document link, the path of a site link), so without it
// every external image or script in a shared document hands that code to a
// third party in the Referer header.
//
// A site sends this on every entry, stylesheets and scripts included: the
// headers are harmless on a subresource, and any HTML or SVG entry opened
// directly is a document that needs the sandbox as much as index.html does.
func setUserContentHeaders(w http.ResponseWriter, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	// Undo the router's blanket X-Frame-Options: embedding a document you
	// published is reasonable, and the sandbox above is what contains it.
	h.Del("X-Frame-Options")
	h.Set("Content-Security-Policy", userContentCSP)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
}

// Render serves a file's content at GET /res/{slug}?code=<access_code>. The
// served bytes depend on the file's kind (see renderContent): html verbatim,
// markdown rendered to HTML, txt as escaped preformatted text.
// A soft-deleted file always 404s, even for a signed-in owner or a correct
// access code (and isn't counted, since there's no file row to attribute a
// count to). Otherwise: the file's *owner* bypasses the public/code checks;
// everyone else — anonymous, or signed in as a different user — needs
// is_public plus a matching ?code=. Every access to an existing file bumps
// exactly one counter — success_count (owner), code_success_count (correct
// code), or failure_count (blocked) — which is owner-facing analytics, not a
// content change, so it deliberately does not touch updated_at.
//
// This is the one handler that must NOT use an owner-scoped query: it serves
// anonymous visitors holding a share link, who have no user at all.
func (h *FilesHandler) Render(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	file, err := h.queries.GetFileBySlugAnyOwner(r.Context(), slug)
	if err != nil {
		queryError(w, h.logger, "get file", err)
		return
	}

	// Lazy expiry (see lazyExpire); a plain document view is always a view.
	h.lazyExpire(r.Context(), &file, true)

	// Only the owner's session bypasses the code check. A signed-in stranger
	// falls through to the public+code path, exactly like an anonymous
	// visitor. Anonymous requests carry no cookie, so this costs them nothing.
	user, signedIn := CurrentUser(r, h.queries)
	owner := signedIn && user.ID == file.UserID
	if file.Kind == KindSite {
		h.redirectIntoSite(w, r, file, user, owner)
		return
	}
	if owner || (file.IsPublic && accessCodeMatches(file.AccessCode, r.URL.Query().Get("code"))) {
		h.recordView(r.Context(), file, owner)
		writeUserHTML(w, http.StatusOK, renderContent(file.Kind, file.Name, file.HtmlContent))
		return
	}

	h.deny(w, r, file, true)
}

// lazyExpire flips a public file whose TTL has passed, or whose view quota is
// used up, to private at access time (there is no cron). It clears the limit
// columns, records which limit fired so the owner's dashboard can say why the
// link stopped working, and does not touch updated_at. file is updated in
// place so the caller sees the post-expiry state.
//
// countViews says whether this request is a view: a site's stylesheets and
// scripts are not, so the page that uses up the last view still loads its own
// assets. The deadline applies to every request regardless.
func (h *FilesHandler) lazyExpire(ctx context.Context, file *sqlcgen.File, countViews bool) {
	if !file.IsPublic {
		return
	}
	reason := ""
	switch {
	case file.ExpiresAt.Valid && !time.Now().Before(file.ExpiresAt.Time):
		reason = ExpiryReasonTTL
	case countViews && file.MaxViews.Valid && file.ViewCount >= file.MaxViews.Int64:
		reason = ExpiryReasonViews
	}
	if reason == "" {
		return
	}
	if err := h.queries.ExpireFile(ctx, sqlcgen.ExpireFileParams{
		ExpiredReason: reason,
		Slug:          file.Slug,
	}); err != nil {
		h.logger.Warn("expire file", "error", err)
	}
	file.IsPublic = false
}

// bump runs one of the Increment* counter statements. Counters are analytics,
// so a failure is logged and never fails the request being served.
func (h *FilesHandler) bump(ctx context.Context, op string, increment func(context.Context, string) error, slug string) {
	if err := increment(ctx, slug); err != nil {
		h.logger.Warn(op, "error", err)
	}
}

// recordView counts one successful view: success_count for the owner,
// code_success_count for anyone arriving by access code. Only code-based
// access consumes a max-views quota; the owner's own views never do.
func (h *FilesHandler) recordView(ctx context.Context, file sqlcgen.File, owner bool) {
	if owner {
		h.bump(ctx, "increment success count", h.queries.IncrementFileSuccessCount, file.Slug)
		return
	}
	h.bump(ctx, "increment code success count", h.queries.IncrementFileCodeSuccessCount, file.Slug)
	if file.MaxViews.Valid {
		h.bump(ctx, "increment view count", h.queries.IncrementFileViewCount, file.Slug)
	}
}

// deny answers a blocked /res request with the sandboxed 403 page, counting it
// as a failed access when count is set (a site only counts page views).
func (h *FilesHandler) deny(w http.ResponseWriter, r *http.Request, file sqlcgen.File, count bool) {
	if count {
		h.bump(r.Context(), "increment failure count", h.queries.IncrementFileFailureCount, file.Slug)
	}
	writeUserHTML(w, http.StatusForbidden, []byte(accessDeniedBody))
}

// writeUserHTML writes a response made of uploader-influenced HTML under the
// sandbox headers (setUserContentHeaders) with an explicit status.
func writeUserHTML(w http.ResponseWriter, status int, body []byte) {
	setUserContentHeaders(w, htmlContentType)
	w.WriteHeader(status)
	w.Write(body)
}

func accessCodeMatches(want, got string) bool {
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

const htmlContentType = "text/html; charset=utf-8"

// Download serves the raw source as a file attachment at
// GET /api/files/{slug}/download. It returns the stored source untransformed
// (not the rendered form), with a filename extension matching the file's kind.
// Scoped to the caller's own files, so a soft-deleted, unknown, or foreign
// slug all 404 alike. Unlike Render, this is not a public view and
// deliberately does not touch success_count/failure_count.
func (h *FilesHandler) Download(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	file, ok := h.userFile(w, r, user, chi.URLParam(r, "slug"))
	if !ok {
		return
	}

	w.Header().Set("Content-Type", downloadContentType(file.Kind))
	w.Header().Set("Content-Disposition", downloadDisposition(file.Name, file.Slug, extForKind(file.Kind)))
	if file.Kind == KindSite {
		// The stored archive is already the site's source, as a zip.
		w.Write(file.Archive)
		return
	}
	w.Write([]byte(file.HtmlContent))
}

// downloadDisposition builds a Content-Disposition header for a download.
// file.Name is a free-form display name (may be empty, Unicode, or contain
// characters unsafe for a header), so we emit an ASCII fallback (filename=)
// plus an RFC 5987 filename* carrying the real, possibly-Unicode name. ext is
// the extension for the file's kind (e.g. "html", "md", "txt").
func downloadDisposition(name, slug, ext string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = slug
	}
	filename := base + "." + ext

	ascii := sanitizeASCII(filename)
	if ascii == "" {
		ascii = slug + "." + ext
	}

	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii, url.PathEscape(filename))
}

// sanitizeASCII produces a safe ASCII filename: printable ASCII except
// double-quote and backslash is kept, everything else (control chars,
// non-ASCII) becomes '_'. This guarantees no header injection via the
// filename= parameter.
func sanitizeASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f && r != '"' && r != '\\' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
