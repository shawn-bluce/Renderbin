-- File content moves out of the database, into blobs on disk (see
-- internal/blobstore). This is the first of three steps:
--
--   0008 (this file)       the blobs table, and blob_extraction to record which
--                          blob each existing row's content went to
--   0009_extract_content   Go, not SQL (db/extract.go): writes every row's
--                          html_content/archive out as a blob
--   0010                   rebuilds files with a blob_id and without the two
--                          content columns, and drops blob_extraction
--
-- Keep this file pure ASCII -- sqlc reads this directory for its schema, and
-- its SQLite rewriter works on byte offsets while the parser reports character
-- positions, so one multi-byte character silently corrupts codegen.
--
-- blobs records every blob the store holds and whether anything still uses
-- it. orphaned_at is the whole garbage-collection protocol: it is set when the
-- last row pointing at the blob lets go of it (by the triggers 0010 creates),
-- cleared when one adopts it, and the collector removes blobs that have been
-- orphaned for longer than its grace period. A blob is inserted already
-- orphaned and adopted by the files write that follows, so a write that dies
-- between the two leaves garbage with a timestamp, never a dangling pointer.

CREATE TABLE blobs (
    id TEXT PRIMARY KEY,                 -- 32 lowercase hex characters
    size INTEGER NOT NULL,               -- bytes
    sha256 TEXT NOT NULL,                -- lowercase hex, for fsck and restore
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    orphaned_at TIMESTAMP                -- NULL while a files row uses it
);

CREATE INDEX idx_blobs_orphaned ON blobs (orphaned_at) WHERE orphaned_at IS NOT NULL;

-- Where 0009 records its progress: one row per file whose content has been
-- written out. A side table rather than a files.blob_id column, because
-- SQLite rewrites a whole row on UPDATE -- setting a column on a row holding a
-- 5MB document writes those 5MB again, so the extraction would have written
-- every byte of content to the database a second time on its way out of it.
-- It also makes 0009 resumable: a row already listed here is skipped.
CREATE TABLE blob_extraction (
    file_id INTEGER PRIMARY KEY,
    blob_id TEXT NOT NULL
);
