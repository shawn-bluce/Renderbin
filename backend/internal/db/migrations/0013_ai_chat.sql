-- "Ask AI": conversations a signed-in user has with an LLM about one file,
-- and their messages. The model settings are configs rows (ai_base_url,
-- ai_api_key, ai_model) and need no schema.
--
-- Keep this file pure ASCII -- sqlc reads this directory for its schema, and
-- its SQLite rewriter works on byte offsets while the parser reports character
-- positions, so one multi-byte character silently corrupts codegen.
--
-- A conversation belongs to one user and one file; every query reaching it
-- filters on user_id, so nobody sees another account's conversations about the
-- same document. file_id rather than slug: a slug can be renamed.
--
-- No foreign keys and no cascade triggers. backup.Restore copies tables in name
-- order with DELETE + INSERT, so a trigger on files or users deleting chats
-- would fire *after* chat_conversations had been restored and wipe it.
-- Conversations whose file or user is gone are invisible (ids are never
-- reused, AUTOINCREMENT) and the hourly job in internal/cron removes them.
CREATE TABLE chat_conversations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    file_id INTEGER NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_chat_conversations_owner ON chat_conversations (user_id, file_id);

CREATE TABLE chat_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_chat_messages_conversation ON chat_messages (conversation_id, id);
