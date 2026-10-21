-- Rebuild files without html_content and archive. By now 0009_extract_content
-- has written every row's content out as a blob and listed it in
-- blob_extraction, which the copy below joins in as blob_id. The column is NOT
-- NULL, so a row the extraction missed fails the copy (the LEFT JOIN yields
-- NULL for it), and the whole migration rolls back with the content still in
-- place.
--
-- Keep this file pure ASCII -- sqlc reads this directory for its schema, and
-- its SQLite rewriter works on byte offsets while the parser reports character
-- positions, so one multi-byte character silently corrupts codegen.
--
-- With the two large columns gone, the column-order rule 0006 introduced no
-- longer applies: a new column can be added with ALTER TABLE ADD COLUMN again.
--
-- sqlite_sequence is carried across by hand, as in 0006: the new table's
-- counter would only be the highest id copied, which is lower than the old one
-- whenever the newest file has been hard-deleted.

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
    -- Bytes of this row's blob: the document's source, or a site's zip. The
    -- writing statements read it from blobs, and the check triggers below
    -- refuse any value that disagrees.
    content_size INTEGER NOT NULL DEFAULT 0,
    -- The content (see internal/blobstore). UNIQUE: a blob belongs to exactly
    -- one row, and an edit writes a new one rather than sharing.
    blob_id TEXT NOT NULL UNIQUE
);

INSERT INTO files_new (
    id, slug, name, is_public, access_code, created_at, updated_at, deleted_at,
    success_count, failure_count, tags, expires_at, max_views, view_count,
    kind, code_success_count, user_id, expired_at, expired_reason, content_size,
    blob_id
)
SELECT
    f.id, f.slug, f.name, f.is_public, f.access_code, f.created_at, f.updated_at, f.deleted_at,
    f.success_count, f.failure_count, f.tags, f.expires_at, f.max_views, f.view_count,
    f.kind, f.code_success_count, f.user_id, f.expired_at, f.expired_reason, f.content_size,
    x.blob_id
FROM files f
LEFT JOIN blob_extraction x ON x.file_id = f.id;

DELETE FROM sqlite_sequence WHERE name = 'files_new';
UPDATE sqlite_sequence SET name = 'files_new' WHERE name = 'files';

-- Takes 0006's two content_size triggers with it; they measured the columns
-- that are going away.
DROP TABLE files;
ALTER TABLE files_new RENAME TO files;
DROP TABLE blob_extraction;

CREATE INDEX idx_files_owner_list ON files (user_id, deleted_at, created_at);

-- content_size must be its blob's size, and the blob must exist: a NULL from
-- the subquery (no such blob) fails the IS NOT comparison too. This replaces
-- the old guarantee that the size was computed from the content in the same
-- statement -- the content is no longer in the database to compute it from.
CREATE TRIGGER files_blob_check_insert
BEFORE INSERT ON files
WHEN NEW.content_size IS NOT (SELECT size FROM blobs WHERE id = NEW.blob_id)
BEGIN
    SELECT RAISE(ABORT, 'files.content_size does not match its blob');
END;

CREATE TRIGGER files_blob_check_update
BEFORE UPDATE OF blob_id, content_size ON files
WHEN NEW.content_size IS NOT (SELECT size FROM blobs WHERE id = NEW.blob_id)
BEGIN
    SELECT RAISE(ABORT, 'files.content_size does not match its blob');
END;

-- Reference tracking for the collector (see 0008). Adopting a blob clears its
-- orphaned_at; letting go of one stamps it, unless another row still uses it
-- (UNIQUE makes that impossible today; the check keeps the triggers correct
-- if that ever changes).
CREATE TRIGGER files_blob_adopt
AFTER INSERT ON files
BEGIN
    UPDATE blobs SET orphaned_at = NULL WHERE id = NEW.blob_id;
END;

CREATE TRIGGER files_blob_swap
AFTER UPDATE OF blob_id ON files
WHEN NEW.blob_id IS NOT OLD.blob_id
BEGIN
    UPDATE blobs SET orphaned_at = NULL WHERE id = NEW.blob_id;
    UPDATE blobs SET orphaned_at = CURRENT_TIMESTAMP
    WHERE id = OLD.blob_id
      AND NOT EXISTS (SELECT 1 FROM files WHERE blob_id = OLD.blob_id);
END;

CREATE TRIGGER files_blob_release
AFTER DELETE ON files
BEGIN
    UPDATE blobs SET orphaned_at = CURRENT_TIMESTAMP
    WHERE id = OLD.blob_id
      AND NOT EXISTS (SELECT 1 FROM files WHERE blob_id = OLD.blob_id);
END;
