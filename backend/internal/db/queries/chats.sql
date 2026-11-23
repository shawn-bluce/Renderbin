-- "Ask AI" conversations. Keep this file pure ASCII (see files.sql).
--
-- Every conversation and message query carries the caller's user_id, so one
-- account's conversations about a document are invisible to another's: a
-- foreign conversation id matches no row, exactly like one that never existed.

-- ===========================================================================
-- CHAT ACCESS -- which file a signed-in caller may ask about.
--
-- The one file lookup here that is not plainly owner-scoped, because asking
-- about a document is allowed to whoever may read it: its owner, or anyone
-- holding the access code of a public file -- the same rule /res/{slug}
-- applies. The rule is in SQL so a handler cannot forget half of it. A public
-- file past its TTL no longer matches for a non-owner, as /res would have taken
-- it offline; the view limit is not checked, since the caller already spent
-- their view reading the page they are asking about. Deleted files and files
-- of suspended accounts never match, as in GetFileBySlugAnyOwner.
-- ===========================================================================

-- name: GetChatFile :one
SELECT files.* FROM files
JOIN users ON users.id = files.user_id
WHERE files.slug = sqlc.arg(slug)
  AND files.deleted_at IS NULL
  AND users.disabled_at IS NULL
  AND (
    files.user_id = sqlc.arg(viewer_id)
    OR (
      files.is_public = 1
      AND files.access_code = sqlc.arg(access_code)
      AND sqlc.arg(access_code) <> ''
      AND (files.expires_at IS NULL OR unixepoch(files.expires_at) > unixepoch('now'))
    )
  );

-- ===========================================================================
-- OWNER-SCOPED conversations and messages.
-- ===========================================================================

-- name: ListChatConversations :many
SELECT * FROM chat_conversations
WHERE user_id = sqlc.arg(user_id) AND file_id = sqlc.arg(file_id)
ORDER BY updated_at DESC, id DESC;

-- name: GetChatConversation :one
SELECT * FROM chat_conversations
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND file_id = sqlc.arg(file_id);

-- name: CreateChatConversation :one
INSERT INTO chat_conversations (user_id, file_id, title)
VALUES (sqlc.arg(user_id), sqlc.arg(file_id), sqlc.arg(title))
RETURNING *;

-- name: TouchChatConversation :exec
UPDATE chat_conversations SET updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: DeleteChatConversation :execrows
DELETE FROM chat_conversations
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND file_id = sqlc.arg(file_id);

-- Messages are reached through their conversation's owner, so a message query
-- cannot be pointed at someone else's conversation by id.
-- name: ListChatMessages :many
SELECT chat_messages.* FROM chat_messages
JOIN chat_conversations ON chat_conversations.id = chat_messages.conversation_id
WHERE chat_messages.conversation_id = sqlc.arg(conversation_id)
  AND chat_conversations.user_id = sqlc.arg(user_id)
ORDER BY chat_messages.id;

-- name: InsertChatMessage :one
INSERT INTO chat_messages (conversation_id, role, content)
SELECT sqlc.arg(conversation_id), sqlc.arg(role), sqlc.arg(content)
WHERE EXISTS (
  SELECT 1 FROM chat_conversations
  WHERE id = sqlc.arg(conversation_id) AND user_id = sqlc.arg(user_id)
)
RETURNING *;

-- Called after DeleteChatConversation, in the same transaction. The conversation
-- row is already gone, so this one keys on the id alone; the handler only
-- reaches it after the owner-scoped delete matched.
-- name: DeleteChatMessagesOf :exec
DELETE FROM chat_messages WHERE conversation_id = sqlc.arg(conversation_id);

-- ===========================================================================
-- HOUSEKEEPING -- internal/cron only.
-- ===========================================================================

-- Conversations whose file was hard-deleted (a trashed file keeps them, and
-- restoring it brings them back) or whose account was deleted.
-- name: DeleteOrphanedChatConversations :execrows
DELETE FROM chat_conversations
WHERE NOT EXISTS (SELECT 1 FROM files WHERE files.id = chat_conversations.file_id)
   OR NOT EXISTS (SELECT 1 FROM users WHERE users.id = chat_conversations.user_id);

-- name: DeleteOrphanedChatMessages :execrows
DELETE FROM chat_messages
WHERE NOT EXISTS (
  SELECT 1 FROM chat_conversations WHERE chat_conversations.id = chat_messages.conversation_id
);
