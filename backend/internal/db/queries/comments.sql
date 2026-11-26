-- Comments on a document. Keep this file pure ASCII (see files.sql).
--
-- Unlike conversations, comments are shared: every query here is keyed by
-- file_id, not by the caller. That is only safe because the handler resolves
-- the file first with GetChatFile, which encodes who may read a document (its
-- owner, or a signed-in holder of a public file's code), so a file_id reaches
-- these statements only for a caller allowed to read that file. Authorship is
-- checked by the handler against the row GetFileComment returns.

-- name: ListFileComments :many
SELECT c.id, c.parent_id, c.quote, c.body, c.is_ai, c.created_at, c.user_id,
       u.username AS author_username, u.nickname AS author_nickname
FROM file_comments c
JOIN users u ON u.id = c.user_id
WHERE c.file_id = sqlc.arg(file_id)
ORDER BY c.id;

-- name: GetFileComment :one
SELECT * FROM file_comments
WHERE id = sqlc.arg(id) AND file_id = sqlc.arg(file_id);

-- parent_id is NULL for a top-level comment. The parent, when given, must be a
-- top-level comment of the same file: enforced here, so no caller can nest
-- deeper or reply across documents.
-- name: InsertFileComment :one
INSERT INTO file_comments (file_id, user_id, parent_id, quote, body, is_ai)
SELECT sqlc.arg(file_id), sqlc.arg(user_id), sqlc.narg(parent_id), sqlc.arg(quote), sqlc.arg(body), sqlc.arg(is_ai)
WHERE sqlc.narg(parent_id) IS NULL OR EXISTS (
  SELECT 1 FROM file_comments p
  WHERE p.id = sqlc.narg(parent_id) AND p.file_id = sqlc.arg(file_id) AND p.parent_id IS NULL
)
RETURNING *;

-- A comment and its replies, in one statement.
-- name: DeleteFileCommentThread :execrows
DELETE FROM file_comments
WHERE file_id = sqlc.arg(file_id) AND (id = sqlc.arg(id) OR parent_id = sqlc.arg(id));

-- ===========================================================================
-- HOUSEKEEPING -- internal/cron only.
-- ===========================================================================

-- Comments whose file was hard-deleted (a trashed file keeps them) or whose
-- author's account was deleted, and replies whose parent is gone.
-- name: DeleteOrphanedFileComments :execrows
DELETE FROM file_comments
WHERE NOT EXISTS (SELECT 1 FROM files WHERE files.id = file_comments.file_id)
   OR NOT EXISTS (SELECT 1 FROM users WHERE users.id = file_comments.user_id)
   OR (parent_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM file_comments p WHERE p.id = file_comments.parent_id));
