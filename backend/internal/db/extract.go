package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
)

// ErrInsufficientSpace stops an upgrade that would fill the disk halfway
// through moving content out of the database. Nothing has been changed when it
// is returned.
var ErrInsufficientSpace = errors.New("not enough free disk space to move file content out of the database")

// extractBatchBytes bounds how much content one transaction of the extraction
// covers. The blobs are written before the transaction that records them, so
// this is also roughly how much a crash can leave behind as unreferenced
// blobs for the collector.
var extractBatchBytes int64 = 64 << 20

// diskFree is the store's free space, as a variable so a test can pretend
// the disk is full.
var diskFree = func(s *blobstore.Store) (uint64, bool) { return s.FreeBytes() }

// spaceNeeded is what extracting pending bytes of content needs free: the
// blobs themselves, plus a tenth and 64MB for the filesystem's own overhead
// and the rebuild and VACUUM that follow. The database does not shrink until
// that VACUUM, so for the duration the content exists twice.
func spaceNeeded(pending int64) uint64 {
	return uint64(pending) + uint64(pending)/10 + 64<<20
}

// extractContent is migration 0009: it writes every file's content out of
// the database as a blob and records which blob in blob_extraction, for 0010
// to join in when it rebuilds files without the content columns.
//
// It is resumable. A file already in blob_extraction is skipped, and a batch
// is recorded only after all of its blobs are durable on disk, so an
// interruption at any point leaves at worst a few unrecorded blobs, which the
// collector removes as files no row knows about.
//
// The source of a row is its archive when it has one (a site; its
// html_content is ” by design) and its html_content otherwise. content_size
// was kept equal to the sum of the two by 0006's triggers, so the blob must
// come out exactly that long; a row where it does not is refused rather than
// guessed at, since the size is what the quota counts.
func extractContent(conn *sql.DB, store *blobstore.Store) error {
	var files, pending int64
	if err := conn.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(content_size), 0) FROM files
		WHERE id NOT IN (SELECT file_id FROM blob_extraction)`).Scan(&files, &pending); err != nil {
		return err
	}
	if files == 0 {
		return nil
	}

	need := spaceNeeded(pending)
	if free, ok := diskFree(store); ok && free < need {
		return fmt.Errorf("%w: moving %d file(s) needs about %s free in %s, and %s is available",
			ErrInsufficientSpace, files, HumanBytes(need), store.Dir(), HumanBytes(free))
	}

	logger := slog.Default()
	// At startup this is the upgrade's downtime, so say what is happening;
	// it also runs when an older backup is restored.
	logger.Info("moving file content out of the database into the blob directory",
		"files", files, "bytes", pending, "blob_dir", store.Dir())
	started := time.Now()

	var doneFiles, doneBytes int64
	for {
		batch, err := extractBatch(conn, store)
		if err != nil {
			return err
		}
		if batch.files == 0 {
			break
		}
		doneFiles += batch.files
		doneBytes += batch.bytes
		logger.Info("moving file content", "files_done", doneFiles, "files_total", files,
			"bytes_done", doneBytes, "bytes_total", pending)
	}

	logger.Info("file content moved out of the database",
		"files", doneFiles, "bytes", doneBytes, "took", time.Since(started).Round(time.Millisecond).String())
	return nil
}

type batchResult struct {
	files, bytes int64
}

type extracted struct {
	fileID int64
	blob   blobstore.Blob
}

// extractBatch moves up to extractBatchBytes of content (always at least one
// file) and records it in one transaction.
func extractBatch(conn *sql.DB, store *blobstore.Store) (batchResult, error) {
	ids, err := nextBatch(conn)
	if err != nil || len(ids) == 0 {
		return batchResult{}, err
	}

	// One row at a time, so memory holds one document rather than a batch.
	done := make([]extracted, 0, len(ids))
	var bytes int64
	for _, id := range ids {
		var (
			kind    string
			size    int64
			html    []byte
			archive []byte
		)
		if err := conn.QueryRow(
			`SELECT kind, content_size, CAST(html_content AS BLOB), archive FROM files WHERE id = ?`, id,
		).Scan(&kind, &size, &html, &archive); err != nil {
			return batchResult{}, fmt.Errorf("read file %d: %w", id, err)
		}
		data := html
		if archive != nil {
			if len(html) != 0 {
				return batchResult{}, fmt.Errorf("file %d (%s) holds both a document and a site archive; refusing to guess which is its content", id, kind)
			}
			data = archive
		}
		if int64(len(data)) != size {
			return batchResult{}, fmt.Errorf("file %d: content is %d bytes but content_size says %d", id, len(data), size)
		}

		b, err := store.Put(data)
		if err != nil {
			return batchResult{}, fmt.Errorf("write file %d: %w", id, err)
		}
		done = append(done, extracted{fileID: id, blob: b})
		bytes += b.Size
	}

	tx, err := conn.Begin()
	if err != nil {
		return batchResult{}, err
	}
	defer tx.Rollback()
	for _, e := range done {
		// Not orphaned: blob_extraction is its reference until 0010 turns
		// that into files.blob_id.
		if _, err := tx.Exec(`INSERT INTO blobs (id, size, sha256, orphaned_at) VALUES (?, ?, ?, NULL)`,
			e.blob.ID, e.blob.Size, e.blob.SHA256); err != nil {
			return batchResult{}, fmt.Errorf("record blob of file %d: %w", e.fileID, err)
		}
		if _, err := tx.Exec(`INSERT INTO blob_extraction (file_id, blob_id) VALUES (?, ?)`,
			e.fileID, e.blob.ID); err != nil {
			return batchResult{}, fmt.Errorf("record extraction of file %d: %w", e.fileID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return batchResult{}, err
	}
	return batchResult{files: int64(len(done)), bytes: bytes}, nil
}

// nextBatch picks the next files to extract, by id, until their sizes add up
// to extractBatchBytes.
func nextBatch(conn *sql.DB) ([]int64, error) {
	rows, err := conn.Query(`
		SELECT id, content_size FROM files
		WHERE id NOT IN (SELECT file_id FROM blob_extraction)
		ORDER BY id LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var (
		ids   []int64
		total int64
	)
	for rows.Next() {
		var id, size int64
		if err := rows.Scan(&id, &size); err != nil {
			return nil, err
		}
		if len(ids) > 0 && total+size > extractBatchBytes {
			break
		}
		ids = append(ids, id)
		total += size
	}
	return ids, rows.Err()
}

// HumanBytes formats a byte count for the log lines and messages an operator
// reads (the migration, `server migrate --check`, `server fsck`).
func HumanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
