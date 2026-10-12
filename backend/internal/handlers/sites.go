package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
	"github.com/shawn-bluce/renderbin/backend/internal/site"
)

// Directory sites.
//
// A site is served entry by entry at /res/{slug}/{token}/{path}. The token is
// in the *path* rather than the query string because that is the only place a
// relative reference carries it: a page at /res/s/CODE/index.html that links
// "css/a.css" makes the browser request /res/s/CODE/css/a.css, credential
// included, without the author changing a byte. ?code= would be dropped by the
// first relative link.
//
// A token is one of two things:
//   - the file's access code, which works while the file is public;
//   - "~" plus a signed preview token (previewSigner), which is how the owner
//     opens a private site. Access codes are [A-Za-z0-9._-], so they never
//     start with "~" and the two cannot be confused.
//
// RenderSite deliberately reads no cookie. Pages in the sandbox run in an
// opaque origin: a browser sends no SameSite=Lax cookie with any of their
// subresource requests (verified in Chrome: stylesheets, scripts, images,
// fetch() and iframes all arrive without one), so a session could not have
// authorized the owner's view of a private site anyway -- half of every page
// would 403. Not reading cookies is also what makes the
// Access-Control-Allow-Origin: * below safe: no response here depends on an
// ambient credential, so letting any origin read it hands out nothing the URL
// did not already grant.

// previewTTL is how long an owner's preview link into a private site works.
const previewTTL = 6 * time.Hour

const previewPrefix = "~"

// previewSigner issues and checks the owner's preview tokens. The token binds
// the file, its owner, an expiry and the owner's current password hash, so an
// admin password reset revokes every preview link at once. The key is drawn at
// startup and never stored: a restart invalidates outstanding preview links
// (the owner reopens the site from the dashboard), and there is no key to leak
// from the database or a backup.
type previewSigner struct {
	key []byte
}

func newPreviewSigner() *previewSigner {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("preview key: %v", err))
	}
	return &previewSigner{key: key}
}

func (p *previewSigner) mac(fileID, userID, exp int64, passwordHash string) []byte {
	m := hmac.New(sha256.New, p.key)
	var buf [24]byte
	binary.BigEndian.PutUint64(buf[0:], uint64(fileID))
	binary.BigEndian.PutUint64(buf[8:], uint64(userID))
	binary.BigEndian.PutUint64(buf[16:], uint64(exp))
	m.Write(buf[:])
	m.Write([]byte(passwordHash))
	return m.Sum(nil)[:16]
}

// token returns "~" + base64url(expiry || mac).
func (p *previewSigner) token(file sqlcgen.File, owner sqlcgen.User, now time.Time) string {
	exp := now.Add(previewTTL).Unix()
	raw := make([]byte, 8, 24)
	binary.BigEndian.PutUint64(raw, uint64(exp))
	raw = append(raw, p.mac(file.ID, owner.ID, exp, owner.PasswordHash)...)
	return previewPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

func (p *previewSigner) verify(tok string, file sqlcgen.File, owner sqlcgen.User, now time.Time) bool {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tok, previewPrefix))
	if err != nil || len(raw) != 24 {
		return false
	}
	exp := int64(binary.BigEndian.Uint64(raw[:8]))
	if now.Unix() >= exp {
		return false
	}
	return hmac.Equal(raw[8:], p.mac(file.ID, owner.ID, exp, owner.PasswordHash))
}

// verifyPreview checks a "~" token against the file's owner as they are right
// now. The owner row is read fresh so a password reset is seen immediately; a
// suspended owner never gets this far (GetFileBySlugAnyOwner matches no row).
func (h *FilesHandler) verifyPreview(ctx context.Context, tok string, file sqlcgen.File) bool {
	owner, err := h.queries.GetUserByID(ctx, file.UserID)
	if err != nil {
		return false
	}
	return h.preview.verify(tok, file, owner, time.Now())
}

// sitePath builds /res/{slug}/{token}/{sub} with every segment of sub
// escaped.
func sitePath(slug, tok, sub string) string {
	segs := strings.Split(sub, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/res/" + slug + "/" + tok + "/" + strings.Join(segs, "/")
}

// redirectIntoSite is what /res/{slug}?code=... does for a site: it moves the
// credential into the path, where relative references will carry it. The
// share link format is unchanged, so links copied from the dashboard and
// handed out by MCP keep working. The hop itself is not a view; the counters
// move on the page it leads to.
func (h *FilesHandler) redirectIntoSite(w http.ResponseWriter, r *http.Request, file sqlcgen.File, user sqlcgen.User, owner bool) {
	switch {
	case owner:
		setUserContentHeaders(w, htmlContentType)
		http.Redirect(w, r, sitePath(file.Slug, h.preview.token(file, user, time.Now()), ""), http.StatusFound)
	case file.IsPublic && accessCodeMatches(file.AccessCode, r.URL.Query().Get("code")):
		setUserContentHeaders(w, htmlContentType)
		http.Redirect(w, r, sitePath(file.Slug, file.AccessCode, ""), http.StatusFound)
	default:
		if err := h.queries.IncrementFileFailureCount(r.Context(), file.Slug); err != nil {
			h.logger.Warn("increment failure count", "error", err)
		}
		setUserContentHeaders(w, htmlContentType)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(accessDeniedBody))
	}
}

// isDocumentRequest reports whether a request is a page view, which is the
// only thing that counts toward a site's analytics and its max_views limit. A
// site page pulls in stylesheets, scripts and data files; if each of those
// counted, max_views=1 would expire the link on the first stylesheet and the
// visitor would get a page without its CSS.
//
// The entry has to be HTML and the browser has to be loading it as a page
// (Sec-Fetch-Dest document, iframe or frame). Clients that send no
// Sec-Fetch-Dest at all -- curl, older browsers -- count too, so the limit
// cannot be dodged by stripping the header. HTML pulled in by fetch() (dest
// "empty") is a fragment, not a view.
func isDocumentRequest(r *http.Request, entry string) bool {
	if !site.IsHTML(entry) {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "", "document", "iframe", "frame":
		return true
	default:
		return false
	}
}

const siteNotFoundBody = `<!doctype html><html><head><meta charset="utf-8"><title>Not found</title></head><body><p>This site has no such page.</p></body></html>`

// RenderSite serves GET /res/{slug}/{token}/{path...}. Like Render it uses the
// UNSCOPED lookup, since visitors holding a share link have no account.
func (h *FilesHandler) RenderSite(w http.ResponseWriter, r *http.Request) {
	// Parsed from the decoded path rather than chi's params, which come from
	// RawPath when the request escaped something: one consistent, unescaped
	// view of the whole path. Slugs cannot contain "/", so the first segment
	// is the slug either way.
	rest := strings.TrimPrefix(r.URL.Path, "/res/")
	slug, rest, _ := strings.Cut(rest, "/")
	if rest == "" {
		// /res/{slug}/ behaves like /res/{slug}.
		h.Render(w, r)
		return
	}
	tok, sub, hasSlash := strings.Cut(rest, "/")

	file, err := h.queries.GetFileBySlugAnyOwner(r.Context(), slug)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && file.Kind != KindSite) {
		// Deliberately without setUserContentHeaders: nothing user-authored is
		// served, and the app's own X-Frame-Options: DENY stays on.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("get site", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	entry := sub
	if entry == "" || strings.HasSuffix(entry, "/") {
		entry += site.IndexFile
	}
	doc := isDocumentRequest(r, entry)

	// Lazy expiry, as in Render. The deadline applies to every request; the
	// view limit only to page views, so the page that uses up the last view
	// still loads its stylesheets and scripts.
	if file.IsPublic {
		reason := ""
		switch {
		case file.ExpiresAt.Valid && !time.Now().Before(file.ExpiresAt.Time):
			reason = ExpiryReasonTTL
		case doc && file.MaxViews.Valid && file.ViewCount >= file.MaxViews.Int64:
			reason = ExpiryReasonViews
		}
		if reason != "" {
			if err := h.queries.ExpireFile(r.Context(), sqlcgen.ExpireFileParams{
				ExpiredReason: reason,
				Slug:          file.Slug,
			}); err != nil {
				h.logger.Warn("expire file", "error", err)
			}
			file.IsPublic = false
		}
	}

	preview := false
	switch {
	case strings.HasPrefix(tok, previewPrefix):
		preview = h.verifyPreview(r.Context(), tok, file)
		if !preview {
			h.denySite(w, r, file, doc)
			return
		}
	case file.IsPublic && accessCodeMatches(file.AccessCode, tok):
	default:
		h.denySite(w, r, file, doc)
		return
	}

	if !hasSlash {
		// /res/s/TOKEN -> /res/s/TOKEN/, or index.html's relative references
		// would resolve one level too high and lose the token.
		http.Redirect(w, r, sitePath(slug, tok, "")+queryPart(r), http.StatusFound)
		return
	}

	arc, err := site.Open(file.Archive)
	if err != nil {
		h.logger.Error("open site archive", "slug", file.Slug, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data, found, err := arc.Read(entry)
	if err != nil {
		h.logger.Error("read site entry", "slug", file.Slug, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		// A directory requested without its trailing slash: redirect, for the
		// same relative-reference reason as above.
		if sub != "" && !strings.HasSuffix(sub, "/") && arc.Has(sub+"/"+site.IndexFile) {
			http.Redirect(w, r, sitePath(slug, tok, sub+"/")+queryPart(r), http.StatusFound)
			return
		}
		setUserContentHeaders(w, htmlContentType)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(siteNotFoundBody))
		return
	}

	if doc {
		if preview {
			if err := h.queries.IncrementFileSuccessCount(r.Context(), file.Slug); err != nil {
				h.logger.Warn("increment success count", "error", err)
			}
		} else {
			if err := h.queries.IncrementFileCodeSuccessCount(r.Context(), file.Slug); err != nil {
				h.logger.Warn("increment code success count", "error", err)
			}
			if file.MaxViews.Valid {
				if err := h.queries.IncrementFileViewCount(r.Context(), file.Slug); err != nil {
					h.logger.Warn("increment view count", "error", err)
				}
			}
		}
	}

	setUserContentHeaders(w, site.ContentType(entry, data))
	hdr := w.Header()
	// Module scripts, fetch() and web fonts are CORS requests, and from the
	// sandbox's opaque origin they are cross-origin to this server: without
	// this header every one of them fails. See the top of this file for why it
	// is safe here, and never anywhere under /api.
	hdr.Set("Access-Control-Allow-Origin", "*")
	// Revalidate every time, so making the site private or changing its
	// access code takes effect on the next request rather than whenever a
	// cache gets round to it. ServeContent still answers 304 when nothing
	// changed.
	hdr.Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", file.UpdatedAt, bytes.NewReader(data))
}

func (h *FilesHandler) denySite(w http.ResponseWriter, r *http.Request, file sqlcgen.File, doc bool) {
	if doc {
		if err := h.queries.IncrementFileFailureCount(r.Context(), file.Slug); err != nil {
			h.logger.Warn("increment failure count", "error", err)
		}
	}
	setUserContentHeaders(w, htmlContentType)
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(accessDeniedBody))
}

func queryPart(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

// siteName decides what a new site is called: the name the uploader gave, else
// the <title> of its index.html, else "Untitled". An explicit name over the
// limit is an error; a long <title> is shortened instead, since the uploader
// never chose it.
func siteName(requested string, entries []site.Entry) (string, error) {
	name := strings.TrimSpace(requested)
	if name != "" {
		if len(name) > maxNameBytes {
			return "", fmt.Errorf("name must be at most %d bytes", maxNameBytes)
		}
		return name, nil
	}
	name = site.Title(entries)
	for len(name) > maxNameBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" {
		name = "Untitled"
	}
	return name, nil
}

// storeNewSite packs normalized entries and creates the site row, after the
// quota check. Shared by POST /api/sites and MCP upload_site. Errors wrapping
// errOverQuota are for the caller; anything else is internal.
func storeNewSite(ctx context.Context, queries *sqlcgen.Queries, user sqlcgen.User, name string, entries []site.Entry, isPublic bool) (sqlcgen.File, error) {
	archive, err := site.Pack(entries)
	if err != nil {
		return sqlcgen.File{}, fmt.Errorf("pack site: %w", err)
	}
	if err := enforceQuota(ctx, queries, user, int64(len(archive)), 0); err != nil {
		return sqlcgen.File{}, err
	}
	accessCode, err := auth.NewAccessCode()
	if err != nil {
		return sqlcgen.File{}, fmt.Errorf("generate access code: %w", err)
	}
	return createSiteWithFreshSlug(ctx, queries, sqlcgen.CreateSiteParams{
		Name:       name,
		Archive:    archive,
		IsPublic:   isPublic,
		AccessCode: accessCode,
		UserID:     user.ID,
	})
}

// readSiteUpload reads a zip request body and turns it into a normalized site.
// It writes the error response itself and reports whether to continue.
func readSiteUpload(w http.ResponseWriter, r *http.Request) ([]site.Entry, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, site.MaxUploadBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, fmt.Sprintf("zip exceeds %d MB", site.MaxUploadBytes>>20), http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "could not read request body", http.StatusBadRequest)
		return nil, false
	}
	entries, err := site.ReadZip(body)
	if err == nil {
		entries, err = site.Normalize(entries)
	}
	if err != nil {
		writeSiteError(w, err)
		return nil, false
	}
	return entries, true
}

func writeSiteError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if site.IsTooLarge(err) {
		status = http.StatusRequestEntityTooLarge
	}
	http.Error(w, err.Error(), status)
}

// CreateSite uploads a directory at POST /api/sites?name=..., the body being a
// zip of it. It lives outside /api/files for the same reason /api/search does:
// a static segment at the /api/files/{slug} level would shadow a file whose
// slug happens to match it.
func (h *FilesHandler) CreateSite(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	entries, ok := readSiteUpload(w, r)
	if !ok {
		return
	}
	name, err := siteName(r.URL.Query().Get("name"), entries)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Same default-visibility policy as a document upload over HTTP.
	file, err := storeNewSite(r.Context(), h.queries, user, name, entries,
		configBool(r, h.queries, ConfigUploadDefaultPublic))
	if err != nil {
		if errors.Is(err, errOverQuota) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		h.logger.Error("create site", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := toFileResponse(file)
	resp.Entries = site.Infos(entries)
	writeJSONStatus(w, http.StatusCreated, resp)
}

// ReplaceSite swaps a site's whole content for a new zip at
// PUT /api/files/{slug}/site. Name, slug, access code, visibility and limits
// are untouched.
func (h *FilesHandler) ReplaceSite(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")

	old, err := h.queries.GetUserFileSize(r.Context(), sqlcgen.GetUserFileSizeParams{
		Slug:   slug,
		UserID: user.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("get file size", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if old.Kind != KindSite {
		http.Error(w, "only a site's content can be replaced with a zip", http.StatusBadRequest)
		return
	}

	entries, ok := readSiteUpload(w, r)
	if !ok {
		return
	}
	archive, err := site.Pack(entries)
	if err != nil {
		h.logger.Error("pack site", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !h.checkQuota(w, r, user, int64(len(archive)), old.ContentSize) {
		return
	}

	file, err := h.queries.ReplaceSiteArchive(r.Context(), sqlcgen.ReplaceSiteArchiveParams{
		Archive: archive,
		Slug:    slug,
		UserID:  user.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("replace site", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := toFileResponse(file)
	resp.Entries = site.Infos(entries)
	writeJSON(w, resp)
}
