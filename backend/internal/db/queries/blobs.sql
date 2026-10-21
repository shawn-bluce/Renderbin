-- Blob bookkeeping (see migrations 0008/0010 and internal/blobstore).
--
-- Keep this file pure ASCII, like files.sql: sqlc's SQLite rewriter
-- substitutes parameters by byte offset, so a multi-byte character in a
-- comment silently corrupts codegen.
--
-- None of these are owner-scoped, and none needs to be: a blob ID reaches a
-- handler only out of a files row an owner-scoped query returned. InsertBlob
-- is the one a request calls, to record a blob it has just written; the rest
-- serve the collector (internal/cron) and fsck.

-- A new blob starts orphaned. The files write that follows adopts it (the
-- files_blob_adopt/files_blob_swap triggers clear orphaned_at), so if that
-- write never happens -- a slug collision, a quota race, a crash -- what is
-- left is a timestamped orphan the collector removes, not a leak.
-- search_meta is computed from the same bytes by the caller (storeContent);
-- the CAST makes it a plain string parameter, so a write records "no fields"
-- (empty) rather than leaving it NULL, which means "not computed".
-- name: InsertBlob :exec
INSERT INTO blobs (id, size, sha256, search_meta, orphaned_at)
VALUES (sqlc.arg(id), sqlc.arg(size), sqlc.arg(sha256), CAST(sqlc.arg(search_meta) AS TEXT), CURRENT_TIMESTAMP);

-- Claims every blob orphaned before cutoff (unix seconds) by deleting its
-- record, and returns the IDs whose files the collector then removes. Record
-- first, file second: if the removal fails, the file is left with no record,
-- which the collector's sweep of the directory also handles.
--
-- The NOT EXISTS is a second lock on the same door: orphaned_at is maintained
-- by triggers and should never be set on a blob a row still uses, but deleting
-- such a blob would be data loss, so it is checked here as well.
-- name: DeleteOrphanedBlobs :many
DELETE FROM blobs
WHERE orphaned_at IS NOT NULL
  AND unixepoch(orphaned_at) < CAST(sqlc.arg(cutoff) AS INTEGER)
  AND NOT EXISTS (SELECT 1 FROM files WHERE files.blob_id = blobs.id)
RETURNING id;

-- Every recorded blob, for the collector's sweep of the directory: a file on
-- disk whose ID is not in this list belongs to no one.
-- name: ListBlobIDs :many
SELECT id FROM blobs;

-- Every file and the blob it names, for fsck. A LEFT JOIN, because a file
-- whose blob record is missing is exactly what fsck is looking for (0010's
-- check triggers should make it impossible).
-- name: ListFileBlobs :many
SELECT files.id AS file_id, files.slug, files.blob_id, files.content_size,
       blobs.size AS blob_size, blobs.sha256 AS blob_sha256
FROM files
LEFT JOIN blobs ON blobs.id = files.blob_id
ORDER BY files.id;

-- What is waiting for the collector, for fsck's report.
-- name: OrphanedBlobStats :one
SELECT CAST(COUNT(*) AS INTEGER) AS blobs, CAST(COALESCE(SUM(size), 0) AS INTEGER) AS bytes
FROM blobs
WHERE orphaned_at IS NOT NULL;
