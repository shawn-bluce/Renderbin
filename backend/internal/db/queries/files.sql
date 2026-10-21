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

-- content_size is read from the blob's own record by the statement itself,
-- here and in every statement that changes blob_id, so there is no way to
-- point a row at content without writing that content's size in the same
-- statement -- and 0010's check triggers refuse a size that disagrees anyway.
-- The blob is written to disk and recorded in blobs (InsertBlob) first; see
-- internal/blobstore for why that order makes a crash harmless.
-- name: CreateFile :one
INSERT INTO files (slug, name, blob_id, content_size, kind, is_public, access_code, user_id)
VALUES (
    sqlc.arg(slug),
    sqlc.arg(name),
    sqlc.arg(blob_id),
    (SELECT size FROM blobs WHERE id = sqlc.arg(blob_id)),
    sqlc.arg(kind),
    sqlc.arg(is_public),
    sqlc.arg(access_code),
    sqlc.arg(user_id)
)
RETURNING *;

-- A directory site: the blob is the whole directory as one zip (see
-- internal/site).
-- name: CreateSite :one
INSERT INTO files (slug, name, blob_id, content_size, kind, is_public, access_code, user_id)
VALUES (
    sqlc.arg(slug),
    sqlc.arg(name),
    sqlc.arg(blob_id),
    (SELECT size FROM blobs WHERE id = sqlc.arg(blob_id)),
    'site',
    sqlc.arg(is_public),
    sqlc.arg(access_code),
    sqlc.arg(user_id)
)
RETURNING *;

-- The listings are SELECT * again. They used to name every column but
-- html_content, because reading a row meant reading its document; the content
-- now lives in blobs on disk, and a files row is a few hundred bytes.
-- name: ListUserFiles :many
SELECT * FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
ORDER BY created_at DESC;

-- name: ListUserDeletedFiles :many
SELECT * FROM files
WHERE deleted_at IS NOT NULL AND user_id = sqlc.arg(user_id)
ORDER BY deleted_at DESC;

-- Paged listings, for the dashboard (GET /api/files?limit=...). Keyset
-- pagination: each page starts after the last row of the one before, named by
-- its sort key and id (after_id = 0 for the first page), so a file created or
-- deleted between two requests neither repeats nor skips a row the way an
-- OFFSET would. One statement per sort order, because ORDER BY cannot take a
-- parameter; id breaks ties, so the order is total.
--
-- visibility is 'all', 'public' or 'private'. "Recent" is id order: ids are
-- issued in creation order, so it is created_at order with exact tie-breaks.
-- Names sort with NOCASE (ASCII-only case folding, then code points), which is
-- what SQLite has; there is no ICU collation in a pure-Go build.

-- name: PageUserFilesRecent :many
SELECT * FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(visibility) AS TEXT) = 'all' OR is_public = (CAST(sqlc.arg(visibility) AS TEXT) = 'public'))
  AND (CAST(sqlc.arg(after_id) AS INTEGER) = 0 OR id < CAST(sqlc.arg(after_id) AS INTEGER))
ORDER BY id DESC
LIMIT sqlc.arg(page_size);

-- name: PageUserFilesNameAsc :many
SELECT * FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(visibility) AS TEXT) = 'all' OR is_public = (CAST(sqlc.arg(visibility) AS TEXT) = 'public'))
  AND (CAST(sqlc.arg(after_id) AS INTEGER) = 0
    OR name > CAST(sqlc.arg(after_name) AS TEXT) COLLATE NOCASE
    OR (name = CAST(sqlc.arg(after_name) AS TEXT) COLLATE NOCASE AND id > CAST(sqlc.arg(after_id) AS INTEGER)))
ORDER BY name COLLATE NOCASE ASC, id ASC
LIMIT sqlc.arg(page_size);

-- name: PageUserFilesNameDesc :many
SELECT * FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(visibility) AS TEXT) = 'all' OR is_public = (CAST(sqlc.arg(visibility) AS TEXT) = 'public'))
  AND (CAST(sqlc.arg(after_id) AS INTEGER) = 0
    OR name < CAST(sqlc.arg(after_name) AS TEXT) COLLATE NOCASE
    OR (name = CAST(sqlc.arg(after_name) AS TEXT) COLLATE NOCASE AND id < CAST(sqlc.arg(after_id) AS INTEGER)))
ORDER BY name COLLATE NOCASE DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: PageUserFilesSuccessAsc :many
SELECT * FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(visibility) AS TEXT) = 'all' OR is_public = (CAST(sqlc.arg(visibility) AS TEXT) = 'public'))
  AND (CAST(sqlc.arg(after_id) AS INTEGER) = 0
    OR success_count > CAST(sqlc.arg(after_n) AS INTEGER)
    OR (success_count = CAST(sqlc.arg(after_n) AS INTEGER) AND id > CAST(sqlc.arg(after_id) AS INTEGER)))
ORDER BY success_count ASC, id ASC
LIMIT sqlc.arg(page_size);

-- name: PageUserFilesSuccessDesc :many
SELECT * FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(visibility) AS TEXT) = 'all' OR is_public = (CAST(sqlc.arg(visibility) AS TEXT) = 'public'))
  AND (CAST(sqlc.arg(after_id) AS INTEGER) = 0
    OR success_count < CAST(sqlc.arg(after_n) AS INTEGER)
    OR (success_count = CAST(sqlc.arg(after_n) AS INTEGER) AND id < CAST(sqlc.arg(after_id) AS INTEGER)))
ORDER BY success_count DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: PageUserFilesFailureAsc :many
SELECT * FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(visibility) AS TEXT) = 'all' OR is_public = (CAST(sqlc.arg(visibility) AS TEXT) = 'public'))
  AND (CAST(sqlc.arg(after_id) AS INTEGER) = 0
    OR failure_count > CAST(sqlc.arg(after_n) AS INTEGER)
    OR (failure_count = CAST(sqlc.arg(after_n) AS INTEGER) AND id > CAST(sqlc.arg(after_id) AS INTEGER)))
ORDER BY failure_count ASC, id ASC
LIMIT sqlc.arg(page_size);

-- name: PageUserFilesFailureDesc :many
SELECT * FROM files
WHERE deleted_at IS NULL AND user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(visibility) AS TEXT) = 'all' OR is_public = (CAST(sqlc.arg(visibility) AS TEXT) = 'public'))
  AND (CAST(sqlc.arg(after_id) AS INTEGER) = 0
    OR failure_count < CAST(sqlc.arg(after_n) AS INTEGER)
    OR (failure_count = CAST(sqlc.arg(after_n) AS INTEGER) AND id < CAST(sqlc.arg(after_id) AS INTEGER)))
ORDER BY failure_count DESC, id DESC
LIMIT sqlc.arg(page_size);

-- The trash, most recently deleted first. deleted_at is compared through
-- unixepoch() because the cursor carries it as a number; every value in the
-- column was written by CURRENT_TIMESTAMP, so the two orders agree.
-- name: PageUserTrash :many
SELECT * FROM files
WHERE deleted_at IS NOT NULL AND user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(after_id) AS INTEGER) = 0
    OR unixepoch(deleted_at) < CAST(sqlc.arg(after_n) AS INTEGER)
    OR (unixepoch(deleted_at) = CAST(sqlc.arg(after_n) AS INTEGER) AND id < CAST(sqlc.arg(after_id) AS INTEGER)))
ORDER BY unixepoch(deleted_at) DESC, id DESC
LIMIT sqlc.arg(page_size);

-- What each view of the dashboard holds, sent with every page: with the list
-- paged, the client no longer has every row to count. An aggregate over the
-- owner's rows, which are a few hundred bytes each.
-- name: CountUserFiles :one
SELECT CAST(COALESCE(SUM(deleted_at IS NULL), 0) AS INTEGER) AS live,
       CAST(COALESCE(SUM(deleted_at IS NULL AND is_public), 0) AS INTEGER) AS public,
       CAST(COALESCE(SUM(deleted_at IS NOT NULL), 0) AS INTEGER) AS trashed
FROM files
WHERE user_id = sqlc.arg(user_id);

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
-- since a site is edited through different statements than a document, and
-- the blob, so an edit that leaves the content as it was keeps it rather than
-- writing an identical copy.
-- name: GetUserFileSize :one
SELECT files.content_size, files.kind, files.blob_id, blobs.sha256 FROM files
JOIN blobs ON blobs.id = files.blob_id
WHERE files.slug = sqlc.arg(slug) AND files.user_id = sqlc.arg(user_id) AND files.deleted_at IS NULL;

-- The default search: a file's name and its blob's search_meta (title,
-- description, keywords... see internal/searchmeta), never its content, so it
-- is answered from the database alone. Plain substring search via instr()
-- rather than LIKE: no wildcard/escape handling needed, and lower() gives the
-- same ASCII-only case-insensitivity LIKE would. search_meta is NULL for a
-- blob not yet filled in, which matches nothing.
-- name: SearchUserFiles :many
SELECT sqlc.embed(files), CAST(COALESCE(blobs.search_meta, '') AS TEXT) AS search_meta
FROM files
JOIN blobs ON blobs.id = files.blob_id
WHERE files.deleted_at IS NULL AND files.user_id = sqlc.arg(user_id)
  AND (instr(lower(files.name), lower(sqlc.arg(query))) > 0
    OR instr(lower(COALESCE(blobs.search_meta, '')), lower(sqlc.arg(query))) > 0)
ORDER BY files.created_at DESC;

-- The candidates for a full-text search, which matches in Go after reading
-- each document from its blob (handlers/search.go): every live file with its
-- search_meta, so a site -- whose zip is not text -- can still match there.
-- name: ListUserFilesWithSearchMeta :many
SELECT sqlc.embed(files), CAST(COALESCE(blobs.search_meta, '') AS TEXT) AS search_meta
FROM files
JOIN blobs ON blobs.id = files.blob_id
WHERE files.deleted_at IS NULL AND files.user_id = sqlc.arg(user_id)
ORDER BY files.created_at DESC;

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
-- Go check someone can forget: pointing a site at a document's blob would
-- serve that document's source as a zip, and the archive would be lost.
-- name: UpdateFile :one
UPDATE files
SET name = sqlc.arg(name),
    slug = sqlc.arg(new_slug),
    blob_id = sqlc.arg(blob_id),
    content_size = (SELECT size FROM blobs WHERE id = sqlc.arg(blob_id)),
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

-- Replaces a site's archive, but only if it is still the one the caller read
-- (old_blob_id). Building the new archive happens outside any transaction --
-- MCP's update_site reads the old one, merges a patch into it and writes the
-- result to disk -- so this compare-and-swap is what keeps two concurrent
-- edits from silently overwriting each other, without holding SQLite's write
-- lock for the duration of that disk work. No row means "gone, or changed
-- meanwhile"; the caller tells the two apart.
-- name: ReplaceSiteBlob :one
UPDATE files
SET blob_id = sqlc.arg(blob_id),
    content_size = (SELECT size FROM blobs WHERE id = sqlc.arg(blob_id)),
    updated_at = CURRENT_TIMESTAMP
WHERE slug = sqlc.arg(slug) AND user_id = sqlc.arg(user_id) AND deleted_at IS NULL
  AND kind = 'site' AND blob_id = sqlc.arg(old_blob_id)
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

-- The site render path. It used to come in two halves, so that answering a
-- 304 did not load the archive; the archive is a blob on disk now, opened
-- only when bytes are served, and this row is all access control, counting
-- and the conditional check need. Keeps the users join: suspension holds
-- here too.
-- name: GetSiteBySlugAnyOwner :one
SELECT files.* FROM files
JOIN users ON users.id = files.user_id
WHERE files.slug = ? AND files.kind = 'site'
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
