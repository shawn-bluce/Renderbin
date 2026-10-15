-- ===========================================================================
-- OWNER-SCOPED -- everything reachable from a request behind requireAuth.
--
-- Every query below carries `user_id = sqlc.arg(user_id)`, so a slug belonging
-- to another user simply doesn't match: reads return sql.ErrNoRows and writes
-- affect 0 rows, which handlers turn into the same 404 a nonexistent slug
-- gets. Ownership therefore lives in the schema, not in a Go check a caller
-- can forget -- and because sqlc runs with emit_interface: false, dropping the
-- predicate from a query breaks the build at every call site.
--
-- New file queries belong in this section and MUST carry the predicate.
-- ===========================================================================

-- content_size is derived from html_content by the statement itself, here and
-- in UpdateFile, so the two can never drift: there is no way to write the
-- content through this schema without writing its size in the same statement.
-- length(CAST(x AS BLOB)) is bytes; bare length() on TEXT counts characters.
-- name: CreateFile :one
INSERT INTO files (slug, name, html_content, content_size, kind, is_public, access_code, user_id)
VALUES (
    sqlc.arg(slug),
    sqlc.arg(name),
    sqlc.arg(html_content),
    length(CAST(sqlc.arg(html_content) AS BLOB)),
    sqlc.arg(kind),
    sqlc.arg(is_public),
    sqlc.arg(access_code),
    sqlc.arg(user_id)
)
RETURNING *;

-- A directory site: html_content stays '' and the whole directory is one zip
-- in archive (see internal/site). content_size is again derived by the
-- statement, from the archive this time; length() of a BLOB is bytes.
-- name: CreateSite :one
INSERT INTO files (slug, name, html_content, archive, content_size, kind, is_public, access_code, user_id)
VALUES (
    sqlc.arg(slug),
    sqlc.arg(name),
    '',
    sqlc.arg(archive),
    length(sqlc.arg(archive)),
    'site',
    sqlc.arg(is_public),
    sqlc.arg(access_code),
    sqlc.arg(user_id)
)
RETURNING *;

-- The two listings and the name search deliberately do NOT select
-- html_content. They used to be SELECT *, which read every document's full
-- source out of SQLite so the handler could blank the field before encoding --
-- 350MB of RSS to answer a 13KB request for an account holding 160MB. Anything
-- added here must stay off html_content; GetUserFileBySlug is the endpoint that
-- returns content, and it returns one row.
-- name: ListUserFiles :many
SELECT id, slug, name, is_public, access_code, created_at, updated_at, deleted_at,
       success_count, failure_count, tags, expires_at, max_views, view_count,
       kind, code_success_count, user_id, expired_at, expired_reason, content_size
FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
ORDER BY created_at DESC;

-- name: ListUserDeletedFiles :many
SELECT id, slug, name, is_public, access_code, created_at, updated_at, deleted_at,
       success_count, failure_count, tags, expires_at, max_views, view_count,
       kind, code_success_count, user_id, expired_at, expired_reason, content_size
FROM files
WHERE deleted_at IS NOT NULL AND user_id = sqlc.arg(user_id)
ORDER BY deleted_at DESC;

-- name: GetUserFileBySlug :one
SELECT * FROM files
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL;

-- The caller's total stored bytes, used to enforce users.quota_bytes. Trashed
-- rows are included on purpose: they still occupy the database, and emptying
-- the trash is a user action, so excluding them would make soft-delete an
-- unlimited quota bypass.
-- name: SumUserContentSize :one
SELECT CAST(COALESCE(SUM(content_size), 0) AS INTEGER) AS used_bytes FROM files
WHERE user_id = sqlc.arg(user_id);

-- What an edit is replacing, so the quota check can subtract it instead of
-- charging the account twice for a file it already stores -- plus the kind,
-- since a site is edited through different statements than a document.
-- name: GetUserFileSize :one
SELECT content_size, kind FROM files
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL;

-- Plain substring search via instr() rather than LIKE: no wildcard/escape
-- handling needed, and lower() gives the same ASCII-only case-insensitivity
-- LIKE would.

-- name: SearchUserFilesByName :many
SELECT id, slug, name, is_public, access_code, created_at, updated_at, deleted_at,
       success_count, failure_count, tags, expires_at, max_views, view_count,
       kind, code_success_count, user_id, expired_at, expired_reason, content_size
FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
  AND instr(lower(name), lower(sqlc.arg(name_query))) > 0
ORDER BY created_at DESC;

-- Content search returns a bounded window around the first match instead of
-- the whole document, for the same reason the listings dropped html_content: a
-- query matching fifty 5MB files would otherwise pull 250MB into memory to
-- render fifty 200-character excerpts.
--
-- match_pos is instr()'s 1-based character offset (0 when only the name
-- matched); snippet_window is up to 100 characters either side of the match.
-- Both instr() and substr() count characters here, not bytes, so the window is
-- rune-safe and the handler only has to decide about ellipses.
-- name: SearchUserFilesWithContent :many
SELECT id, slug, name, is_public, access_code, created_at, updated_at, deleted_at,
       success_count, failure_count, tags, expires_at, max_views, view_count,
       kind, code_success_count, user_id, expired_at, expired_reason, content_size,
       CAST(instr(lower(html_content), lower(sqlc.arg(content_query))) AS INTEGER) AS match_pos,
       CAST(length(html_content) AS INTEGER) AS content_chars,
       CAST(substr(html_content,
              MAX(1, instr(lower(html_content), lower(sqlc.arg(content_query))) - 100),
              length(sqlc.arg(content_query)) + 200) AS TEXT) AS snippet_window
FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
  AND (instr(lower(name), lower(sqlc.arg(name_query))) > 0
    OR instr(lower(html_content), lower(sqlc.arg(content_query))) > 0)
ORDER BY created_at DESC;

-- name: RenameFile :one
UPDATE files
SET name = sqlc.arg(name), updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
RETURNING *;

-- name: SetFileVisibility :one
UPDATE files
SET is_public = sqlc.arg(is_public), updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
RETURNING *;

-- name: SetFileTags :one
UPDATE files
SET tags = sqlc.arg(tags), updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
RETURNING *;

-- name: RefreshFileAccessCode :one
UPDATE files
SET access_code = sqlc.arg(access_code), updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
RETURNING *;

-- The SET slug and the WHERE slug are different values; naming both explicitly
-- keeps them from being mixed up (and avoids sqlc's generated `Slug_2` field).
--
-- kind <> 'site' keeps document writes off site rows in SQL rather than in a
-- Go check someone can forget: a site's html_content is '' by design, so a
-- rename that wrote the unchanged "content" back would otherwise be a write
-- of that empty string -- and a site whose archive is left alone but whose
-- size is recomputed from html_content stops counting against the quota.
-- name: UpdateFile :one
UPDATE files
SET name = sqlc.arg(name),
    slug = sqlc.arg(new_slug),
    html_content = sqlc.arg(html_content),
    content_size = length(CAST(sqlc.arg(html_content) AS BLOB)) + COALESCE(length(archive), 0),
    access_code = sqlc.arg(access_code),
    updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
  AND kind <> 'site'
RETURNING *;

-- A site's counterpart to UpdateFile: the same name/slug/access-code edit,
-- without content, which for a site is replaced only as a whole archive.
-- name: UpdateSiteMeta :one
UPDATE files
SET name = sqlc.arg(name),
    slug = sqlc.arg(new_slug),
    access_code = sqlc.arg(access_code),
    updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
  AND kind = 'site'
RETURNING *;

-- name: ReplaceSiteArchive :one
UPDATE files
SET archive = sqlc.arg(archive),
    content_size = length(sqlc.arg(archive)),
    updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
  AND kind = 'site'
RETURNING *;

-- Both expiry writers clear expired_at/expired_reason: that marker records the
-- last time a limit took the file offline, and leaving it set next to a freshly
-- configured (or freshly removed) limit would contradict the file's own state.
-- name: SetFileExpiry :one
UPDATE files
SET is_public = 1,
    expires_at = sqlc.arg(expires_at),
    max_views = sqlc.arg(max_views),
    view_count = 0,
    expired_at = NULL,
    expired_reason = '',
    updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
RETURNING *;

-- name: ClearFileExpiry :one
UPDATE files
SET expires_at = NULL,
    max_views = NULL,
    view_count = 0,
    expired_at = NULL,
    expired_reason = '',
    updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
RETURNING *;

-- :execrows rather than :exec so the handler can tell "soft-deleted it" from
-- "matched nothing" and 404 instead of reporting a success-shaped no-op.
-- name: SoftDeleteFile :execrows
UPDATE files
SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL;

-- name: RestoreFile :one
UPDATE files
SET deleted_at = NULL, updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NOT NULL
RETURNING *;

-- name: HardDeleteFile :execrows
DELETE FROM files
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NOT NULL;

-- Empty the trash. deleted_at IS NOT NULL is the whole safety property here:
-- this is the only unbounded delete in the file, so if that predicate is ever
-- dropped it takes the user's live files with it.
-- name: HardDeleteUserTrash :execrows
DELETE FROM files
WHERE user_id = sqlc.arg(user_id) AND deleted_at IS NOT NULL;

-- ===========================================================================
-- ADMIN CASCADE -- account deletion only (DELETE /api/admin/users/{id}).
--
-- This one carries user_id like everything above it, but the id is the
-- *target account's*, never the caller's, so the owner-scoping argument that
-- protects the section above does not apply here: the predicate cannot stop a
-- caller from naming someone else, only the super-admin check in the handler
-- can. It also has no deleted_at guard -- deleting an account takes its trash
-- with it, which is the point -- so unlike HardDeleteUserTrash there is no
-- second predicate standing between a mistake and every file the account owns.
-- Call it from AdminHandler.Delete and nowhere else, inside its transaction.
-- ===========================================================================

-- name: DeleteUserFiles :execrows
DELETE FROM files
WHERE user_id = sqlc.arg(user_id);

-- ===========================================================================
-- UNSCOPED -- the public render path (GET /res/{slug}) only.
--
-- These serve anonymous visitors holding a share link, who have no user at
-- all, so they cannot be owner-scoped without breaking every public link.
-- NEVER call anything below this line from a handler behind requireAuth --
-- that is exactly how the IDOR this section exists to prevent was introduced.
-- See FilesHandler.Render.
-- ===========================================================================

-- The join is the enforcement point for account suspension: a suspended
-- owner's files simply match no row here, so every one of their share links
-- becomes the same 404 a slug that never existed gets. Doing it in SQL rather
-- than as a Go check after the fact means the render path cannot forget.
-- name: GetFileBySlugAnyOwner :one
SELECT files.* FROM files
JOIN users ON users.id = files.user_id
WHERE files.slug = ? AND files.deleted_at IS NULL AND users.disabled_at IS NULL;

-- The site render path in two halves, so a request does not load the whole
-- archive before it knows it needs a byte of it. Every stylesheet, script and
-- image of a site is its own request, and with Cache-Control: no-cache each
-- one revalidates on every visit; answering those 304s used to read the full
-- zip each time. GetSiteBySlugAnyOwner carries everything access control and
-- the conditional check need (same columns as ListUserFiles, so the handler
-- converts it the same way); GetSiteArchive runs only when bytes are served.
-- Both keep the users join: suspension has to hold on either half.
-- name: GetSiteBySlugAnyOwner :one
SELECT files.id, files.slug, files.name, files.is_public, files.access_code,
       files.created_at, files.updated_at, files.deleted_at, files.success_count,
       files.failure_count, files.tags, files.expires_at, files.max_views,
       files.view_count, files.kind, files.code_success_count, files.user_id,
       files.expired_at, files.expired_reason, files.content_size
FROM files
JOIN users ON users.id = files.user_id
WHERE files.slug = ? AND files.kind = 'site'
  AND files.deleted_at IS NULL AND users.disabled_at IS NULL;

-- updated_at comes back with the archive because it is the Last-Modified of
-- these exact bytes, should the site have been replaced between the halves.
-- name: GetSiteArchive :one
SELECT files.archive, files.updated_at FROM files
JOIN users ON users.id = files.user_id
WHERE files.id = ? AND files.kind = 'site'
  AND files.deleted_at IS NULL AND users.disabled_at IS NULL;

-- name: IncrementFileSuccessCount :exec
UPDATE files
SET success_count = success_count + 1
WHERE slug = ? AND deleted_at IS NULL;

-- name: IncrementFileCodeSuccessCount :exec
UPDATE files
SET code_success_count = code_success_count + 1
WHERE slug = ? AND deleted_at IS NULL;

-- name: IncrementFileFailureCount :exec
UPDATE files
SET failure_count = failure_count + 1
WHERE slug = ? AND deleted_at IS NULL;

-- name: IncrementFileViewCount :exec
UPDATE files
SET view_count = view_count + 1
WHERE slug = ? AND deleted_at IS NULL;

-- Taking a file offline records why, overwriting any earlier event: only the
-- most recent one is kept, which is all the dashboard badge claims to show.
-- name: ExpireFile :exec
UPDATE files
SET is_public = 0,
    expires_at = NULL,
    max_views = NULL,
    expired_at = CURRENT_TIMESTAMP,
    expired_reason = sqlc.arg(expired_reason)
WHERE slug = sqlc.arg(slug) AND deleted_at IS NULL;
