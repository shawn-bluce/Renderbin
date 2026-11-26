-- Comments on a document: a reader selects a passage and writes a note about
-- it; everyone who may read the document sees them. A reply points at a
-- top-level comment through parent_id (one level, no deeper); only a top-level
-- comment carries the quoted passage.
--
-- Keep this file pure ASCII -- sqlc reads this directory for its schema, and
-- its SQLite rewriter works on byte offsets while the parser reports character
-- positions, so one multi-byte character silently corrupts codegen.
--
-- No foreign keys and no cascade triggers, for the reason given in
-- 0013_ai_chat.sql: backup.Restore copies tables in name order with DELETE +
-- INSERT, so a trigger on files or users would fire after file_comments had
-- been restored and wipe it. Comments whose file or author is gone are
-- invisible (the listing joins users, and files are looked up first) and the
-- hourly job in internal/cron removes them.
CREATE TABLE file_comments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    parent_id INTEGER,
    quote TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_file_comments_file ON file_comments (file_id, id);
