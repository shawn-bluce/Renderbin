package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/buildinfo"
	"github.com/shawn-bluce/renderbin/backend/internal/config"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

const (
	// mcpMaxBatchFiles caps upload_files; each file is separately capped at
	// the configured per-file limit, but the whole batch must also fit the MCP
	// request-body limit.
	mcpMaxBatchFiles = 20
	// mcpDefaultMaxRequestBytes bounds a /mcp request body — the SDK reads the whole
	// JSON-RPC message into memory and (as of v1.2.0) applies no limit itself.
	mcpDefaultMaxRequestBytes = 32 << 20
)

// errFileNotFound is returned both for slugs that don't exist and for files
// owned by another user, so MCP callers can't probe other users' slugs.
var errFileNotFound = errors.New("file not found in this project")

// MCPHandler serves the MCP (Model Context Protocol) endpoint at /mcp: a
// stateless streamable-HTTP server whose tools let an AI client manage the
// authenticated user's own files. Authentication is the per-user API key
// (users.api_key, issued on the settings page) as a Bearer token; the whole
// endpoint is gated on the mcp_enabled config.
type MCPHandler struct {
	queries *sqlcgen.Queries
	logger  *slog.Logger
	config  config.Runtime
}

// NewMCPHandler builds the /mcp http.Handler: body cap → mcp_enabled gate →
// Bearer-token auth → streamable MCP server with the eight file tools.
func NewMCPHandler(queries *sqlcgen.Queries, logger *slog.Logger, cfg config.Runtime) http.Handler {
	m := &MCPHandler{queries: queries, logger: logger, config: cfg}

	srv := mcp.NewServer(&mcp.Implementation{Name: "renderbin", Version: buildinfo.Version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "upload_file",
		Description: fmt.Sprintf("Upload a Markdown or HTML document of at most %dMB. The file starts private; the returned URL (with its access code) becomes publicly viewable once the file is published with publish_file.", cfg.MaxFileSizeMB),
	}, m.uploadFile)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "upload_files",
		Description: fmt.Sprintf("Upload up to 20 Markdown or HTML documents of at most %dMB each in one call. Files start private; returns each file's URL.", cfg.MaxFileSizeMB),
	}, m.uploadFiles)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_files",
		Description: "List your own documents, newest first, with each one's slug, format, visibility and access URL. Use search_files instead when you know part of a name or its content.",
	}, m.listFiles)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_files",
		Description: "Search your own documents by name and content. Returns each match's name and access URL.",
	}, m.searchFiles)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_file",
		Description: "Update a document's name and/or content, identified by its slug. The slug, kind, and access code stay unchanged.",
	}, m.updateFile)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "publish_file",
		Description: "Make a document publicly accessible and return its shareable URL. Optionally give the link a lifetime: either ttl (a number plus a unit: 36h, 1d, 2w, 6mo, 1y) or max_views, never both. When the limit is reached the file goes private again by itself.",
	}, m.publishFile)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "unpublish_file",
		Description: "Make a public document private again, so its URL stops working for everyone but you. The document itself is kept; use delete_file to remove it.",
	}, m.unpublishFile)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_file",
		Description: "Move a document to the trash. First call it without confirm: it returns the file's name and URL so you can ask the user to confirm. Only after the user explicitly confirms, call it again with confirm=true. Permanent deletion is not available over MCP.",
	}, m.deleteFile)

	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	withAuth := mcpauth.RequireBearerToken(m.verifyAPIKey, nil)(streamable)
	return m.requireMCPEnabled(capRequestBody(withAuth, mcpRequestBodyLimit(cfg.MaxFileSizeBytes)))
}

// requireMCPEnabled 403s every /mcp request while the mcp_enabled config is off.
func (m *MCPHandler) requireMCPEnabled(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !configBool(r, m.queries, ConfigMCPEnabled) {
			http.Error(w, "MCP is disabled", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mcpRequestBodyLimit(maxFileSizeBytes int64) int64 {
	return max(mcpDefaultMaxRequestBytes, maxFileBody(maxFileSizeBytes))
}

func capRequestBody(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// verifyAPIKey resolves a Bearer token to its user row. The user and the
// request-derived base URL travel to tool handlers via TokenInfo.Extra
// (surfaced as req.Extra.TokenInfo) — the SDK's supported identity path; the
// tool-handler ctx does not carry HTTP middleware values.
func (m *MCPHandler) verifyAPIKey(ctx context.Context, token string, r *http.Request) (*mcpauth.TokenInfo, error) {
	user, err := m.queries.GetUserByAPIKey(ctx, sql.NullString{String: token, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, mcpauth.ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	// A suspended account's key is as dead as its password. Without this,
	// suspension would close the browser door and leave the agent one open.
	if user.DisabledAt.Valid {
		return nil, mcpauth.ErrInvalidToken
	}
	return &mcpauth.TokenInfo{
		UserID: strconv.FormatInt(user.ID, 10),
		// API keys don't expire; this only satisfies the SDK's per-request
		// zero-expiration check and easily outlives the request it's made for.
		Expiration: time.Now().Add(time.Hour),
		Extra: map[string]any{
			"user":    user,
			"baseURL": publicShareBaseURL(r, m.config.PublicShareBaseURL),
		},
	}, nil
}

// mcpIdentity recovers the authenticated user and base URL stashed by
// verifyAPIKey. RequireBearerToken guarantees TokenInfo is present.
func mcpIdentity(req *mcp.CallToolRequest) (sqlcgen.User, string, error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return sqlcgen.User{}, "", errors.New("unauthenticated")
	}
	extra := req.Extra.TokenInfo.Extra
	user, uok := extra["user"].(sqlcgen.User)
	baseURL, bok := extra["baseURL"].(string)
	if !uok || !bok {
		return sqlcgen.User{}, "", errors.New("unauthenticated")
	}
	return user, baseURL, nil
}

func accessURL(baseURL string, f sqlcgen.File) string {
	return baseURL + "/res/" + f.Slug + "?code=" + f.AccessCode
}

// ownedFile loads the user's own non-deleted file. Ownership is enforced by
// the query itself, so a missing slug and someone else's slug are literally
// the same case: no row, hence the same errFileNotFound.
func (m *MCPHandler) ownedFile(ctx context.Context, user sqlcgen.User, slug string) (sqlcgen.File, error) {
	file, err := m.queries.GetUserFileBySlug(ctx, sqlcgen.GetUserFileBySlugParams{
		Slug:   slug,
		UserID: user.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return sqlcgen.File{}, errFileNotFound
	}
	if err != nil {
		m.logger.Error("mcp get file", "error", err)
		return sqlcgen.File{}, errors.New("internal error")
	}
	return file, nil
}

func textResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: fmt.Sprintf(format, args...)},
	}}
}

// --- upload ---

type mcpUploadInput struct {
	Name    string `json:"name,omitempty" jsonschema:"display name; defaults to Untitled"`
	Kind    string `json:"kind" jsonschema:"document format: markdown or html"`
	Content string `json:"content" jsonschema:"the raw document source, subject to the configured per-file size limit"`
}

type mcpFileInfo struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	IsPublic bool   `json:"is_public"`
	URL      string `json:"url" jsonschema:"access URL including the access code; anonymously viewable only while the file is public"`
}

func fileInfo(baseURL string, f sqlcgen.File) mcpFileInfo {
	return mcpFileInfo{Slug: f.Slug, Name: f.Name, Kind: f.Kind, IsPublic: f.IsPublic, URL: accessURL(baseURL, f)}
}

// normalizeMCPKind is stricter than the web API's normalizeKind: MCP uploads
// accept only markdown and html documents, and the kind must be explicit.
func normalizeMCPKind(k string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case KindMarkdown, "md":
		return KindMarkdown, true
	case KindHTML, "htm":
		return KindHTML, true
	default:
		return "", false
	}
}

func (m *MCPHandler) createUpload(ctx context.Context, user sqlcgen.User, in mcpUploadInput) (sqlcgen.File, error) {
	kind, ok := normalizeMCPKind(in.Kind)
	if !ok {
		return sqlcgen.File{}, errors.New("kind must be markdown or html")
	}
	if in.Content == "" {
		return sqlcgen.File{}, errors.New("content is required")
	}
	if int64(len(in.Content)) > m.config.MaxFileSizeBytes {
		return sqlcgen.File{}, fmt.Errorf("content exceeds %dMB", m.config.MaxFileSizeMB)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Untitled"
	}
	// The same bound the HTTP path applies. Without it an agent could store a
	// multi-kilobyte name that later turns every download of that file into a
	// 502 from the reverse proxy, whose header buffer it overflows.
	if len(name) > maxNameBytes {
		return sqlcgen.File{}, fmt.Errorf("name must be at most %d bytes", maxNameBytes)
	}
	// Storage limits are per account, not per protocol: an API key that could
	// ignore them would make users.quota_bytes decorative.
	if err := enforceQuota(ctx, m.queries, user, int64(len(in.Content)), 0); err != nil {
		if errors.Is(err, errOverQuota) {
			return sqlcgen.File{}, err
		}
		m.logger.Error("mcp check quota", "user_id", user.ID, "error", err)
		return sqlcgen.File{}, errors.New("internal error")
	}

	accessCode, err := auth.NewAccessCode()
	if err != nil {
		m.logger.Error("mcp generate access code", "error", err)
		return sqlcgen.File{}, errors.New("internal error")
	}
	file, err := createFileWithFreshSlug(ctx, m.queries, sqlcgen.CreateFileParams{
		Name:        name,
		HtmlContent: in.Content,
		Kind:        kind,
		// Always private, regardless of ConfigUploadDefaultPublic: the upload
		// tools promise "starts private" in their descriptions, and publish_file
		// is the agent's explicit consent step for making anything reachable.
		IsPublic:   false,
		AccessCode: accessCode,
		UserID:     user.ID,
	})
	if err != nil {
		m.logger.Error("mcp create file", "error", err)
		return sqlcgen.File{}, errors.New("internal error")
	}
	return file, nil
}

type uploadFileOutput struct {
	mcpFileInfo
}

func (m *MCPHandler) uploadFile(ctx context.Context, req *mcp.CallToolRequest, in mcpUploadInput) (*mcp.CallToolResult, uploadFileOutput, error) {
	user, baseURL, err := mcpIdentity(req)
	if err != nil {
		return nil, uploadFileOutput{}, err
	}
	file, err := m.createUpload(ctx, user, in)
	if err != nil {
		return nil, uploadFileOutput{}, err
	}
	out := uploadFileOutput{fileInfo(baseURL, file)}
	return textResult("Uploaded %q (private). URL: %s — call publish_file with slug %q to make it publicly viewable.",
		out.Name, out.URL, out.Slug), out, nil
}

type uploadFilesInput struct {
	Files []mcpUploadInput `json:"files" jsonschema:"the documents to upload, at most 20 per call"`
}

type uploadFilesResult struct {
	Name  string `json:"name"`
	Slug  string `json:"slug,omitempty"`
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty" jsonschema:"set when this file failed to upload"`
}

type uploadFilesOutput struct {
	Uploaded int                 `json:"uploaded"`
	Failed   int                 `json:"failed"`
	Results  []uploadFilesResult `json:"results"`
}

func (m *MCPHandler) uploadFiles(ctx context.Context, req *mcp.CallToolRequest, in uploadFilesInput) (*mcp.CallToolResult, uploadFilesOutput, error) {
	user, baseURL, err := mcpIdentity(req)
	if err != nil {
		return nil, uploadFilesOutput{}, err
	}
	if len(in.Files) == 0 {
		return nil, uploadFilesOutput{}, errors.New("files is required")
	}
	if len(in.Files) > mcpMaxBatchFiles {
		return nil, uploadFilesOutput{}, fmt.Errorf("at most %d files per call", mcpMaxBatchFiles)
	}

	out := uploadFilesOutput{Results: make([]uploadFilesResult, 0, len(in.Files))}
	var lines []string
	for _, f := range in.Files {
		file, err := m.createUpload(ctx, user, f)
		if err != nil {
			out.Failed++
			out.Results = append(out.Results, uploadFilesResult{Name: f.Name, Error: err.Error()})
			lines = append(lines, fmt.Sprintf("- %q failed: %s", f.Name, err))
			continue
		}
		out.Uploaded++
		out.Results = append(out.Results, uploadFilesResult{Name: file.Name, Slug: file.Slug, URL: accessURL(baseURL, file)})
		lines = append(lines, fmt.Sprintf("- %q → %s", file.Name, accessURL(baseURL, file)))
	}

	summary := fmt.Sprintf("%d uploaded, %d failed (all uploads start private; use publish_file to share):\n%s",
		out.Uploaded, out.Failed, strings.Join(lines, "\n"))
	return textResult("%s", summary), out, nil
}

// --- list ---

// mcpListLimit caps how many rows list_files returns. An agent's context is the
// real constraint here, so the tool reports the true total alongside the
// truncated list instead of pretending the cap is the whole story.
const mcpListLimit = 200

type listFilesInput struct{}

type listFilesOutput struct {
	Total     int           `json:"total" jsonschema:"how many documents you have in total"`
	Truncated bool          `json:"truncated" jsonschema:"true when total exceeds the returned page"`
	Files     []mcpFileInfo `json:"files"`
}

func (m *MCPHandler) listFiles(ctx context.Context, req *mcp.CallToolRequest, _ listFilesInput) (*mcp.CallToolResult, listFilesOutput, error) {
	user, baseURL, err := mcpIdentity(req)
	if err != nil {
		return nil, listFilesOutput{}, err
	}

	rows, err := m.queries.ListUserFiles(ctx, user.ID)
	if err != nil {
		m.logger.Error("mcp list files", "error", err)
		return nil, listFilesOutput{}, errors.New("internal error")
	}
	files := make([]sqlcgen.File, 0, len(rows))
	for _, row := range rows {
		files = append(files, listRowToFile(row))
	}
	if len(files) == 0 {
		return textResult("This project has no documents yet. Use upload_file to add one."),
			listFilesOutput{Files: []mcpFileInfo{}}, nil
	}

	out := listFilesOutput{Total: len(files), Truncated: len(files) > mcpListLimit}
	if out.Truncated {
		files = files[:mcpListLimit]
	}
	out.Files = make([]mcpFileInfo, 0, len(files))
	var lines []string
	for _, f := range files {
		info := fileInfo(baseURL, f)
		out.Files = append(out.Files, info)
		lines = append(lines, fmt.Sprintf("- %q [%s, %s]: %s", info.Name, info.Kind, visibilityLabel(f), info.URL))
	}

	summary := fmt.Sprintf("%d document(s):\n%s", out.Total, strings.Join(lines, "\n"))
	if out.Truncated {
		summary += fmt.Sprintf("\n(showing the %d newest of %d; use search_files to narrow it down)", mcpListLimit, out.Total)
	}
	return textResult("%s", summary), out, nil
}

// visibilityLabel describes a file's shareability in the one sentence a tool
// result has room for, including any self-expiring limit on the link.
func visibilityLabel(f sqlcgen.File) string {
	if !f.IsPublic {
		return "private -- publish_file required before the URL works for others"
	}
	switch {
	case f.ExpiresAt.Valid:
		return "public until " + f.ExpiresAt.Time.UTC().Format(timeLayout)
	case f.MaxViews.Valid:
		return fmt.Sprintf("public for %d more view(s)", max(0, f.MaxViews.Int64-f.ViewCount))
	default:
		return "public"
	}
}

// --- search ---

type searchFilesInput struct {
	Query string `json:"query" jsonschema:"substring to search for in document names and content"`
}

type searchFilesMatch struct {
	mcpFileInfo
	Snippet string `json:"snippet,omitempty" jsonschema:"content excerpt around the match, when the match is in the content"`
}

type searchFilesOutput struct {
	Found   bool               `json:"found"`
	Results []searchFilesMatch `json:"results"`
}

func (m *MCPHandler) searchFiles(ctx context.Context, req *mcp.CallToolRequest, in searchFilesInput) (*mcp.CallToolResult, searchFilesOutput, error) {
	user, baseURL, err := mcpIdentity(req)
	if err != nil {
		return nil, searchFilesOutput{}, err
	}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, searchFilesOutput{}, errors.New("query is required")
	}

	rows, err := m.queries.SearchUserFilesWithContent(ctx, sqlcgen.SearchUserFilesWithContentParams{
		UserID:       user.ID,
		NameQuery:    q,
		ContentQuery: q,
	})
	if err != nil {
		m.logger.Error("mcp search files", "error", err)
		return nil, searchFilesOutput{}, errors.New("internal error")
	}

	if len(rows) == 0 {
		return textResult("No documents matching %q were found in this project.", q),
			searchFilesOutput{Found: false, Results: []searchFilesMatch{}}, nil
	}

	out := searchFilesOutput{Found: true, Results: make([]searchFilesMatch, 0, len(rows))}
	var lines []string
	for _, row := range rows {
		f := contentSearchRowToFile(row)
		match := searchFilesMatch{mcpFileInfo: fileInfo(baseURL, f)}
		if !containsFold(f.Name, q) {
			match.Snippet = snippetFromWindow(row.SnippetWindow, q, row.MatchPos, row.ContentChars)
		}
		out.Results = append(out.Results, match)
		lines = append(lines, fmt.Sprintf("- %q (%s): %s", f.Name, visibilityLabel(f), match.URL))
	}
	return textResult("Found %d document(s) matching %q:\n%s", len(rows), q, strings.Join(lines, "\n")), out, nil
}

// --- update ---

type updateFileInput struct {
	Slug    string `json:"slug" jsonschema:"the document's slug (the path segment of its URL)"`
	Name    string `json:"name,omitempty" jsonschema:"new display name; omit to keep the current one"`
	Content string `json:"content,omitempty" jsonschema:"new document source, subject to the configured per-file size limit; omit to keep the current one"`
}

func (m *MCPHandler) updateFile(ctx context.Context, req *mcp.CallToolRequest, in updateFileInput) (*mcp.CallToolResult, mcpFileInfo, error) {
	user, baseURL, err := mcpIdentity(req)
	if err != nil {
		return nil, mcpFileInfo{}, err
	}
	if in.Name == "" && in.Content == "" {
		return nil, mcpFileInfo{}, errors.New("provide a new name, new content, or both")
	}
	if int64(len(in.Content)) > m.config.MaxFileSizeBytes {
		return nil, mcpFileInfo{}, fmt.Errorf("content exceeds %dMB", m.config.MaxFileSizeMB)
	}

	file, err := m.ownedFile(ctx, user, in.Slug)
	if err != nil {
		return nil, mcpFileInfo{}, err
	}

	name := file.Name
	if in.Name != "" {
		name = strings.TrimSpace(in.Name)
	}
	if len(name) > maxNameBytes {
		return nil, mcpFileInfo{}, fmt.Errorf("name must be at most %d bytes", maxNameBytes)
	}
	content := file.HtmlContent
	if in.Content != "" {
		content = in.Content
	}
	// file.ContentSize is what this write replaces, so rewriting a document
	// with one of the same size is not charged twice.
	if err := enforceQuota(ctx, m.queries, user, int64(len(content)), file.ContentSize); err != nil {
		if errors.Is(err, errOverQuota) {
			return nil, mcpFileInfo{}, err
		}
		m.logger.Error("mcp check quota", "user_id", user.ID, "error", err)
		return nil, mcpFileInfo{}, errors.New("internal error")
	}
	updated, err := m.queries.UpdateFile(ctx, sqlcgen.UpdateFileParams{
		Name:        name,
		NewSlug:     file.Slug, // slug and access code deliberately unchanged over MCP
		HtmlContent: content,
		AccessCode:  file.AccessCode,
		Slug:        file.Slug,
		UserID:      user.ID,
	})
	if err != nil {
		m.logger.Error("mcp update file", "error", err)
		return nil, mcpFileInfo{}, errors.New("internal error")
	}

	out := fileInfo(baseURL, updated)
	return textResult("Updated %q. URL: %s", out.Name, out.URL), out, nil
}

// --- publish ---

type publishFileInput struct {
	Slug string `json:"slug" jsonschema:"the document's slug (the path segment of its URL)"`
	// Pointers, so "field omitted" is distinguishable from "24h"/0 and the
	// mutual-exclusion check below sees what the caller actually sent.
	TTL      *string `json:"ttl,omitempty" jsonschema:"optional link lifetime as a number plus a unit - h, d, w, mo or y (e.g. 36h, 1d, 2w, 6mo, 1y), at most 10 years. Mutually exclusive with max_views"`
	MaxViews *int64  `json:"max_views,omitempty" jsonschema:"optional number of anonymous views the link allows before going private. Mutually exclusive with ttl"`
}

func (m *MCPHandler) publishFile(ctx context.Context, req *mcp.CallToolRequest, in publishFileInput) (*mcp.CallToolResult, mcpFileInfo, error) {
	user, baseURL, err := mcpIdentity(req)
	if err != nil {
		return nil, mcpFileInfo{}, err
	}
	// Validated before the lookup so a bad ttl can't publish the file anyway.
	limit, errMsg := parseExpiryLimit(in.TTL, in.MaxViews)
	if errMsg != "" {
		return nil, mcpFileInfo{}, errors.New(errMsg)
	}
	file, err := m.ownedFile(ctx, user, in.Slug)
	if err != nil {
		return nil, mcpFileInfo{}, err
	}

	// SetFileExpiry publishes as part of setting a limit, so a limited publish
	// is one statement rather than a visibility write followed by an expiry
	// write that could leave the file public with no limit if the second failed.
	var published sqlcgen.File
	if limit.set {
		published, err = m.queries.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{
			ExpiresAt: limit.expiresAt,
			MaxViews:  limit.maxViews,
			Slug:      file.Slug,
			UserID:    user.ID,
		})
	} else {
		published, err = m.queries.SetFileVisibility(ctx, sqlcgen.SetFileVisibilityParams{
			IsPublic: true,
			Slug:     file.Slug,
			UserID:   user.ID,
		})
	}
	if err != nil {
		m.logger.Error("mcp publish file", "error", err)
		return nil, mcpFileInfo{}, errors.New("internal error")
	}

	out := fileInfo(baseURL, published)
	return textResult("Published %q (%s). Anyone with this URL can now view it: %s",
		out.Name, visibilityLabel(published), out.URL), out, nil
}

// --- unpublish ---

type unpublishFileInput struct {
	Slug string `json:"slug" jsonschema:"the document's slug (the path segment of its URL)"`
}

// unpublishFile is publish_file's inverse: visibility only. Any expiry limit is
// left alone, so re-publishing later resumes the same countdown rather than
// silently handing out an unlimited link.
func (m *MCPHandler) unpublishFile(ctx context.Context, req *mcp.CallToolRequest, in unpublishFileInput) (*mcp.CallToolResult, mcpFileInfo, error) {
	user, baseURL, err := mcpIdentity(req)
	if err != nil {
		return nil, mcpFileInfo{}, err
	}
	file, err := m.ownedFile(ctx, user, in.Slug)
	if err != nil {
		return nil, mcpFileInfo{}, err
	}

	updated, err := m.queries.SetFileVisibility(ctx, sqlcgen.SetFileVisibilityParams{
		IsPublic: false,
		Slug:     file.Slug,
		UserID:   user.ID,
	})
	if err != nil {
		m.logger.Error("mcp unpublish file", "error", err)
		return nil, mcpFileInfo{}, errors.New("internal error")
	}

	out := fileInfo(baseURL, updated)
	return textResult("Unpublished %q. Its URL now works only for you; the document itself is untouched.", out.Name), out, nil
}

// --- delete ---

type deleteFileInput struct {
	Slug    string `json:"slug" jsonschema:"the document's slug (the path segment of its URL)"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to actually delete; call without it first and ask the user to confirm"`
}

type deleteFileOutput struct {
	mcpFileInfo
	Deleted bool `json:"deleted"`
}

func (m *MCPHandler) deleteFile(ctx context.Context, req *mcp.CallToolRequest, in deleteFileInput) (*mcp.CallToolResult, deleteFileOutput, error) {
	user, baseURL, err := mcpIdentity(req)
	if err != nil {
		return nil, deleteFileOutput{}, err
	}
	// Re-verify ownership on both phases: the server is stateless, so the
	// confirm=true call must stand on its own.
	file, err := m.ownedFile(ctx, user, in.Slug)
	if err != nil {
		return nil, deleteFileOutput{}, err
	}
	out := deleteFileOutput{mcpFileInfo: fileInfo(baseURL, file)}

	if !in.Confirm {
		return textResult("About to move %q (%s) to the trash. Ask the user to confirm, then call delete_file again with confirm=true. Nothing has been deleted yet.",
			out.Name, out.URL), out, nil
	}

	rows, err := m.queries.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{
		Slug:   file.Slug,
		UserID: user.ID,
	})
	if err != nil {
		m.logger.Error("mcp delete file", "error", err)
		return nil, deleteFileOutput{}, errors.New("internal error")
	}
	if rows == 0 {
		// Trashed between the ownedFile lookup and here.
		return nil, deleteFileOutput{}, errFileNotFound
	}
	out.Deleted = true
	return textResult("Moved %q to the trash. It can be restored from the web UI; permanent deletion is not available over MCP.", out.Name), out, nil
}
