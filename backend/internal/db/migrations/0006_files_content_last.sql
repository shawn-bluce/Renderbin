-- Rebuild files with its two large columns, html_content and archive, LAST.
--
-- Keep this file pure ASCII -- sqlc reads this directory for its schema, and
-- its SQLite rewriter works on byte offsets while the parser reports character
-- positions, so one multi-byte character silently corrupts codegen.
--
-- Why: a row bigger than a page keeps its head in the b-tree cell and spills
-- the rest onto a linked list of overflow pages. A column stored after a big
-- value can only be reached by walking that list. html_content used to be the
-- fourth column, so every query that read deleted_at, kind, user_id or
-- content_size -- the listings, the trash, search, the quota SUM -- read every
-- document in full from disk even though it never selected one. Measured on
-- 300 files of 1MB: the listing took 46ms with html_content mid-row and
-- 0.26ms with it last, with the whole database already in the OS page cache.
--
-- SQLite cannot reorder columns, so this is the documented 12-step rebuild:
-- create, copy, drop, rename, then recreate what the drop took with it (the
-- index and the two content_size triggers).
--
-- sqlite_sequence is handled by hand. The new table's counter is only the
-- highest id copied into it, which is lower than the old one whenever the
-- newest file has been hard-deleted; keeping it would hand that id out again.
-- So the new table's row is discarded and the old one renamed onto it before
-- the drop, and the RENAME at the end carries it back to the name files.

CREATE TABLE files_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    is_public BOOLEAN NOT NULL DEFAULT 0,
    access_code TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Soft delete: the trash. A non-NULL value hides the row everywhere,
    -- including from its own owner's /res/{slug} requests.
    deleted_at TIMESTAMP,
    -- Access analytics, split by how access was granted. Bumped on every
    -- request to an existing file; deliberately does not touch updated_at.
    success_count INTEGER NOT NULL DEFAULT 0,      -- viewed by its owner
    failure_count INTEGER NOT NULL DEFAULT 0,      -- blocked attempts
    tags TEXT NOT NULL DEFAULT '',                 -- comma-separated, normalized in Go
    -- Link expiry: a TTL or a view quota, never both. Enforced lazily on
    -- access (no cron); expiring flips the file private and clears both.
    expires_at TIMESTAMP,
    max_views INTEGER,
    view_count INTEGER NOT NULL DEFAULT 0,         -- only code-based views count
    -- 'html', 'markdown', 'txt' or 'site'. Fixed at creation.
    kind TEXT NOT NULL DEFAULT 'html',
    code_success_count INTEGER NOT NULL DEFAULT 0, -- viewed with a correct access code
    -- Owner. No DEFAULT on purpose: a silent fallback would mis-attribute a
    -- file rather than fail loudly.
    user_id INTEGER NOT NULL,
    expired_at TIMESTAMP,
    expired_reason TEXT NOT NULL DEFAULT '',
    -- Bytes this row stores: html_content plus archive. Kept in step by the
    -- triggers below and by the writing statements themselves.
    content_size INTEGER NOT NULL DEFAULT 0,
    -- The two large columns. They must stay last: see the top of this file.
    -- Any column added later goes ABOVE them, which ALTER TABLE ADD COLUMN
    -- cannot do -- so a new column means another rebuild, not an ALTER.
    html_content TEXT NOT NULL,
    archive BLOB
);

INSERT INTO files_new (
    id, slug, name, is_public, access_code, created_at, updated_at, deleted_at,
    success_count, failure_count, tags, expires_at, max_views, view_count,
    kind, code_success_count, user_id, expired_at, expired_reason, content_size,
    html_content, archive
)
SELECT
    id, slug, name, is_public, access_code, created_at, updated_at, deleted_at,
    success_count, failure_count, tags, expires_at, max_views, view_count,
    kind, code_success_count, user_id, expired_at, expired_reason, content_size,
    html_content, archive
FROM files;

DELETE FROM sqlite_sequence WHERE name = 'files_new';
UPDATE sqlite_sequence SET name = 'files_new' WHERE name = 'files';

DROP TABLE files;
ALTER TABLE files_new RENAME TO files;

CREATE INDEX idx_files_owner_list ON files (user_id, deleted_at, created_at);

-- The same two triggers 0004 created; the DROP above took them with it.
CREATE TRIGGER files_content_size_after_insert
AFTER INSERT ON files
WHEN NEW.content_size <> length(CAST(NEW.html_content AS BLOB)) + COALESCE(length(NEW.archive), 0)
BEGIN
    UPDATE files SET content_size = length(CAST(NEW.html_content AS BLOB)) + COALESCE(length(NEW.archive), 0)
    WHERE id = NEW.id;
END;

CREATE TRIGGER files_content_size_after_update
AFTER UPDATE OF html_content, archive ON files
WHEN NEW.content_size <> length(CAST(NEW.html_content AS BLOB)) + COALESCE(length(NEW.archive), 0)
BEGIN
    UPDATE files SET content_size = length(CAST(NEW.html_content AS BLOB)) + COALESCE(length(NEW.archive), 0)
    WHERE id = NEW.id;
END;
