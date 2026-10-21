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
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
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
		h.deny(w, r, file, true)
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

	// The row only: the archive is opened below, once a response actually
	// needs its bytes.
	file, err := h.queries.GetSiteBySlugAnyOwner(r.Context(), slug)
	if err != nil {
		// A miss is deliberately without setUserContentHeaders: nothing
		// user-authored is served, and the app's own X-Frame-Options: DENY stays on.
		queryError(w, h.logger, "get site", err)
		return
	}

	entry := sub
	if entry == "" || strings.HasSuffix(entry, "/") {
		entry += site.IndexFile
	}
	doc := isDocumentRequest(r, entry)

	// Lazy expiry, as in Render: the deadline applies to every request, the
	// view limit only to page views.
	h.lazyExpire(r.Context(), &file, doc)

	preview := false
	switch {
	case strings.HasPrefix(tok, previewPrefix):
		preview = h.verifyPreview(r.Context(), tok, file)
		if !preview {
			h.deny(w, r, file, doc)
			return
		}
	case file.IsPublic && accessCodeMatches(file.AccessCode, tok):
	default:
		h.deny(w, r, file, doc)
		return
	}

	if !hasSlash {
		// /res/s/TOKEN -> /res/s/TOKEN/, or index.html's relative references
		// would resolve one level too high and lose the token.
		http.Redirect(w, r, sitePath(slug, tok, "")+queryPart(r), http.StatusFound)
		return
	}

	// The archive cannot have changed since the browser cached this entry
	// (updated_at moves on every replacement), so neither the bytes nor
	// whether the entry exists can differ from what it already holds: answer
	// without opening the zip. A page view still counts, as it did when
	// ServeContent made the same decision after the load.
	if notModifiedSince(r, file.UpdatedAt) {
		if doc {
			h.recordView(r.Context(), file, preview)
		}
		setSiteHeaders(w, "")
		varyOnDest(w, entry)
		w.Header().Del("Content-Type")
		w.Header().Set("Last-Modified", file.UpdatedAt.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// Read through the blob on disk: the central directory and this one
	// entry, not the whole zip. The blob is the one this row named, and an
	// archive that has since been replaced is still on disk for the
	// collector's grace period, so the row and the bytes agree.
	blob, err := h.blobs.Open(file.BlobID)
	if err != nil {
		serverError(w, h.logger, "open site archive", err, "slug", file.Slug)
		return
	}
	defer blob.Close()
	arc, err := openSiteBlob(blob)
	if err != nil {
		serverError(w, h.logger, "open site archive", err, "slug", file.Slug)
		return
	}
	data, found, err := arc.Read(entry)
	if err != nil {
		serverError(w, h.logger, "read site entry", err, "slug", file.Slug)
		return
	}
	if !found {
		// A directory requested without its trailing slash: redirect, for the
		// same relative-reference reason as above.
		if sub != "" && !strings.HasSuffix(sub, "/") && arc.Has(sub+"/"+site.IndexFile) {
			http.Redirect(w, r, sitePath(slug, tok, sub+"/")+queryPart(r), http.StatusFound)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		writeUserHTML(w, http.StatusNotFound, []byte(siteNotFoundBody))
		return
	}

	contentType := site.ContentType(entry, data)
	if doc {
		h.recordView(r.Context(), file, preview)
		// Only pages get the QR widget: HTML pulled in by fetch() is a
		// fragment the site's own script splices somewhere.
		note := qrNoteNone
		if preview {
			note = qrNotePreview
		}
		data = h.withQRWidget(r.Context(), data, note)
	}

	setSiteHeaders(w, contentType)
	varyOnDest(w, entry)
	http.ServeContent(w, r, "", file.UpdatedAt, bytes.NewReader(data))
}

// openSiteBlob reads a site's zip through its open blob file.
func openSiteBlob(blob *os.File) (*site.Archive, error) {
	info, err := blob.Stat()
	if err != nil {
		return nil, err
	}
	return site.Open(blob, info.Size())
}

// siteEntries lists what a site holds, from its zip's central directory.
func (h *FilesHandler) siteEntries(file sqlcgen.File) ([]site.EntryInfo, error) {
	blob, err := h.blobs.Open(file.BlobID)
	if err != nil {
		return nil, err
	}
	defer blob.Close()
	arc, err := openSiteBlob(blob)
	if err != nil {
		return nil, err
	}
	return arc.List(), nil
}

// setSiteHeaders sets what every served site entry carries, a 304 included.
func setSiteHeaders(w http.ResponseWriter, contentType string) {
	setUserContentHeaders(w, contentType)
	hdr := w.Header()
	// Module scripts, fetch() and web fonts are CORS requests, and from the
	// sandbox's opaque origin they are cross-origin to this server: without
	// this header every one of them fails. See the top of this file for why it
	// is safe here, and never anywhere under /api.
	hdr.Set("Access-Control-Allow-Origin", "*")
	// Revalidate every time, so making the site private or changing its
	// access code takes effect on the next request rather than whenever a
	// cache gets round to it. Unchanged entries still get a 304.
	hdr.Set("Cache-Control", "no-cache")
}

// varyOnDest tells caches that an HTML entry's bytes depend on how it was
// requested: a page view carries the QR widget, a fetch() of the same URL does
// not, and neither may be answered with the other's cached copy.
//
// The widget is not part of the validator, so turning it on or off reaches a
// page a browser already holds only once the site's archive next changes:
// until then a revalidation is still a 304.
func varyOnDest(w http.ResponseWriter, entry string) {
	if site.IsHTML(entry) {
		w.Header().Add("Vary", "Sec-Fetch-Dest")
	}
}

// notModifiedSince is the If-Modified-Since half of http.ServeContent's
// precondition check, run before the archive is loaded. Anything it does not
// decide -- an If-None-Match, a malformed date -- returns false and goes the
// long way, where ServeContent applies the full rules.
func notModifiedSince(r *http.Request, modtime time.Time) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.Header.Get("If-None-Match") != "" {
		return false
	}
	ims, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
	if err != nil {
		return false
	}
	// Last-Modified has whole-second resolution; compare at the same.
	return !modtime.Truncate(time.Second).After(ims)
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
		return name, nameTooLong(name)
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
func storeNewSite(ctx context.Context, queries *sqlcgen.Queries, store *blobstore.Store, user sqlcgen.User, name string, entries []site.Entry, isPublic bool) (sqlcgen.File, error) {
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
	blobID, err := storeContent(ctx, queries, store, archive)
	if err != nil {
		return sqlcgen.File{}, err
	}
	return withFreshSlug(func(slug string) (sqlcgen.File, error) {
		return queries.CreateSite(ctx, sqlcgen.CreateSiteParams{
			Slug:       slug,
			Name:       name,
			BlobID:     blobID,
			IsPublic:   isPublic,
			AccessCode: accessCode,
			UserID:     user.ID,
		})
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

// siteStillThere tells "replaced meanwhile" (409) from "deleted meanwhile"
// (404) after ReplaceSiteBlob's compare-and-swap matched no row.
func (h *FilesHandler) siteStillThere(r *http.Request, user sqlcgen.User, slug string) bool {
	_, err := h.queries.GetUserFileSize(r.Context(), sqlcgen.GetUserFileSizeParams{Slug: slug, UserID: user.ID})
	return err == nil
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
	file, err := storeNewSite(r.Context(), h.queries, h.blobs, user, name, entries,
		configBool(r.Context(), h.queries, ConfigUploadDefaultPublic))
	if err != nil {
		if errors.Is(err, errOverQuota) {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		serverError(w, h.logger, "create site", err)
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

	old, ok := h.ownedFileSize(w, r, user, slug)
	if !ok {
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
		serverError(w, h.logger, "pack site", err)
		return
	}
	if !h.checkQuota(w, r, user, int64(len(archive)), old.ContentSize) {
		return
	}
	blobID, err := storeContent(r.Context(), h.queries, h.blobs, archive)
	if err != nil {
		serverError(w, h.logger, "replace site", err)
		return
	}

	file, err := h.queries.ReplaceSiteBlob(r.Context(), sqlcgen.ReplaceSiteBlobParams{
		BlobID:    blobID,
		Slug:      slug,
		UserID:    user.ID,
		OldBlobID: old.BlobID,
	})
	if errors.Is(err, sql.ErrNoRows) && h.siteStillThere(r, user, slug) {
		http.Error(w, "the site was changed while this upload was processed; reload it and try again", http.StatusConflict)
		return
	}
	if err != nil {
		queryError(w, h.logger, "replace site", err)
		return
	}

	resp := toFileResponse(file)
	resp.Entries = site.Infos(entries)
	writeJSON(w, resp)
}
