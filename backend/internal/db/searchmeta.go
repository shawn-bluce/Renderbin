package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/searchmeta"
)

// fillBatch is how many blobs FillSearchMeta reads per query.
const fillBatch = 200

// FillSearchMeta computes blobs.search_meta wherever it is still NULL, from
// each blob's content and the kind of the file that uses it. It is migration
// 0012 for the blobs that predate the column, and backup.Restore runs it again
// once a restored backup's blobs are in the store.
//
// A blob that is not on disk is skipped and stays NULL rather than failing:
// during a restore, db.Open migrates the snapshot before its blobs have
// arrived, so missing blobs are expected there, and the restore fills them in
// afterwards. Orphaned blobs are skipped too -- no file, no kind, and nothing
// will search them.
func FillSearchMeta(conn *sql.DB, store *blobstore.Store) error {
	after := ""
	for {
		rows, err := conn.Query(`
			SELECT blobs.id, files.kind FROM blobs
			JOIN files ON files.blob_id = blobs.id
			WHERE blobs.search_meta IS NULL AND blobs.id > ?
			ORDER BY blobs.id LIMIT ?`, after, fillBatch)
		if err != nil {
			return err
		}
		type pending struct{ id, kind string }
		var batch []pending
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.id, &p.kind); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}

		for _, p := range batch {
			after = p.id
			data, err := store.Read(p.id)
			if errors.Is(err, blobstore.ErrNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("read blob %s: %w", p.id, err)
			}
			if _, err := conn.Exec(`UPDATE blobs SET search_meta = ? WHERE id = ?`,
				searchmeta.Extract(p.kind, data), p.id); err != nil {
				return err
			}
		}
	}
}
